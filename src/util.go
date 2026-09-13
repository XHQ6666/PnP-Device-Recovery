package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeInstanceID returns a canonical map key for a PnP instance ID.
// Keys are case-insensitive (Windows instance IDs are compared with EqualFold / ToUpper).
func NormalizeInstanceID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

// EqualFoldInstanceID reports whether two instance IDs are the same ignoring case.
func EqualFoldInstanceID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ExecutableDir returns the directory containing the current executable.
// Relative config/log paths resolve against this directory (not the process cwd).
func ExecutableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// ResolvePathRelativeToExe joins a relative path with the executable directory.
// Empty path uses defaultLogFilename. Absolute paths are returned unchanged.
func ResolvePathRelativeToExe(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultLogFilename
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	dir, err := ExecutableDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, path), nil
}
