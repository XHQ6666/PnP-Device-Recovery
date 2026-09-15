//go:build windows

package main

import "time"

var procGetTickCount64 = modKernel32.NewProc("GetTickCount64")

// systemUptime returns approximate time since boot via GetTickCount64.
func systemUptime() (time.Duration, bool) {
	if err := procGetTickCount64.Find(); err != nil {
		return 0, false
	}
	r, _, _ := procGetTickCount64.Call()
	// GetTickCount64 returns milliseconds since boot.
	return time.Duration(r) * time.Millisecond, true
}
