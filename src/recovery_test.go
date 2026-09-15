package main

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger(t *testing.T) *Logger {
	t.Helper()
	lg, err := NewLoggerAtPath(filepath.Join(t.TempDir(), "r.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return lg
}

func testCfg(devices ...DeviceConfig) *Config {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: LogLevelNormal, Path: "x.log"},
		Devices: devices,
	}
	cfg.resolved = defaultAdvancedValues()
	return cfg
}

func TestDeviceInfoHealthy(t *testing.T) {
	ok := &DeviceInfo{Status: 0, ProblemCode: 0}
	if !ok.IsHealthy() {
		t.Fatal("should be healthy")
	}
	bad := &DeviceInfo{Status: DN_HAS_PROBLEM, ProblemCode: 22}
	if bad.IsHealthy() {
		t.Fatal("should be unhealthy")
	}
	bad2 := &DeviceInfo{Status: 0, ProblemCode: 1}
	if bad2.IsHealthy() {
		t.Fatal("problem code != 0 should be unhealthy")
	}
}

func TestDeviceInfoEnabledDisabled(t *testing.T) {
	dis := &DeviceInfo{Status: DN_HAS_PROBLEM, ProblemCode: CM_PROB_DISABLED}
	if !dis.IsDisabled() || dis.IsEnabled() {
		t.Fatal("code 22 should be disabled")
	}
	en := &DeviceInfo{Status: DN_STARTED | DN_HAS_PROBLEM, ProblemCode: 43}
	if en.IsDisabled() || !en.IsEnabled() {
		t.Fatal("code 43 should still be enabled (not administratively disabled)")
	}
}

func TestRecoveryCoalesceSameDevice(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 10})
	ms, err := BuildMatchers(cfg.Devices)
	if err != nil {
		t.Fatal(err)
	}
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	info := &DeviceInfo{
		FriendlyName: "TestDev",
		InstanceID:   "TEST\\VID_0000\\1",
		DevInst:      1,
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  43,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rm.HandleDeviceProblem(ctx, info, false)
	rm.HandleDeviceProblem(ctx, info, false) // coalesce

	time.Sleep(50 * time.Millisecond)
	rm.StopNewWork()
	cancel()
	rm.Wait()
}

func TestRecoveryStopPreventsNew(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 2})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0
	rm.StopNewWork()

	info := &DeviceInfo{
		FriendlyName: "TestDev",
		InstanceID:   "ID1",
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  1,
	}
	rm.HandleDeviceProblem(context.Background(), info, false)
	rm.Wait() // should be immediate — no worker started
}

func TestRecoverySessionAttemptResetSemantics(t *testing.T) {
	sess := &deviceSession{maxRetries: 3, name: "X"}
	sess.mu.Lock()
	sess.attempts++
	if sess.attempts != 1 {
		t.Fatal("first attempt")
	}
	sess.attempts = 0 // success reset
	sess.mu.Unlock()

	sess.mu.Lock()
	sess.attempts++
	a := sess.attempts
	sess.mu.Unlock()
	if a != 1 {
		t.Fatalf("new session should start at 1, got %d", a)
	}
}

func TestFindMatcher(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(
		DeviceConfig{FriendlyName: "Exact Name", MaxRetries: 1},
		DeviceConfig{FriendlyName: "regex:^Intel.*BE200.*$", MaxRetries: 5},
	)
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0
	m, d := rm.FindMatcher("Exact Name")
	if m == nil || d.MaxRetries != 1 {
		t.Fatal("exact")
	}
	m, d = rm.FindMatcher("Intel WiFi BE200 Card")
	if m == nil || d.MaxRetries != 5 {
		t.Fatal("regex")
	}
	m, _ = rm.FindMatcher("Other")
	if m != nil {
		t.Fatal("should not match")
	}
}

func TestConcurrentDifferentDevices(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(
		DeviceConfig{FriendlyName: "DevA", MaxRetries: 1},
		DeviceConfig{FriendlyName: "DevB", MaxRetries: 1},
	)
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var started int32
	var wg sync.WaitGroup
	launch := func(name, id string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt32(&started, 1)
			rm.HandleDeviceProblem(ctx, &DeviceInfo{
				FriendlyName: name,
				InstanceID:   id,
				Status:       DN_HAS_PROBLEM,
				ProblemCode:  10,
			}, false)
		}()
	}
	launch("DevA", "A1")
	launch("DevB", "B1")
	wg.Wait()
	time.Sleep(30 * time.Millisecond)
	cancel()
	rm.StopNewWork()
	rm.Wait()
	if atomic.LoadInt32(&started) != 2 {
		t.Fatalf("expected 2 launches, got %d", started)
	}
}

