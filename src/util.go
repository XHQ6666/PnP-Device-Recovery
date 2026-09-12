package main

import "strings"

// NormalizeInstanceID returns a canonical map key for a PnP instance ID.
// Keys are case-insensitive (Windows instance IDs are compared with EqualFold / ToUpper).
func NormalizeInstanceID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

// EqualFoldInstanceID reports whether two instance IDs are the same ignoring case.
func EqualFoldInstanceID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
