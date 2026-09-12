//go:build !windows

package main

import "fmt"

// EnsureElevated is a no-op stub on non-Windows (always "elevated").
func EnsureElevated(logger *Logger) error {
	if logger != nil {
		logger.Infof("privilege check skipped (non-Windows stub)")
	}
	return nil
}

// IsElevated always returns true on non-Windows stubs.
func IsElevated() (bool, error) {
	return true, nil
}

// ErrNotElevated indicates UAC elevation was denied.
var ErrNotElevated = fmt.Errorf("administrator privileges required")