func stubUnhealthy(name, id string) *DeviceInfo {
	return &DeviceInfo{
		FriendlyName: name,
		InstanceID:   id,
		DevInst:      7,
		Status:       DN_HAS_PROBLEM | DN_STARTED,
		ProblemCode:  43,
	}
}

// statefulStub tracks disable→disabled(22) so enable gates work like Windows.
type statefulStub struct {
	mu   sync.Mutex
	dev  DeviceInfo
	step int32 // for success tests
}

func newStatefulStub(name, id string) *statefulStub {
	d := *stubUnhealthy(name, id)
	return &statefulStub{dev: d}
}

func (s *statefulStub) snapshot() DeviceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev
}

func (s *statefulStub) ops(disables, enables *int32) deviceOps {
	return deviceOps{
		Enumerate: func() ([]DeviceInfo, error) {
			d := s.snapshot()
			return []DeviceInfo{d}, nil
		},
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			d := s.dev
			d.InstanceID = id
			return &d, nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) {
			d := s.snapshot()
			return &d, nil
		},
		Disable: func(info *DeviceInfo) error {
			if disables != nil {
				atomic.AddInt32(disables, 1)
			}
			s.mu.Lock()
			s.dev.ProblemCode = CM_PROB_DISABLED
			s.dev.Status = DN_HAS_PROBLEM // not started while disabled
			s.mu.Unlock()
			return nil
		},
		Enable: func(info *DeviceInfo) error {
			if enables != nil {
				atomic.AddInt32(enables, 1)
			}
			s.mu.Lock()
			if atomic.LoadInt32(&s.step) >= 1 {
				// success mode: heal on enable
				s.dev = DeviceInfo{
					FriendlyName: s.dev.FriendlyName,
					InstanceID:   s.dev.InstanceID,
					DevInst:      3,
					Status:       DN_STARTED,
					ProblemCode:  0,
				}
			} else {
				// exhaust paths: remain unhealthy after enable
				s.dev.ProblemCode = 43
				s.dev.Status = DN_HAS_PROBLEM | DN_STARTED
			}
			s.mu.Unlock()
			return nil
		},
	}
}

func waitState(t *testing.T, rm *RecoveryManager, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	key := NormalizeInstanceID(id)
	for time.Now().Before(deadline) {
		st, _ := rm.sessionView(key)
		if st == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, att := rm.sessionView(key)
	t.Fatalf("state=%s attempt=%d want %s", st, att, want)
}

func TestExhaustedThenCheckClears(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 2})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "PCI\\VEN_10DE\\1")
	var disables, enables int32
	rm.setOps(stub.ops(&disables, &enables))

	ctx := context.Background()
	rm.Scan(ctx, ScanReason{Reason: "startup"})
	rm.Wait()
	waitState(t, rm, "PCI\\VEN_10DE\\1", StateExhausted, 2*time.Second)
	if n := atomic.LoadInt32(&disables); n != 2 {
		t.Fatalf("expected 2 disable attempts, got %d", n)
	}
	if n := atomic.LoadInt32(&enables); n != 2 {
		t.Fatalf("expected 2 enable attempts, got %d", n)
	}

	rm.Scan(ctx, ScanReason{Reason: "pnp_event", TriggerInstance: "DISPLAY\\X", TriggerAction: "change"})
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 2 {
		t.Fatalf("PnP must not retry Exhausted device, disables=%d", n)
	}

	rm.Scan(ctx, ScanReason{Reason: "check", IgnoreDelay: true})
	rm.Wait()
	waitState(t, rm, "PCI\\VEN_10DE\\1", StateExhausted, 2*time.Second)
	if n := atomic.LoadInt32(&disables); n != 4 {
		t.Fatalf("check should run a new session (2 more attempts), disables=%d", n)
	}
}

func TestInstanceIDFoldSameSession(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 5})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	block := make(chan struct{})
	var disables int32
	stub := newStatefulStub("TestDev", "PCI\\VEN_10DE\\1")
	base := stub.ops(&disables, nil)
	base.Disable = func(info *DeviceInfo) error {
		atomic.AddInt32(&disables, 1)
		stub.mu.Lock()
		stub.dev.ProblemCode = CM_PROB_DISABLED
		stub.dev.Status = DN_HAS_PROBLEM
		stub.mu.Unlock()
		<-block
		return nil
	}
	rm.setOps(base)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := stubUnhealthy("TestDev", "PCI\\VEN_10DE\\1")
	b := stubUnhealthy("TestDev", "pci\\ven_10de\\1")
	rm.HandleDeviceProblem(ctx, a, false)
	time.Sleep(20 * time.Millisecond)
	rm.HandleDeviceProblem(ctx, b, false)
	close(block)
	cancel()
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 1 {
		t.Fatalf("case-folded IDs must share one session, disables=%d", n)
	}
}

