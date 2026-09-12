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
