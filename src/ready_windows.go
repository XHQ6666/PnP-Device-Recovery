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
// Ready = console session up + lock/logon UI (LogonUI), then a short fixed buffer.
const (
	readyPollInterval = 500 * time.Millisecond
	readyPostReadyMin = 3 * time.Second  // minimum wait after lock-screen ready before Initial Scan
	readyLogonUIGrace = 5 * time.Second  // if session is up but LogonUI never appears (e.g. auto-logon)
	readyMaxWait      = 1 * time.Minute
)

// WTS_CONNECTSTATE_CLASS
const (
	wtsCurrentServerHandle = 0
	wtsConnectState        = 8 // WTSConnectState
	wtsActive              = 0 // WTSActive
	wtsConnected           = 1 // WTSConnected
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

func sessionStateReady(state uint32) bool {
	return state == wtsActive || state == wtsConnected
}

func processPresent(exeName string) bool {
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
		if strings.EqualFold(name, exeName) {
			return true
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return false
}

func logonUIPresent() bool {
	return processPresent("LogonUI.exe")
}

// WaitForSystemReady waits until the console session is Connected/Active and the
// lock/logon UI is likely up (LogonUI.exe), then applies a minimum post-ready
// buffer (3s) before Initial Scan. It does not wait for explorer/desktop.
// After readyMaxWait it proceeds with a warning instead of hanging forever.
func WaitForSystemReady(ctx context.Context, logger *Logger) error {
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: waiting for console session Connected/Active and LogonUI (lock screen)")
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
		if sid != invalidSessionID && ok && sessionStateReady(state) {
			if !sessionReady {
				sessionReady = true
				sessionReadyAt = time.Now()
				if logger != nil {
					logger.Infof("console session ready: session_id=%d state=%d (0=Active 1=Connected)", sid, state)
				}
			}
			if logonUIPresent() {
				if logger != nil {
					logger.Infof("soft signal: LogonUI.exe present (lock/logon UI)")
				}
				break
			}
			if time.Since(sessionReadyAt) >= readyLogonUIGrace {
				if logger != nil {
					logger.Warnf("LogonUI.exe not found after %s; proceeding on session state only (e.g. auto-logon)", readyLogonUIGrace)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			if logger != nil {
				logger.Warnf("WAIT_FOR_SYSTEM_READY timed out after %s; proceeding with warning", readyMaxWait)
			}
			// Still apply the minimum post-ready buffer when possible.
			if !sleepOrDone(ctx, readyPostReadyMin) {
				return ctx.Err()
			}
			return nil
		}
		if !sleepOrDone(ctx, readyPollInterval) {
			return ctx.Err()
		}
	}

	if logger != nil {
		logger.Infof("lock-screen ready: applying minimum post-ready buffer %s before Initial Scan", readyPostReadyMin)
	}
	if !sleepOrDone(ctx, readyPostReadyMin) {
		return ctx.Err()
	}
	return nil
}
