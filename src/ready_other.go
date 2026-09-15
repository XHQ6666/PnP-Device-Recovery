//go:build !windows

package main

import (
	"context"
	"time"
)

// WaitForSystemReady returns immediately on non-Windows (tests / stubs).
func WaitForSystemReady(ctx context.Context, logger *Logger, minUptime, poll, maxWait time.Duration) error {
	_ = ctx
	_ = minUptime
	_ = poll
	_ = maxWait
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: skipped (non-Windows stub)")
	}
	return nil
}
