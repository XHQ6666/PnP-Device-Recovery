//go:build !windows

package main

import "context"

// WaitForSystemReady returns immediately on non-Windows (tests / stubs).
func WaitForSystemReady(ctx context.Context, logger *Logger) error {
	_ = ctx
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: skipped (non-Windows stub)")
	}
	return nil
}
