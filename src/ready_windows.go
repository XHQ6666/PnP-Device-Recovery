//go:build windows

package main

import (
	"context"
	"time"
)

// Default Ready constants — used when callers pass zero durations.
const (
	readyPollInterval = 200 * time.Millisecond
	readyMinUptime    = 1 * time.Second
	readyMaxWait      = 1 * time.Minute
)

// WaitForSystemReady waits until machine uptime is at least minUptime.
// Zero durations fall back to package default constants.
// After maxWait it proceeds with a warning.
func WaitForSystemReady(ctx context.Context, logger *Logger, minUptime, poll, maxWait time.Duration) error {
	if minUptime <= 0 {
		minUptime = readyMinUptime
	}
	if poll <= 0 {
		poll = readyPollInterval
	}
	if maxWait <= 0 {
		maxWait = readyMaxWait
	}
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: waiting for system uptime >= %s", minUptime)
	}
	deadline := time.Now().Add(maxWait)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		up, ok := systemUptime()
		if ok {
			if logger != nil {
				logger.Debugf("ready poll: uptime=%s (need >= %s)", up.Round(time.Millisecond), minUptime)
			}
			if up >= minUptime {
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
				logger.Warnf("WAIT_FOR_SYSTEM_READY timed out after %s; proceeding with warning", maxWait)
			}
			return nil
		}
		if !sleepOrDone(ctx, poll) {
			return ctx.Err()
		}
	}
}
