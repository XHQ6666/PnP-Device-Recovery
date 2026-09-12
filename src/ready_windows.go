//go:build windows

package main

import (
	"context"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Internal constants — not part of config.json.
const (
	readyPollInterval  = 1 * time.Second
	readySafetyBuffer  = 12 * time.Second
	readyExplorerGrace = 30 * time.Second
	readyMaxWait       = 8 * time.Minute
)

const (
	wtsCurrentServerHandle = 0
	wtsConnectState        = 8
	wtsActive              = 0
	invalidSessionID       = 0xFFFFFFFF
)

var (
	modWtsapi32                      = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW  = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory                = modWtsapi32.NewProc("WTSFreeMemory")
	procWTSGetActiveConsoleSessionId = modKernel32.NewProc("WTSGetActiveConsoleSessionId")
)

func activeConsoleSessionID() uint32 {
	if err := procWTSGetActiveConsoleSessionId.Find(); err != nil {
		return invalidSessionID
	}
	r, _, _ := procWTSGetActiveConsoleSessionId.Call()
	return uint32(r)
}

func querySessionState(sessionID uint32) (uint32, bool) {
	if sessionID == invalidSessionID {
		return 0, false
	}
	if err := procWTSQuerySessionInformationW.Find(); err != nil {
		return 0, false
	}
	var buf uintptr
	var nbytes uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(
		uintptr(wtsCurrentServerHandle),
		uintptr(sessionID),
		uintptr(wtsConnectState),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&nbytes)),
	)
	if r == 0 || buf == 0 {
		return 0, false
	}
	state := *(*uint32)(unsafe.Pointer(buf))
	procWTSFreeMemory.Call(buf)
	return state, true
}

func explorerPresent() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return false
	}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "explorer.exe") {
			return true
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return false
}

// WaitForSystemReady waits for a WTSActive interactive session, treats
// explorer.exe as a soft signal, then applies a short hardcoded safety buffer.
// After readyMaxWait it proceeds with a warning instead of hanging forever.
func WaitForSystemReady(ctx context.Context, logger *Logger) error {
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: waiting for interactive WTSActive session")
	}
	deadline := time.Now().Add(readyMaxWait)
	var sessionReadyAt time.Time
	sessionReady := false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sid := activeConsoleSessionID()
		state, ok := querySessionState(sid)
		if sid != invalidSessionID && ok && state == wtsActive {
			if !sessionReady {
				sessionReady = true
				sessionReadyAt = time.Now()
				if logger != nil {
					logger.Infof("interactive session active: session_id=%d state=WTSActive", sid)
				}
			}
			if explorerPresent() {
				if logger != nil {
					logger.Infof("soft signal: explorer.exe present")
				}
				break
			}
			if time.Since(sessionReadyAt) >= readyExplorerGrace {
				if logger != nil {
					logger.Warnf("explorer.exe not found after %s; proceeding on WTSActive only", readyExplorerGrace)
				}
				break
			}
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

	if logger != nil {
		logger.Infof("applying post-session safety buffer %s", readySafetyBuffer)
	}
	if !sleepOrDone(ctx, readySafetyBuffer) {
		return ctx.Err()
	}
	return nil
}
