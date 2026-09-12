package main

// DeviceInfo holds device identity and health fields from SetupAPI/CfgMgr32.
type DeviceInfo struct {
	FriendlyName string
	InstanceID   string
	DevInst      uint32
	Status       uint32
	ProblemCode  uint32
}

// DN_HAS_PROBLEM is the CfgMgr32 device-node flag indicating a problem.
const DN_HAS_PROBLEM = 0x00000400

// IsHealthy returns true when the device has no problem flag and ProblemCode==0.
func (d *DeviceInfo) IsHealthy() bool {
	if d == nil {
		return false
	}
	return (d.Status&DN_HAS_PROBLEM) == 0 && d.ProblemCode == 0
}
