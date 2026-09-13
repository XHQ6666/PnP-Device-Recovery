package main

import "testing"

func TestIsHealthyTable(t *testing.T) {
	cases := []struct {
		status  uint32
		problem uint32
		want    bool
	}{
		{0, 0, true},
		{0x00000000, 0, true},
		{DN_HAS_PROBLEM, 0, false},
		{0, 22, false},
		{DN_HAS_PROBLEM, 43, false},
		{0x00000001, 0, true}, // other flags OK if no DN_HAS_PROBLEM and problem==0
	}
	for _, c := range cases {
		d := &DeviceInfo{Status: c.status, ProblemCode: c.problem}
		if got := d.IsHealthy(); got != c.want {
			t.Fatalf("status=0x%X problem=%d: got %v want %v", c.status, c.problem, got, c.want)
		}
	}
	if (&DeviceInfo{}).IsHealthy() != true {
		// zero value is healthy (no problem)
	}
	var nilDev *DeviceInfo
	if nilDev.IsHealthy() {
		t.Fatal("nil should not be healthy")
	}
}


func TestIsDisabledEnabled(t *testing.T) {
	var nilDev *DeviceInfo
	if nilDev.IsDisabled() || nilDev.IsEnabled() {
		t.Fatal("nil")
	}
	d := &DeviceInfo{Status: DN_HAS_PROBLEM, ProblemCode: CM_PROB_DISABLED}
	if !d.IsDisabled() || d.IsEnabled() {
		t.Fatal("disabled")
	}
	e := &DeviceInfo{Status: DN_STARTED, ProblemCode: 43}
	if e.IsDisabled() || !e.IsEnabled() {
		t.Fatal("enabled despite problem")
	}
}
