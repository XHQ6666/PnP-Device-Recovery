//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modSetupapi = windows.NewLazySystemDLL("setupapi.dll")
	modCfgmgr32 = windows.NewLazySystemDLL("cfgmgr32.dll")

	procSetupDiGetClassDevsW             = modSetupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo            = modSetupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiDestroyDeviceInfoList     = modSetupapi.NewProc("SetupDiDestroyDeviceInfoList")
	procSetupDiGetDeviceRegistryProperty = modSetupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")
	procSetupDiGetDeviceInstanceIdW      = modSetupapi.NewProc("SetupDiGetDeviceInstanceIdW")
	procSetupDiCallClassInstaller        = modSetupapi.NewProc("SetupDiCallClassInstaller")
	procSetupDiSetClassInstallParamsW    = modSetupapi.NewProc("SetupDiSetClassInstallParamsW")

	procCMGetDevNodeStatus = modCfgmgr32.NewProc("CM_Get_DevNode_Status")
	procCMLocateDevNodeW   = modCfgmgr32.NewProc("CM_Locate_DevNodeW")
	procCMDisableDevNode   = modCfgmgr32.NewProc("CM_Disable_DevNode")
	procCMEnableDevNode    = modCfgmgr32.NewProc("CM_Enable_DevNode")
	procCMGetDeviceIDW     = modCfgmgr32.NewProc("CM_Get_Device_IDW")
	procCMGetDeviceIDSize  = modCfgmgr32.NewProc("CM_Get_Device_ID_Size")
)

const (
	DIGCF_PRESENT            = 0x00000002
	DIGCF_ALLCLASSES         = 0x00000004
	SPDRP_FRIENDLYNAME       = 0x0000000C
	SPDRP_DEVICEDESC         = 0x00000000
	DIF_PROPERTYCHANGE       = 0x00000012
	DICS_ENABLE              = 0x00000001
	DICS_DISABLE             = 0x00000002
	DICS_FLAG_GLOBAL         = 0x00000001
	INVALID_HANDLE_VALUE     = ^windows.Handle(0)
	CR_SUCCESS               = 0
	CM_DISABLE_UI_NOT_OK     = 0x00000002
	CM_LOCATE_DEVNODE_NORMAL = 0x00000000
)

type spDevinfoData struct {
	cbSize    uint32
	ClassGuid windows.GUID
	DevInst   uint32
	Reserved  uintptr
}

type spPropchangeParams struct {
	ClassInstallHeader spClassInstallHeader
	StateChange        uint32
	Scope              uint32
	HwProfile          uint32
}

type spClassInstallHeader struct {
	cbSize          uint32
	InstallFunction uint32
}

// EnumerateDevices lists present devices via SetupAPI and fills Status/Problem via CfgMgr32.
func EnumerateDevices() ([]DeviceInfo, error) {
	if err := modSetupapi.Load(); err != nil {
		return nil, fmt.Errorf("load setupapi.dll: %w", err)
	}
	if err := modCfgmgr32.Load(); err != nil {
		return nil, fmt.Errorf("load cfgmgr32.dll: %w", err)
	}

	h, _, err := procSetupDiGetClassDevsW.Call(
		0,
		0,
		0,
		uintptr(DIGCF_PRESENT|DIGCF_ALLCLASSES),
	)
	if windows.Handle(h) == INVALID_HANDLE_VALUE {
		return nil, fmt.Errorf("SetupDiGetClassDevsW failed: %w", err)
	}
	devInfo := windows.Handle(h)
	defer procSetupDiDestroyDeviceInfoList.Call(uintptr(devInfo))

	var results []DeviceInfo
	for i := uint32(0); ; i++ {
		var data spDevinfoData
		data.cbSize = uint32(unsafe.Sizeof(data))
		r, _, _ := procSetupDiEnumDeviceInfo.Call(uintptr(devInfo), uintptr(i), uintptr(unsafe.Pointer(&data)))
		if r == 0 {
			break
		}

		info := DeviceInfo{DevInst: data.DevInst}

		if name, err := getDeviceRegistryString(devInfo, &data, SPDRP_FRIENDLYNAME); err == nil && name != "" {
			info.FriendlyName = name
		} else if desc, err := getDeviceRegistryString(devInfo, &data, SPDRP_DEVICEDESC); err == nil {
			info.FriendlyName = desc
		}

		if id, err := getDeviceInstanceID(devInfo, &data); err == nil {
			info.InstanceID = id
		}

		var status, problem uint32
		cr, _, _ := procCMGetDevNodeStatus.Call(
			uintptr(unsafe.Pointer(&status)),
			uintptr(unsafe.Pointer(&problem)),
			uintptr(data.DevInst),
			0,
		)
		if cr == CR_SUCCESS {
			info.Status = status
			info.ProblemCode = problem
		}

		if info.FriendlyName != "" || info.InstanceID != "" {
			results = append(results, info)
		}
	}
	return results, nil
}