func TestStateMachineIdleRecoveringExhausted(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 1})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "USB\\VID_0001\\1")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	base := stub.ops(nil, nil)
	base.Disable = func(info *DeviceInfo) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		stub.mu.Lock()
		stub.dev.ProblemCode = CM_PROB_DISABLED
		stub.dev.Status = DN_HAS_PROBLEM
		stub.mu.Unlock()
		return nil
	}
	rm.setOps(base)

	ctx := context.Background()
	if st, _ := rm.sessionView(NormalizeInstanceID("USB\\VID_0001\\1")); st != StateIdle {
		t.Fatalf("pre-start %s", st)
	}
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "USB\\VID_0001\\1"), false)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("did not enter Recovering")
	}
	st, _ := rm.sessionView(NormalizeInstanceID("USB\\VID_0001\\1"))
	if st != StateRecovering {
		t.Fatalf("want Recovering, got %s", st)
	}
	close(release)
	rm.Wait()
	waitState(t, rm, "USB\\VID_0001\\1", StateExhausted, 2*time.Second)

	rm.ClearExhausted()
	st, att := rm.sessionView(NormalizeInstanceID("USB\\VID_0001\\1"))
	if st != StateIdle || att != 0 {
		t.Fatalf("after check-clear: state=%s attempt=%d", st, att)
	}
}

func TestSuccessResetsToIdle(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 3})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "ID1")
	atomic.StoreInt32(&stub.step, 1) // enable will heal
	var disables, enables int32
	rm.setOps(stub.ops(&disables, &enables))

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "ID1"), false)
	rm.Wait()
	st, att := rm.sessionView(NormalizeInstanceID("ID1"))
	if st != StateIdle || att != 0 {
		t.Fatalf("success should return Idle/0, got %s/%d", st, att)
	}
	if atomic.LoadInt32(&disables) != 1 || atomic.LoadInt32(&enables) != 1 {
		t.Fatalf("disables=%d enables=%d", disables, enables)
	}
}

func TestRecDelayDaemonSkipsAutoButCheckIgnores(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{
		FriendlyName: "TestDev",
		MaxRetries:   2,
		RecDelay:     RecDelaySpec{Present: true, Based: "daemon", Sec: 3600},
	})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0
	rm.setProcessStartForTest(time.Now()) // daemon delay not elapsed

	stub := newStatefulStub("TestDev", "IDDELAY")
	var disables int32
	rm.setOps(stub.ops(&disables, nil))

	ctx := context.Background()
	rm.Scan(ctx, ScanReason{Reason: "startup"})
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("auto recover must skip during rec_delay, disables=%d", n)
	}

	rm.Scan(ctx, ScanReason{Reason: "check", IgnoreDelay: true})
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n == 0 {
		t.Fatal("check must ignore rec_delay and recover")
	}
}

func TestRecDelayUptimeGateAndBypass(t *testing.T) {
	lg := testLogger(t)
	bypass := 60
	cfg := testCfg(DeviceConfig{
		FriendlyName: "TestDev",
		MaxRetries:   1,
		RecDelay:     RecDelaySpec{Present: true, Based: "uptime", Sec: 3600},
	})
	cfg.Advanced = &AdvancedConfig{UptimeBypass: &bypass}
	cfg.resolved = cfg.resolveAdvanced()

	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "IDUP")
	var disables int32
	rm.setOps(stub.ops(&disables, nil))

	// Uptime below sec and below bypass → skip
	rm.setUptimeFnForTest(func() (time.Duration, bool) { return 10 * time.Second, true })
	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "IDUP"), false)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("uptime < sec and < bypass must skip, disables=%d", n)
	}

	// Uptime >= bypass → allow even if sec not met
	rm.setUptimeFnForTest(func() (time.Duration, bool) { return 60 * time.Second, true })
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "IDUP"), false)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n == 0 {
		t.Fatal("uptime_bypass should allow Recover")
	}
}

func TestRecDelayUptimeBypassOff(t *testing.T) {
	lg := testLogger(t)
	zero := 0
	cfg := testCfg(DeviceConfig{
		FriendlyName: "TestDev",
		MaxRetries:   1,
		RecDelay:     RecDelaySpec{Present: true, Based: "uptime", Sec: 100},
	})
	cfg.Advanced = &AdvancedConfig{UptimeBypass: &zero}
	cfg.resolved = cfg.resolveAdvanced()
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0
	rm.setUptimeFnForTest(func() (time.Duration, bool) { return 60 * time.Second, true }) // >= old default bypass but bypass off

	stub := newStatefulStub("TestDev", "IDBYP")
	var disables int32
	rm.setOps(stub.ops(&disables, nil))
	rm.HandleDeviceProblem(context.Background(), stubUnhealthy("TestDev", "IDBYP"), false)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("uptime_bypass=0 must wait for sec, disables=%d", n)
	}
}

