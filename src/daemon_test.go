package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	lg := testLogger(t)
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: LogLevelNormal, Path: "x.log"},
		Devices:    []DeviceConfig{{FriendlyName: "TestDev", MaxRetries: 2}},
		RetryDelay: 0,
	}
	ms, err := BuildMatchers(cfg.Devices)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(cfg, ms, lg)
	t.Cleanup(func() {
		d.coalescer.Stop()
		d.cancel()
	})
	return d
}

func TestDebounceCoalesceTrailingEdge(t *testing.T) {
	var n int32
	var lastI, lastA string
	var mu sync.Mutex
	c := newEventCoalescer(40*time.Millisecond, func(i, a string) {
		mu.Lock()
		lastI, lastA = i, a
		mu.Unlock()
		atomic.AddInt32(&n, 1)
	})
	defer c.Stop()

	c.Trigger("A", "arrival")
	c.Trigger("DISPLAY\\FOO", "change")
	c.Trigger("B", "nodes_changed")
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&n) != 0 {
		t.Fatal("should not fire before debounce interval")
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected one coalesced fire, got %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastI != "B" || lastA != "nodes_changed" {
		t.Fatalf("last trigger=%q %q", lastI, lastA)
	}
}

func TestStartupPnPDoesNotRecover(t *testing.T) {
	d := newTestDaemon(t)
	var scans int32
	d.coalescer = newEventCoalescer(15*time.Millisecond, func(i, a string) {
		atomic.AddInt32(&scans, 1)
	})
	d.SetPhase(PhaseWaitForSystemReady)
	d.OnPnPEvent(PnPEvent{Kind: "arrival", InstanceID: "DISPLAY\\BAR"})
	d.OnPnPEvent(PnPEvent{Kind: "change", InstanceID: "PCI\\VEN_10DE\\1"})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&scans) != 0 {
		t.Fatal("PnP events must not trigger scan during WAIT_FOR_SYSTEM_READY")
	}
	if !d.pendingStartup() {
		t.Fatal("expected pending startup PnP flag")
	}
}

func TestNormalRunningDebouncesDisplayIntoScan(t *testing.T) {
	d := newTestDaemon(t)
	var n int32
	var lastI, lastA string
	var mu sync.Mutex
	d.SetPhase(PhaseNormalRunning)
	d.coalescer = newEventCoalescer(30*time.Millisecond, func(i, a string) {
		mu.Lock()
		lastI, lastA = i, a
		mu.Unlock()
		atomic.AddInt32(&n, 1)
	})
	d.OnPnPEvent(PnPEvent{Kind: "change", InstanceID: "DISPLAY\\LCD"})
	d.OnPnPEvent(PnPEvent{Kind: "arrival", InstanceID: "PCI\\VEN_10DE\\1"})
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected 1 coalesced scan, got %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastI != "PCI\\VEN_10DE\\1" || lastA != "arrival" {
		t.Fatalf("expected last trigger to be GPU arrival, got %q %q", lastI, lastA)
	}
}

func TestPhaseTransitions(t *testing.T) {
	d := newTestDaemon(t)
	if d.Phase() != PhaseStarting {
		t.Fatalf("initial phase %s", d.Phase())
	}
	d.SetPhase(PhaseWaitForSystemReady)
	if d.Phase() != PhaseWaitForSystemReady {
		t.Fatal(d.Phase())
	}
	d.SetPhase(PhaseNormalRunning)
	if d.Phase() != PhaseNormalRunning {
		t.Fatal(d.Phase())
	}
	snap := d.Snapshot()
	if snap.Phase != PhaseNormalRunning {
		t.Fatalf("snapshot phase %s", snap.Phase)
	}
	if snap.ListenerRegistered {
		t.Fatal("listener not registered in unit test")
	}
}

func TestHandleIPCStatusAndCheck(t *testing.T) {
	d := newTestDaemon(t)
	d.SetPhase(PhaseNormalRunning)
	bad := d.HandleIPC(IPCRequest{Cmd: "nope"})
	if bad.OK {
		t.Fatal("invalid cmd should fail")
	}
	st := d.HandleIPC(IPCRequest{Cmd: "status"})
	if !st.OK || st.Status == nil || st.Status.Phase != PhaseNormalRunning {
		t.Fatalf("status: %+v", st)
	}
	ck := d.HandleIPC(IPCRequest{Cmd: "check"})
	if !ck.OK || ck.Status == nil {
		t.Fatalf("check: %+v", ck)
	}
}
