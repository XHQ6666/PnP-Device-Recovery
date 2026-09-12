//go:build !windows

package main

import "fmt"

// EnumerateDevices is a Linux stub — real implementation is Windows-only.
func EnumerateDevices() ([]DeviceInfo, error) {
	return nil, fmt.Errorf("EnumerateDevices is only available on Windows")
}

// GetDeviceByInstanceID is a Linux stub.
func GetDeviceByInstanceID(instanceID string) (*DeviceInfo, error) {
	return nil, fmt.Errorf("GetDeviceByInstanceID is only available on Windows")
}

// FindDeviceByFriendlyName is a Linux stub.
func FindDeviceByFriendlyName(friendlyName string) (*DeviceInfo, error) {
	return nil, fmt.Errorf("FindDeviceByFriendlyName is only available on Windows")
}

// DisableDevice is a Linux stub.
func DisableDevice(info *DeviceInfo) error {
	return fmt.Errorf("DisableDevice is only available on Windows")
}

// EnableDevice is a Linux stub.
func EnableDevice(info *DeviceInfo) error {
	return fmt.Errorf("EnableDevice is only available on Windows")
}
