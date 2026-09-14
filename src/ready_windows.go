//go:build windows

package main

import (
	"context"
	"time"
)

// Internal constants — not part of config.json.
// Ready = machine uptime has reached readyMinUptime (system has been running).
const (
	readyPollInterval = 200 * time.Millisecond
	readyMinUptime    = 1 * time.Second
	readyMaxWait      = 1 * time.Minute // safety if uptime API fails
)

var procGetTickCount64 = modKernel32.NewProc("GetTickCount64")

// systemUptime returns approximate time since boot via GetTickCount64.
func systemUptime() (time.Duration, bool) {
	if err := procGetTickCount64.Find(); err != nil {
		return 0, false
	}
	r, _, _ := procGetTickCount64.Call()
	// GetTickCount64 returns milliseconds since boot (wraps after ~49 days as uint64 ms — fine).
	return time.Duration(r) * time.Millisecond, true
}

// WaitForSystemReady waits until machine uptime is at least readyMinUptime (1s).
// No LogonUI / input-desktop / explorer checks. After readyMaxWait it proceeds with a warning.
func WaitForSystemReady(ctx context.Context, logger *Logger) error {
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: waiting for system uptime >= %s", readyMinUptime)
	}
	deadline := time.Now().Add(readyMaxWait)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		up, ok := systemUptime()
		if ok {
			if logger != nil {
				logger.Debugf("ready poll: uptime=%s (need >= %s)", up.Round(time.Millisecond), readyMinUptime)
			}
			if up >= readyMinUptime {
				if logger != nil {
					logger.Infof("system ready: uptime=%s", up.Round(time.Millisecond))
				}
				return nil
			}
		} else if logger != nil {
			logger.Debugf("ready poll: GetTickCount64 unavailable")
		}
		if time.Now().After(deadline) {
			if logger != nil {
				logger.Warnf("WAIT_FOR_SYSTEM_READY timed out after %s; proceeding with warning", readyMaxWait)
			}
			return nil
		}
		if !sleepOrDone(ctx, readyPollInterval) {
			return ctx.Err()
		}
	}
}
