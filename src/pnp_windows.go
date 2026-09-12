//go:build windows

package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW             = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW              = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow                = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW               = modUser32.NewProc("DefWindowProcW")
	procGetMessageW                  = modUser32.NewProc("GetMessageW")
	procTranslateMessage             = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW             = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage              = modUser32.NewProc("PostQuitMessage")
	procPostMessageW                 = modUser32.NewProc("PostMessageW")
	procRegisterDeviceNotificationW  = modUser32.NewProc("RegisterDeviceNotificationW")
	procUnregisterDeviceNotification = modUser32.NewProc("UnregisterDeviceNotification")

	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")

	procCMRegisterNotification   = modCfgmgr32.NewProc("CM_Register_Notification")
	procCMUnregisterNotification = modCfgmgr32.NewProc("CM_Unregister_Notification")
)

const (
	WM_DEVICECHANGE = 0x0219
	WM_DESTROY      = 0x0002
	WM_CLOSE        = 0x0010
	WM_QUIT         = 0x0012
	WM_USER_STOP    = 0x0400 + 77

	DBT_DEVNODES_CHANGED                = 0x0007
	DBT_DEVICEARRIVAL                   = 0x8000
	DBT_DEVICEREMOVECOMPLETE            = 0x8004
	DBT_DEVTYP_DEVICEINTERFACE          = 0x00000005
	DEVICE_NOTIFY_WINDOW_HANDLE         = 0x00000000
	DEVICE_NOTIFY_ALL_INTERFACE_CLASSES = 0x00000004

	CM_NOTIFY_FILTER_TYPE_DEVICEINTERFACE     = 0
	CM_NOTIFY_FILTER_TYPE_DEVICEHANDLE        = 1
	CM_NOTIFY_FILTER_TYPE_DEVICEINSTANCE      = 2
	CM_NOTIFY_ACTION_DEVICEINTERFACEARRIVAL   = 0
	CM_NOTIFY_ACTION_DEVICEINTERFACEREMOVAL   = 1
	CM_NOTIFY_ACTION_DEVICEQUERYREMOVE        = 2
	CM_NOTIFY_ACTION_DEVICEQUERYREMOVEFAILED  = 3
	CM_NOTIFY_ACTION_DEVICEREMOVEPENDING      = 4
	CM_NOTIFY_ACTION_DEVICEREMOVECOMPLETE     = 5
	CM_NOTIFY_ACTION_DEVICEINSTANCEENUMERATED = 6
	CM_NOTIFY_ACTION_DEVICEINSTANCESTARTED    = 7
	CM_NOTIFY_ACTION_DEVICEINSTANCEREMOVED    = 8

	WS_OVERLAPPED = 0x00000000
	CW_USEDEFAULT = 0x80000000
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type devBroadcastDeviceInterfaceW struct {
	dbccSize       uint32
	dbccDeviceType uint32
	dbccReserved   uint32
	dbccClassGuid  windows.GUID
	dbccName       [1]uint16
}

// cmNotifyFilter mirrors CM_NOTIFY_FILTER. The union is sized for DeviceInstance
// (MAX_DEVICE_ID_LEN = 200 WCHAR) which is the largest fixed member.
const maxDeviceIDLen = 200

type cmNotifyFilter struct {
	cbSize     uint32
	flags      uint32
	filterType uint32
	reserved   uint32
	// union:
	instanceID [maxDeviceIDLen]uint16
}

const CM_NOTIFY_FILTER_FLAG_ALL_INTERFACE_CLASSES = 0x00000001
const CM_NOTIFY_FILTER_FLAG_ALL_DEVICE_INSTANCES = 0x00000002

// windowsPnPListener uses CM_Register_Notification when available,
// otherwise RegisterDeviceNotification + hidden message window (WM_DEVICECHANGE).
// Disable/Enable is NEVER called inside the callback — events are queued to workers.
type windowsPnPListener struct {
	logger   *Logger
	onEvent  func(PnPEvent)
	mu       sync.Mutex
	hwnd     windows.Handle
	notify   windows.Handle // HDEVNOTIFY or HCMNOTIFICATION
	useCM    bool
	cmCtx    uintptr
	started  bool
	stopOnce sync.Once
	done     chan struct{}
	queue    chan PnPEvent
	wg       sync.WaitGroup
}

// NewPnPListener creates an event-driven PnP listener (no polling as primary).
func NewPnPListener(logger *Logger, onEvent func(PnPEvent)) (PnPListener, error) {
	l := &windowsPnPListener{
		logger:  logger,
		onEvent: onEvent,
		done:    make(chan struct{}),
		queue:   make(chan PnPEvent, 256),
	}
	return l, nil
}

func (l *windowsPnPListener) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return fmt.Errorf("PnP listener already started")
	}
	l.started = true
	l.mu.Unlock()

	// Worker that drains the queue — never disable/enable in callback path.
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			select {
			case <-l.done:
				return
			case ev, ok := <-l.queue:
				if !ok {
					return
				}
				if l.onEvent != nil {
					l.onEvent(ev)
				}
			}
		}
	}()

	// Prefer CM_Register_Notification (cfgmgr32).
	if err := l.tryStartCM(ctx); err == nil {
		l.logger.Infof("PnP: using CM_Register_Notification (event-driven, no polling)")
		<-ctx.Done()
		_ = l.Stop()
		return ctx.Err()
	} else {
		l.logger.Warnf("CM_Register_Notification unavailable (%v); falling back to RegisterDeviceNotification + hidden window", err)
	}

	errCh := make(chan error, 1)
	go l.messageLoop(errCh)

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-ctx.Done():
	}
	<-ctx.Done()
	_ = l.Stop()
	return ctx.Err()
}

