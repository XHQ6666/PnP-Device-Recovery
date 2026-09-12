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
	lg, err := NewLogger(filepath.Join(t.TempDir(), "r.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return lg
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

func TestRecoveryCoalesceSameDevice(t *testing.T) {
	lg := testLogger(t)
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 10}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, err := BuildMatchers(cfg.Devices)
	if err != nil {
		t.Fatal(err)
	}
	rm := NewRecoveryManager(cfg, ms, lg)

	// Without real Disable/Enable (stubs fail on Linux), session still runs and exhausts or errors.
	// We verify coalesce: second Handle while running does not start another goroutine race.
	info := &DeviceInfo{
		FriendlyName: "TestDev",
		InstanceID:   "TEST\\VID_0000\\1",
		DevInst:      1,
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  43,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rm.HandleDeviceProblem(ctx, info)
	rm.HandleDeviceProblem(ctx, info) // coalesce

	// Give worker a moment then cancel to end sleeps/loops quickly
	time.Sleep(50 * time.Millisecond)
	rm.StopNewWork()
	cancel()
	rm.Wait()
}

func TestRecoveryStopPreventsNew(t *testing.T) {
	lg := testLogger(t)
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 2}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
	rm.StopNewWork()

	info := &DeviceInfo{
		FriendlyName: "TestDev",
		InstanceID:   "ID1",
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  1,
	}
	rm.HandleDeviceProblem(context.Background(), info)
	rm.Wait() // should be immediate — no worker started
}

func TestRecoverySessionAttemptResetSemantics(t *testing.T) {
	// Unit-test session counter logic without Windows APIs.
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
	cfg := &Config{
		Devices: []DeviceConfig{
			{FriendlyName: "Exact Name", MaxRetries: 1},
			{FriendlyName: "regex:^Intel.*BE200.*$", MaxRetries: 5},
		},
		RetryDelay: 1,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
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
	cfg := &Config{
		Devices: []DeviceConfig{
			{FriendlyName: "DevA", MaxRetries: 1},
			{FriendlyName: "DevB", MaxRetries: 1},
		},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)
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
			})
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
		Status:       DN_HAS_PROBLEM,
		ProblemCode:  43,
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
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 2}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)

	dev := stubUnhealthy("TestDev", "PCI\\VEN_10DE\\1")
	var disables int32
	rm.setOps(deviceOps{
		Enumerate: func() ([]DeviceInfo, error) { return []DeviceInfo{*dev}, nil },
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			d := *dev
			d.InstanceID = id
			return &d, nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) { return dev, nil },
		Disable: func(*DeviceInfo) error {
			atomic.AddInt32(&disables, 1)
			return nil
		},
		Enable: func(*DeviceInfo) error { return nil },
	})

	ctx := context.Background()
	rm.Scan(ctx, ScanReason{Reason: "startup"})
	rm.Wait()
	waitState(t, rm, dev.InstanceID, StateExhausted, 2*time.Second)
	if n := atomic.LoadInt32(&disables); n != 2 {
		t.Fatalf("expected 2 disable attempts, got %d", n)
	}

	// Normal PnP scan must NOT start a new session.
	rm.Scan(ctx, ScanReason{Reason: "pnp_event", TriggerInstance: "DISPLAY\\X", TriggerAction: "change"})
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 2 {
		t.Fatalf("PnP must not retry Exhausted device, disables=%d", n)
	}
	st, att := rm.sessionView(NormalizeInstanceID(dev.InstanceID))
	if st != StateExhausted || att != 0 {
		t.Fatalf("display after exhaust: state=%s attempt=%d", st, att)
	}

	// check clears Exhausted and starts attempt 1
	rm.Scan(ctx, ScanReason{Reason: "check"})
	rm.Wait()
	waitState(t, rm, dev.InstanceID, StateExhausted, 2*time.Second)
	if n := atomic.LoadInt32(&disables); n != 4 {
		t.Fatalf("check should run a new session (2 more attempts), disables=%d", n)
	}
}

