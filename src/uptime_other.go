//go:build !windows

package main

import "time"

// systemUptime on non-Windows returns a large uptime so Ready / uptime-based
// gates do not hang in Linux unit tests. RecoveryManager can inject uptimeFn.
func systemUptime() (time.Duration, bool) {
	return 365 * 24 * time.Hour, true
}
