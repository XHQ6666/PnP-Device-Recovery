//go:build !windows

package main

import (
	"os"
	"syscall"
)

const linuxLockPath = "/tmp/PnPDeviceRecovery.lock"

// AcquireSingleton takes a non-blocking flock (enough for Linux tests).
func AcquireSingleton() (func(), error) {
	f, err := os.OpenFile(linuxLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrAlreadyRunning
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
