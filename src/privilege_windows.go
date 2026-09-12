//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modShell32              = windows.NewLazySystemDLL("shell32.dll")
	modAdvapi32             = windows.NewLazySystemDLL("advapi32.dll")
	procShellExecuteExW     = modShell32.NewProc("ShellExecuteExW")
	procOpenProcessToken    = modAdvapi32.NewProc("OpenProcessToken")
	procGetTokenInformation = modAdvapi32.NewProc("GetTokenInformation")
)

const (
	TOKEN_QUERY             = 0x0008
	TokenElevation          = 20
	SEE_MASK_NOCLOSEPROCESS = 0x00000040
	SW_NORMAL               = 1
)

type tokenElevation struct {
	TokenIsElevated uint32
}

type shellExecuteInfoW struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIconOrMon   windows.Handle
	hProcess     windows.Handle
}

// IsElevated returns whether the current process has an elevated token.
func IsElevated() (bool, error) {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer token.Close()

	var elev tokenElevation
	var retLen uint32
	err = windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elev)), uint32(unsafe.Sizeof(elev)), &retLen)
	if err != nil {
		return false, fmt.Errorf("GetTokenInformation: %w", err)
	}
	return elev.TokenIsElevated != 0, nil
}

// EnsureElevated checks elevation; if not elevated, relaunches self via ShellExecuteEx runas.
// On UAC deny, logs and returns ErrNotElevated for clean exit.
func EnsureElevated(logger *Logger) error {
	ok, err := IsElevated()
	if err != nil {
		return fmt.Errorf("elevation check failed: %w", err)
	}
	if ok {
		if logger != nil {
			logger.Infof("running with administrator privileges")
		}
		return nil
	}

	if logger != nil {
		logger.Warnf("not elevated; requesting UAC elevation via ShellExecuteEx runas")
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	exeUTF16, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}

	// Preserve args except the process name.
	args := strings.Join(os.Args[1:], " ")
	var params *uint16
	if args != "" {
		params, err = windows.UTF16PtrFromString(args)
		if err != nil {
			return err
		}
	}

	info := shellExecuteInfoW{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		fMask:        SEE_MASK_NOCLOSEPROCESS,
		lpVerb:       verb,
		lpFile:       exeUTF16,
		lpParameters: params,
		nShow:        SW_NORMAL,
	}

	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		// ERROR_CANCELLED = 1223 — user denied UAC
		if errno, ok := callErr.(windows.Errno); ok && errno == 1223 {
			if logger != nil {
				logger.Errorf("UAC elevation denied by user; exiting cleanly")
			}
			return ErrNotElevated
		}
		if logger != nil {
			logger.Errorf("ShellExecuteEx runas failed: %v", callErr)
		}
		return fmt.Errorf("ShellExecuteEx runas failed: %w", callErr)
	}

	// Elevated child started — exit this non-elevated instance cleanly.
	if logger != nil {
		logger.Infof("elevated process launched; exiting non-elevated instance")
	}
	os.Exit(0)
	return nil
}

// ErrNotElevated indicates UAC elevation was denied.
var ErrNotElevated = fmt.Errorf("administrator privileges required (UAC denied)")