func getDeviceRegistryString(devInfo windows.Handle, data *spDevinfoData, prop uint32) (string, error) {
	var reqSize uint32
	var dataType uint32
	procSetupDiGetDeviceRegistryProperty.Call(
		uintptr(devInfo),
		uintptr(unsafe.Pointer(data)),
		uintptr(prop),
		uintptr(unsafe.Pointer(&dataType)),
		0,
		0,
		uintptr(unsafe.Pointer(&reqSize)),
	)
	if reqSize == 0 {
		return "", fmt.Errorf("property size 0")
	}
	buf := make([]uint16, reqSize/2+1)
	r, _, err := procSetupDiGetDeviceRegistryProperty.Call(
		uintptr(devInfo),
		uintptr(unsafe.Pointer(data)),
		uintptr(prop),
		uintptr(unsafe.Pointer(&dataType)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(reqSize),
		uintptr(unsafe.Pointer(&reqSize)),
	)
	if r == 0 {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

func getDeviceInstanceID(devInfo windows.Handle, data *spDevinfoData) (string, error) {
	var reqSize uint32
	procSetupDiGetDeviceInstanceIdW.Call(
		uintptr(devInfo),
		uintptr(unsafe.Pointer(data)),
		0,
		0,
		uintptr(unsafe.Pointer(&reqSize)),
	)
	if reqSize == 0 {
		return "", fmt.Errorf("instance id size 0")
	}
	buf := make([]uint16, reqSize)
	r, _, err := procSetupDiGetDeviceInstanceIdW.Call(
		uintptr(devInfo),
		uintptr(unsafe.Pointer(data)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(reqSize),
		uintptr(unsafe.Pointer(&reqSize)),
	)
	if r == 0 {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

// GetDeviceByInstanceID locates a device node and returns its status fields.
func GetDeviceByInstanceID(instanceID string) (*DeviceInfo, error) {
	if err := modCfgmgr32.Load(); err != nil {
		return nil, err
	}
	idPtr, err := windows.UTF16PtrFromString(instanceID)
	if err != nil {
		return nil, err
	}
	var devInst uint32
	cr, _, _ := procCMLocateDevNodeW.Call(
		uintptr(unsafe.Pointer(&devInst)),
		uintptr(unsafe.Pointer(idPtr)),
		uintptr(CM_LOCATE_DEVNODE_NORMAL),
	)
	if cr != CR_SUCCESS {
		return nil, fmt.Errorf("%s instance=%s", formatCR("CM_Locate_DevNodeW", cr, nil), instanceID)
	}

	info := &DeviceInfo{InstanceID: instanceID, DevInst: devInst}
	var status, problem uint32
	cr, _, _ = procCMGetDevNodeStatus.Call(
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&problem)),
		uintptr(devInst),
		0,
	)
	if cr == CR_SUCCESS {
		info.Status = status
		info.ProblemCode = problem
	}

	// Friendly name via full enumerate match (instance ID is authoritative).
	if all, err := EnumerateDevices(); err == nil {
		for i := range all {
			if EqualFoldInstanceID(all[i].InstanceID, instanceID) {
				info.FriendlyName = all[i].FriendlyName
				info.Status = all[i].Status
				info.ProblemCode = all[i].ProblemCode
				info.DevInst = all[i].DevInst
				break
			}
		}
	}
	return info, nil
}

// FindDeviceByFriendlyName returns the first present device with exact friendly name.
func FindDeviceByFriendlyName(friendlyName string) (*DeviceInfo, error) {
	all, err := EnumerateDevices()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].FriendlyName == friendlyName {
			d := all[i]
			return &d, nil
		}
	}
	return nil, fmt.Errorf("device not found: %s", friendlyName)
}

// DisableDevice disables a device via CM_Disable_DevNode, falling back to SetupAPI DIF_PROPERTYCHANGE.
func DisableDevice(info *DeviceInfo) error {
	if info == nil {
		return fmt.Errorf("nil device")
	}
	if err := modCfgmgr32.Load(); err != nil {
		return err
	}
	cr, _, callErr := procCMDisableDevNode.Call(uintptr(info.DevInst), uintptr(CM_DISABLE_UI_NOT_OK))
	if cr == CR_SUCCESS {
		return nil
	}
	cmErr := formatCR("CM_Disable_DevNode", cr, callErr)
	// Fallback SetupAPI
	if err := propertyChange(info, DICS_DISABLE); err != nil {
		return fmt.Errorf("%s and SetupAPI fallback failed: %w", cmErr, err)
	}
	return nil
}

// EnableDevice enables a device via CM_Enable_DevNode, falling back to SetupAPI DIF_PROPERTYCHANGE.
func EnableDevice(info *DeviceInfo) error {
	if info == nil {
		return fmt.Errorf("nil device")
	}
	if err := modCfgmgr32.Load(); err != nil {
		return err
	}
	cr, _, callErr := procCMEnableDevNode.Call(uintptr(info.DevInst), 0)
	if cr == CR_SUCCESS {
		return nil
	}
	cmErr := formatCR("CM_Enable_DevNode", cr, callErr)
	if err := propertyChange(info, DICS_ENABLE); err != nil {
		return fmt.Errorf("%s and SetupAPI fallback failed: %w", cmErr, err)
	}
	return nil
}

func propertyChange(info *DeviceInfo, stateChange uint32) error {
	if err := modSetupapi.Load(); err != nil {
		return err
	}
	h, _, err := procSetupDiGetClassDevsW.Call(0, 0, 0, uintptr(DIGCF_PRESENT|DIGCF_ALLCLASSES))
	if windows.Handle(h) == INVALID_HANDLE_VALUE {
		return fmt.Errorf("SetupDiGetClassDevsW: %w", err)
	}
	devInfo := windows.Handle(h)
	defer procSetupDiDestroyDeviceInfoList.Call(uintptr(devInfo))

	var data spDevinfoData
	found := false
	for i := uint32(0); ; i++ {
		data.cbSize = uint32(unsafe.Sizeof(data))
		r, _, _ := procSetupDiEnumDeviceInfo.Call(uintptr(devInfo), uintptr(i), uintptr(unsafe.Pointer(&data)))
		if r == 0 {
			break
		}
		id, err := getDeviceInstanceID(devInfo, &data)
		if err != nil {
			continue
		}
		if EqualFoldInstanceID(id, info.InstanceID) || (info.InstanceID == "" && data.DevInst == info.DevInst) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("device not found in SetupAPI list for property change")
	}

	params := spPropchangeParams{
		ClassInstallHeader: spClassInstallHeader{
			cbSize:          uint32(unsafe.Sizeof(spClassInstallHeader{})),
			InstallFunction: DIF_PROPERTYCHANGE,
		},
		StateChange: stateChange,
		Scope:       DICS_FLAG_GLOBAL,
		HwProfile:   0,
	}
	r, _, err := procSetupDiSetClassInstallParamsW.Call(
		uintptr(devInfo),
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Sizeof(params)),
	)
	if r == 0 {
		return fmt.Errorf("SetupDiSetClassInstallParamsW: %w", err)
	}
	r, _, err = procSetupDiCallClassInstaller.Call(
		uintptr(DIF_PROPERTYCHANGE),
		uintptr(devInfo),
		uintptr(unsafe.Pointer(&data)),
	)
	if r == 0 {
		return fmt.Errorf("SetupDiCallClassInstaller: %w", err)
	}
	return nil
}

func formatCR(api string, cr uintptr, win32 error) string {
	s := fmt.Sprintf("api=%s result=%s (0x%X)", api, crName(uint64(cr)), cr)
	if win32 != nil && !isErrnoZero(win32) {
		s += fmt.Sprintf(" win32=%v", win32)
	}
	return s
}

func isErrnoZero(err error) bool {
	if err == nil {
		return true
	}
	if errno, ok := err.(syscall.Errno); ok {
		return errno == 0
	}
	if errno, ok := err.(windows.Errno); ok {
		return errno == 0
	}
	return false
}

func crName(cr uint64) string {
	switch cr {
	case 0x00:
		return "CR_SUCCESS"
	case 0x01:
		return "CR_DEFAULT"
	case 0x02:
		return "CR_OUT_OF_MEMORY"
	case 0x03:
		return "CR_INVALID_POINTER"
	case 0x04:
		return "CR_INVALID_FLAG"
	case 0x05:
		return "CR_INVALID_DEVNODE"
	case 0x0D:
		return "CR_NO_SUCH_DEVNODE"
	case 0x13:
		return "CR_FAILURE"
	case 0x17:
		return "CR_REMOVE_VETOED"
	case 0x1A:
		return "CR_BUFFER_SMALL"
	case 0x1E:
		return "CR_INVALID_DEVICE_ID"
	case 0x1F:
		return "CR_INVALID_DATA"
	case 0x20:
		return "CR_INVALID_API"
	case 0x21:
		return "CR_DEVLOADER_NOT_READY"
	case 0x22:
		return "CR_NEED_RESTART"
	case 0x24:
		return "CR_DEVICE_NOT_THERE"
	case 0x28:
		return "CR_NOT_DISABLEABLE"
	case 0x2A:
		return "CR_QUERY_VETOED"
	case 0x33:
		return "CR_ACCESS_DENIED"
	case 0x34:
		return "CR_CALL_NOT_IMPLEMENTED"
	case 0x3B:
		return "CR_INVALID_STRUCTURE_SIZE"
	default:
		return fmt.Sprintf("CR_0x%X", cr)
	}
}

// silence unused syscall import if build path changes
var _ = syscall.EINVAL