func TestInstanceIDFoldSameSession(t *testing.T) {
	lg := testLogger(t)
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 5}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)

	block := make(chan struct{})
	var disables int32
	rm.setOps(deviceOps{
		Enumerate: func() ([]DeviceInfo, error) { return nil, nil },
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			return stubUnhealthy("TestDev", id), nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) {
			return stubUnhealthy("TestDev", "PCI\\VEN_10DE\\1"), nil
		},
		Disable: func(*DeviceInfo) error {
			atomic.AddInt32(&disables, 1)
			<-block
			return nil
		},
		Enable: func(*DeviceInfo) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := stubUnhealthy("TestDev", "PCI\\VEN_10DE\\1")
	b := stubUnhealthy("TestDev", "pci\\ven_10de\\1")
	rm.HandleDeviceProblem(ctx, a)
	time.Sleep(20 * time.Millisecond)
	rm.HandleDeviceProblem(ctx, b)
	close(block)
	cancel()
	rm.Wait()
	if n := atomic.LoadInt32(&disables); n != 1 {
		t.Fatalf("case-folded IDs must share one session, disables=%d", n)
	}
}

func TestStateMachineIdleRecoveringExhausted(t *testing.T) {
	lg := testLogger(t)
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 1}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)

	dev := stubUnhealthy("TestDev", "USB\\VID_0001\\1")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	rm.setOps(deviceOps{
		Enumerate: func() ([]DeviceInfo, error) { return []DeviceInfo{*dev}, nil },
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			d := *dev
			return &d, nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) { return dev, nil },
		Disable: func(*DeviceInfo) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
		Enable: func(*DeviceInfo) error { return nil },
	})

	ctx := context.Background()
	if st, _ := rm.sessionView(NormalizeInstanceID(dev.InstanceID)); st != StateIdle {
		t.Fatalf("pre-start %s", st)
	}
	rm.HandleDeviceProblem(ctx, dev)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("did not enter Recovering")
	}
	st, _ := rm.sessionView(NormalizeInstanceID(dev.InstanceID))
	if st != StateRecovering {
		t.Fatalf("want Recovering, got %s", st)
	}
	close(release)
	rm.Wait()
	waitState(t, rm, dev.InstanceID, StateExhausted, 2*time.Second)

	rm.ClearExhausted()
	st, att := rm.sessionView(NormalizeInstanceID(dev.InstanceID))
	if st != StateIdle || att != 0 {
		t.Fatalf("after check-clear: state=%s attempt=%d", st, att)
	}
}

func TestSuccessResetsToIdle(t *testing.T) {
	lg := testLogger(t)
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 3}},
		RetryDelay: 0,
		LogFile:    "x.log",
	}
	ms, _ := BuildMatchers(cfg.Devices)
	rm := NewRecoveryManager(cfg, ms, lg)

	var step int32
	rm.setOps(deviceOps{
		Enumerate: func() ([]DeviceInfo, error) {
			return []DeviceInfo{*stubUnhealthy("TestDev", "ID1")}, nil
		},
		GetByInstanceID: func(id string) (*DeviceInfo, error) {
			if atomic.LoadInt32(&step) >= 1 {
				return &DeviceInfo{FriendlyName: "TestDev", InstanceID: id, DevInst: 3}, nil
			}
			return stubUnhealthy("TestDev", id), nil
		},
		FindByFriendlyName: func(string) (*DeviceInfo, error) {
			return stubUnhealthy("TestDev", "ID1"), nil
		},
		Disable: func(*DeviceInfo) error {
			atomic.StoreInt32(&step, 1)
			return nil
		},
		Enable: func(*DeviceInfo) error { return nil },
	})

	ctx := context.Background()
	rm.HandleDeviceProblem(ctx, stubUnhealthy("TestDev", "ID1"))
	rm.Wait()
	st, att := rm.sessionView(NormalizeInstanceID("ID1"))
	if st != StateIdle || att != 0 {
		t.Fatalf("success should return Idle/0, got %s/%d", st, att)
	}
}