func (l *windowsPnPListener) enqueue(ev PnPEvent) {
	select {
	case l.queue <- ev:
	default:
		l.logger.Warnf("PnP event queue full, dropping event kind=%s instance=%s", ev.Kind, ev.InstanceID)
	}
}

// tryStartCM registers via CM_Register_Notification for all device instances / interfaces.
func (l *windowsPnPListener) tryStartCM(ctx context.Context) error {
	_ = ctx
	if err := modCfgmgr32.Load(); err != nil {
		return err
	}
	if err := procCMRegisterNotification.Find(); err != nil {
		return err
	}

	filter := cmNotifyFilter{
		cbSize:     uint32(unsafe.Sizeof(cmNotifyFilter{})),
		flags:      CM_NOTIFY_FILTER_FLAG_ALL_DEVICE_INSTANCES,
		filterType: CM_NOTIFY_FILTER_TYPE_DEVICEINSTANCE,
	}

	cb := windows.NewCallback(l.cmNotifyCallback)
	var hcm uintptr
	cr, _, callErr := procCMRegisterNotification.Call(
		uintptr(unsafe.Pointer(&filter)),
		0, // context
		cb,
		uintptr(unsafe.Pointer(&hcm)),
	)
	if cr != CR_SUCCESS {
		return fmt.Errorf("CM_Register_Notification CR=0x%X (%v)", cr, callErr)
	}
	l.mu.Lock()
	l.useCM = true
	l.cmCtx = hcm
	l.notify = windows.Handle(hcm)
	l.mu.Unlock()
	return nil
}

// cmNotifyCallback is invoked by the system — only enqueue, never disable/enable.
func (l *windowsPnPListener) cmNotifyCallback(hNotify uintptr, context uintptr, action uint32, eventData uintptr, eventDataSize uint32) uintptr {
	_ = hNotify
	_ = context
	_ = eventDataSize
	kind := "change"
	var instanceID string
	switch action {
	case CM_NOTIFY_ACTION_DEVICEINSTANCESTARTED:
		kind = "arrival"
	case CM_NOTIFY_ACTION_DEVICEINSTANCEREMOVED:
		kind = "removal"
	case CM_NOTIFY_ACTION_DEVICEINSTANCEENUMERATED:
		kind = "enumerated"
	case CM_NOTIFY_ACTION_DEVICEINTERFACEARRIVAL:
		kind = "arrival"
	case CM_NOTIFY_ACTION_DEVICEINTERFACEREMOVAL:
		kind = "removal"
	default:
		kind = "change"
	}
	// CM_NOTIFY_EVENT_DATA for DEVICEINSTANCE starts with header then InstanceId WCHAR[].
	if eventData != 0 && (action == CM_NOTIFY_ACTION_DEVICEINSTANCESTARTED ||
		action == CM_NOTIFY_ACTION_DEVICEINSTANCEREMOVED ||
		action == CM_NOTIFY_ACTION_DEVICEINSTANCEENUMERATED) {
		// Layout: CM_NOTIFY_FILTER_TYPE (4) + reserved (4) + InstanceId[]
		const header = 8
		instanceID = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(eventData + header)))
	}
	l.logger.Infof("PnP CM event: action=%d kind=%s instance=%q event_devinst_unset", action, kind, instanceID)
	l.enqueue(PnPEvent{Kind: kind, InstanceID: instanceID})
	return CR_SUCCESS
}