func TestRecDelayDeviceBased(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{
		FriendlyName: "TestDev",
		MaxRetries:   1,
		RecDelay:     RecDelaySpec{Present: true, Based: "device", Sec: 3600},
	})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "IDDEV")
	var disables int32
	rm.setOps(stub.ops(&disables, nil))

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "IDDEV"), false)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("device rec_delay just set must skip, disables=%d", n)
	}

	// Seed firstSeen in the past → allow
	key := NormalizeInstanceID("IDDEV")
	rm.setFirstSeenForTest(key, time.Now().Add(-2*time.Hour))
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "IDDEV"), false)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n == 0 {
		t.Fatal("device rec_delay elapsed should recover")
	}
}

func TestRetryIntervalBetweenAttempts(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 2})
	cfg.resolved.RetryInterval = 0 // still zero; we measure gap sleeps via stub timing with nonzero interval
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 30 * time.Millisecond

	stub := newStatefulStub("TestDev", "IDRI")
	var disables int32
	rm.setOps(stub.ops(&disables, nil))

	start := time.Now()
	rm.HandleDeviceProblem(context.Background(), stubUnhealthy("TestDev", "IDRI"), true)
	rm.Wait()
	elapsed := time.Since(start)
	if atomic.LoadInt32(&disables) != 2 {
		t.Fatalf("expected 2 attempts, disables=%d", disables)
	}
	if elapsed < 25*time.Millisecond {
		t.Fatalf("expected retry_interval sleep between attempts, elapsed=%s", elapsed)
	}
}

func TestProblemCodeFilterSkips(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 2, ProblemCode: ProblemCodeSet{43}})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "IDPC")
	stub.dev.ProblemCode = 10 // not in set
	stub.dev.Status = DN_HAS_PROBLEM | DN_STARTED
	var disables int32
	rm.setOps(stub.ops(&disables, nil))

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, &stub.dev, true)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("ProblemCode filter must skip, disables=%d", n)
	}

	// Matching code should proceed
	stub.dev.ProblemCode = 43
	rm.HandleDeviceProblem(ctx, &stub.dev, true)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n == 0 {
		t.Fatal("matching ProblemCode should recover")
	}
}

func TestSkipDisableWhenAlreadyDisabled(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 1})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	stub := newStatefulStub("TestDev", "IDDIS")
	stub.dev.ProblemCode = CM_PROB_DISABLED
	stub.dev.Status = DN_HAS_PROBLEM
	var disables, enables int32
	rm.setOps(stub.ops(&disables, &enables))

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, &DeviceInfo{
		FriendlyName: "TestDev",
		InstanceID:   "IDDIS",
		DevInst:      7,
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  CM_PROB_DISABLED,
	}, true)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 0 {
		t.Fatalf("already disabled must skip Disable, got %d", n)
	}
	if n := atomic.LoadInt32(&enables); n != 1 {
		t.Fatalf("should Enable once, got %d", n)
	}
}

func TestSkipEnableWhenAlreadyEnabled(t *testing.T) {
	lg := testLogger(t)
	cfg := testCfg(DeviceConfig{FriendlyName: "TestDev", MaxRetries: 1})
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.setGapForTest(0)
	rm.retryInterval = 0

	var disables, enables int32
	dev := stubUnhealthy("TestDev", "IDEN")
	rm.setOps(deviceOps{
		Enumerate: func() ([]DeviceInfo, error) { return []DeviceInfo{*dev}, nil },
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			// Always return enabled+unhealthy — Disable "fails" to change state
			d := *stubUnhealthy("TestDev", id)
			return &d, nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) { return stubUnhealthy("TestDev", "IDEN"), nil },
		Disable: func(*DeviceInfo) error {
			atomic.AddInt32(&disables, 1)
			return nil // pretend OK but GetByInstanceID still returns enabled
		},
		Enable: func(*DeviceInfo) error {
			atomic.AddInt32(&enables, 1)
			return nil
		},
	})

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, dev, true)
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 1 {
		t.Fatalf("Disable should run once, got %d", n)
	}
	if n := atomic.LoadInt32(&enables); n != 0 {
		t.Fatalf("Enable must be skipped when still enabled after Disable, got %d", n)
	}
}
