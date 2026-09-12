//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const singletonMutexName = `Global\PnPDeviceRecovery_SingleInstance`

// AcquireSingleton holds a named mutex so only one daemon runs.
func AcquireSingleton() (func(), error) {
	name, err := windows.UTF16PtrFromString(singletonMutexName)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if h != 0 && err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("CreateMutex: %w", err)
	}
	return func() {
		_ = windows.ReleaseMutex(h)
		_ = windows.CloseHandle(h)
	}, nil
}