func (l *windowsPnPListener) messageLoop(errCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := windows.UTF16PtrFromString("PnPDeviceRecoveryHiddenWnd")
	hInst, _, _ := procGetModuleHandleW.Call(0)

	wndProc := windows.NewCallback(l.wndProc)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProc,
		hInstance:     windows.Handle(hInst),
		lpszClassName: className,
	}
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		// Class may already exist from prior run in same process — continue.
		l.logger.Warnf("RegisterClassExW: %v (may already exist)", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		0,
		uintptr(WS_OVERLAPPED),
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT),
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		errCh <- fmt.Errorf("CreateWindowExW failed: %w", err)
		return
	}
	l.mu.Lock()
	l.hwnd = windows.Handle(hwnd)
	l.mu.Unlock()

	// Register for all device interface classes.
	filter := devBroadcastDeviceInterfaceW{
		dbccSize:       uint32(unsafe.Sizeof(devBroadcastDeviceInterfaceW{})),
		dbccDeviceType: DBT_DEVTYP_DEVICEINTERFACE,
	}
	hNotify, _, err := procRegisterDeviceNotificationW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&filter)),
		uintptr(DEVICE_NOTIFY_WINDOW_HANDLE|DEVICE_NOTIFY_ALL_INTERFACE_CLASSES),
	)
	if hNotify == 0 {
		l.logger.Warnf("RegisterDeviceNotificationW failed: %v; still listening for DBT_DEVNODES_CHANGED", err)
	} else {
		l.mu.Lock()
		l.notify = windows.Handle(hNotify)
		l.mu.Unlock()
	}

	l.logger.Infof("PnP: using RegisterDeviceNotification + hidden message window (WM_DEVICECHANGE, event-driven)")
	errCh <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		ret := int32(r)
		if ret == -1 {
			return
		}
		if ret == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (l *windowsPnPListener) wndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_DEVICECHANGE:
		kind := "change"
		switch wParam {
		case DBT_DEVICEARRIVAL:
			kind = "arrival"
		case DBT_DEVICEREMOVECOMPLETE:
			kind = "removal"
		case DBT_DEVNODES_CHANGED:
			kind = "nodes_changed"
		}
		l.logger.Infof("PnP WM_DEVICECHANGE: wParam=0x%X kind=%s event_devinst_unset", wParam, kind)
		l.enqueue(PnPEvent{Kind: kind})
		return 1
	case WM_USER_STOP:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case WM_DESTROY:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func (l *windowsPnPListener) Registered() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return (l.useCM && l.notify != 0) || l.hwnd != 0
}

func (l *windowsPnPListener) Stop() error {
	var stopErr error
	l.stopOnce.Do(func() {
		close(l.done)
		l.mu.Lock()
		notify := l.notify
		useCM := l.useCM
		hwnd := l.hwnd
		l.mu.Unlock()

		if useCM && notify != 0 {
			if procCMUnregisterNotification.Find() == nil {
				procCMUnregisterNotification.Call(uintptr(notify))
			}
		} else {
			if notify != 0 {
				procUnregisterDeviceNotification.Call(uintptr(notify))
			}
			if hwnd != 0 {
				procPostMessageW.Call(uintptr(hwnd), uintptr(WM_USER_STOP), 0, 0)
			}
		}
		l.wg.Wait()
		l.logger.Infof("PnP listener stopped")
	})
	return stopErr
}

var _ PnPListener = (*windowsPnPListener)(nil)
var _ = syscall.EINVAL
