package main

// DeviceInfo holds device identity and health fields from SetupAPI/CfgMgr32.
type DeviceInfo struct {
	FriendlyName string
	InstanceID   string
	DevInst      uint32
	Status       uint32
	ProblemCode  uint32
}

// CfgMgr32 device-node flags / problem codes used for health and enable state.
const (
	DN_HAS_PROBLEM   = 0x00000400
	DN_STARTED       = 0x00000008
	CM_PROB_DISABLED = 22 // administratively disabled in Device Manager
)

// IsHealthy returns true when the device has no problem flag and ProblemCode==0.
func (d *DeviceInfo) IsHealthy() bool {
	if d == nil {
		return false
	}
	return (d.Status&DN_HAS_PROBLEM) == 0 && d.ProblemCode == 0
}

// IsDisabled reports whether the device is administratively disabled.
// Primary signal: ProblemCode == CM_PROB_DISABLED (22). DN_STARTED clear is a
// secondary hint used only together with that problem code.
func (d *DeviceInfo) IsDisabled() bool {
	if d == nil {
		return false
	}
	if d.ProblemCode == CM_PROB_DISABLED {
		return true
	}
	return false
}

// IsEnabled reports whether the device is not administratively disabled.
// A device may be "enabled" yet unhealthy (e.g. Code 43).
func (d *DeviceInfo) IsEnabled() bool {
	if d == nil {
		return false
	}
	return !d.IsDisabled()
}
