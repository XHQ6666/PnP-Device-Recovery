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
// Ready = input desktop Winlogon (lock) with console session up, OR Default (desktop/auto-logon).
const (
	readyPollInterval = 500 * time.Millisecond
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

// Desktop / user-object constants
const (
	desktopReadObjects = 0x0001
	uoiName            = 2 // UOI_NAME
)

var (
	modWtsapi32                      = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW  = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory                = modWtsapi32.NewProc("WTSFreeMemory")
	procWTSGetActiveConsoleSessionId = modKernel32.NewProc("WTSGetActiveConsoleSessionId")
	// modUser32 is declared in pnp_windows.go (same package).
	procOpenInputDesktop             = modUser32.NewProc("OpenInputDesktop")
	procCloseDesktop                 = modUser32.NewProc("CloseDesktop")
	procGetUserObjectInformationW    = modUser32.NewProc("GetUserObjectInformationW")
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

// inputDesktopName returns the UOI_NAME of the current input desktop, or ("", false).
func inputDesktopName() (string, bool) {
	if err := procOpenInputDesktop.Find(); err != nil {
		return "", false
	}
	if err := procGetUserObjectInformationW.Find(); err != nil {
		return "", false
	}
	h, _, _ := procOpenInputDesktop.Call(
		0,
		0, // fInherit = FALSE
		uintptr(desktopReadObjects),
	)
	if h == 0 {
		return "", false
	}
	defer procCloseDesktop.Call(h)

	var needed uint32
	procGetUserObjectInformationW.Call(
		h,
		uintptr(uoiName),
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)
	if needed == 0 {
		needed = 256
	}
	buf := make([]uint16, needed/2+2)
	var got uint32
	r, _, _ := procGetUserObjectInformationW.Call(
		h,
		uintptr(uoiName),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&got)),
	)
	if r == 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// systemReady reports whether the input desktop indicates an interactive lock or desktop.
// Ready when EITHER:
//  1. desktop name is "Winlogon" AND console session Connected/Active, OR
//  2. desktop name is "Default"
func systemReady(logger *Logger) bool {
	name, ok := inputDesktopName()
	if !ok {
		if logger != nil {
			logger.Debugf("ready poll: OpenInputDesktop/GetUserObjectInformationW failed")
		}
		return false
	}
	if logger != nil {
		logger.Debugf("ready poll: input desktop=%q", name)
	}
	switch {
	case strings.EqualFold(name, "Default"):
		return true
	case strings.EqualFold(name, "Winlogon"):
		sid := activeConsoleSessionID()
		state, sok := querySessionState(sid)
		if logger != nil {
			logger.Debugf("ready poll: Winlogon desktop; session_id=%d state_ok=%v state=%d", sid, sok, state)
		}
		return sid != invalidSessionID && sok && sessionStateReady(state)
	default:
		return false
	}
}

// WaitForSystemReady waits until the input desktop is Winlogon (with console session
// Connected/Active) or Default. It does not wait for explorer/desktop beyond Default,
// and no longer uses LogonUI.exe or uptime. After readyMaxWait it proceeds with a
// warning instead of hanging forever.
func WaitForSystemReady(ctx context.Context, logger *Logger) error {
	if logger != nil {
		logger.Infof("WAIT_FOR_SYSTEM_READY: waiting for input desktop Winlogon (session Connected/Active) or Default")
	}
	deadline := time.Now().Add(readyMaxWait)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if systemReady(logger) {
			name, _ := inputDesktopName()
			if logger != nil {
				logger.Infof("system ready: input desktop=%q", name)
			}
			return nil
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
}
