package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Per-device recovery states.
const (
	StateIdle       = "Idle"
	StateRecovering = "Recovering"
	StateExhausted  = "Exhausted"
)

// deviceOps lets tests stub SetupAPI/CfgMgr32.
type deviceOps struct {
	Enumerate          func() ([]DeviceInfo, error)
	GetByInstanceID    func(string) (*DeviceInfo, error)
	FindByFriendlyName func(string) (*DeviceInfo, error)
	Disable            func(*DeviceInfo) error
	Enable             func(*DeviceInfo) error
}

func realDeviceOps() deviceOps {
	return deviceOps{
		Enumerate:          EnumerateDevices,
		GetByInstanceID:    GetDeviceByInstanceID,
		FindByFriendlyName: FindDeviceByFriendlyName,
		Disable:            DisableDevice,
		Enable:             EnableDevice,
	}
}

// RecoveryManager coordinates per-device recovery sessions.
// Same device never runs concurrent recoveries (events coalesce).
// Exhausted devices do not auto-retry on normal PnP events — only check clears.
type RecoveryManager struct {
	cfg          *Config
	matchers     []*Matcher
	logger       *Logger
	delay        time.Duration // retry_delay between disable/enable
	ops          deviceOps
	processStart time.Time

	mu       sync.Mutex
	sessions map[string]*deviceSession // key: NormalizeInstanceID
	wg       sync.WaitGroup
	stopping bool
}

type deviceSession struct {
	mu         sync.Mutex
	state      string
	running    bool
	attempts   int
	cfgIdx     int
	maxRetries int
	name       string
	instanceID string
}

// NewRecoveryManager creates a manager for the given config and matchers.
// processStart is set to now and used for per-device delay gating.
func NewRecoveryManager(cfg *Config, matchers []*Matcher, logger *Logger) *RecoveryManager {
	return &RecoveryManager{
		cfg:          cfg,
		matchers:     matchers,
		logger:       logger,
		delay:        time.Duration(cfg.RetryDelay) * time.Second,
		ops:          realDeviceOps(),
		processStart: time.Now(),
		sessions:     make(map[string]*deviceSession),
	}
}

func (rm *RecoveryManager) setOps(ops deviceOps) {
	rm.ops = ops
}

// setProcessStartForTest overrides the delay clock (tests only).
func (rm *RecoveryManager) setProcessStartForTest(t time.Time) {
	rm.processStart = t
}

func sessionKey(instanceID, friendlyName string, cfgIdx int) string {
	if instanceID != "" {
		return NormalizeInstanceID(instanceID)
	}
	return fmt.Sprintf("cfg:%d:%s", cfgIdx, friendlyName)
}

// StopNewWork prevents starting new recovery sessions (shutdown).
func (rm *RecoveryManager) StopNewWork() {
	rm.mu.Lock()
	rm.stopping = true
	rm.mu.Unlock()
}

// Wait blocks until all in-flight recovery workers finish.
func (rm *RecoveryManager) Wait() {
	rm.wg.Wait()
}

// FindMatcher returns the first matcher that matches friendlyName, or nil.
func (rm *RecoveryManager) FindMatcher(friendlyName string) (*Matcher, *DeviceConfig) {
	for _, m := range rm.matchers {
		if m.Match(friendlyName) {
			d := &rm.cfg.Devices[m.Index()]
			return m, d
		}
	}
	return nil, nil
}

// ClearExhausted resets Exhausted → Idle for every device (check command).
func (rm *RecoveryManager) ClearExhausted() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for key, sess := range rm.sessions {
		sess.mu.Lock()
		if sess.state == StateExhausted {
			sess.state = StateIdle
			sess.attempts = 0
			rm.logger.Infof("cleared Exhausted for %q (%s); next recovery starts at attempt=1", sess.name, key)
		}
		sess.mu.Unlock()
	}
}

// Scan enumerates devices and starts recovery for unhealthy configured matches.
// reason=check clears Exhausted first and ignores per-device delay.
// reason=pnp_event / startup skip Exhausted and apply delay.
func (rm *RecoveryManager) Scan(ctx context.Context, reason ScanReason) {
	rm.logger.Infof("scan start: reason=%s trigger_instance=%q trigger_action=%q ignore_delay=%v",
		reason.Reason, reason.TriggerInstance, reason.TriggerAction, reason.IgnoreDelay)

	if reason.Reason == "check" {
		rm.ClearExhausted()
	}

	ignoreDelay := reason.IgnoreDelay || reason.Reason == "check"

	devices, err := rm.ops.Enumerate()
	if err != nil {
		rm.logger.Errorf("enumerate failed (reason=%s): %v", reason.Reason, err)
		return
	}

	for _, m := range rm.matchers {
		found := false
		for i := range devices {
			dev := &devices[i]
			if !m.Match(dev.FriendlyName) {
				continue
			}
			found = true
			if dev.DevInst != 0 {
				rm.logger.Infof("scan match: reason=%s pattern=%q name=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d healthy=%v",
					reason.Reason, m.Pattern(), dev.FriendlyName, dev.InstanceID, dev.DevInst, dev.Status, dev.ProblemCode, dev.IsHealthy())
			} else {
				rm.logger.Infof("scan match: reason=%s pattern=%q name=%q instance=%q event_devinst_unset status=0x%X problem=%d healthy=%v",
					reason.Reason, m.Pattern(), dev.FriendlyName, dev.InstanceID, dev.Status, dev.ProblemCode, dev.IsHealthy())
			}
			if !dev.IsHealthy() {
				rm.HandleDeviceProblem(ctx, dev, ignoreDelay)
			}
		}
		if !found {
			if reason.Reason == "startup" {
				rm.logger.Warnf("configured device not found at startup: pattern=%q (will keep listening)", m.Pattern())
			} else {
				rm.logger.Warnf("configured device not found: pattern=%q reason=%s", m.Pattern(), reason.Reason)
			}
		}
	}
}

// HandleDeviceProblem starts a recovery session if the device matches and is unhealthy.
// Exhausted devices are skipped (use check). Same device is serial; different devices parallel.
// ignoreDelay=true (IPC/CLI check) skips the per-device delay gate but still applies
// ProblemCode filter and enable/disable state gates.
func (rm *RecoveryManager) HandleDeviceProblem(ctx context.Context, info *DeviceInfo, ignoreDelay bool) {
	if info == nil {
		return
	}
	m, dcfg := rm.FindMatcher(info.FriendlyName)
	if m == nil {
		return
	}
	if info.IsHealthy() {
		rm.logger.Infof("device healthy, skip recovery: name=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d",
			info.FriendlyName, info.InstanceID, info.DevInst, info.Status, info.ProblemCode)
		return
	}

	// ProblemCode filter (applies to auto and check).
	if dcfg.HasProblemCodeFilter() && !dcfg.MatchesProblemCode(info.ProblemCode) {
		rm.logger.Infof("skip Recover: ProblemCode=%d not in configured set %v for %q (instance=%q); keep listening",
			info.ProblemCode, []uint32(dcfg.ProblemCode), info.FriendlyName, info.InstanceID)
		rm.logger.Debugf("ProblemCode filter detail: name=%q status=0x%X DN_STARTED=%v disabled=%v",
			info.FriendlyName, info.Status, info.Status&DN_STARTED != 0, info.IsDisabled())
		return
	}

	// Per-device delay since process start (auto only; check ignores).
	if !ignoreDelay {
		need := time.Duration(dcfg.DelaySeconds()) * time.Second
		elapsed := time.Since(rm.processStart)
		if need > 0 && elapsed < need {
			remain := need - elapsed
			rm.logger.Infof("skip Recover: delay not elapsed for %q (delay=%ds elapsed=%.1fs remain=%.1fs); keep listening",
				info.FriendlyName, dcfg.DelaySeconds(), elapsed.Seconds(), remain.Seconds())
			rm.logger.Debugf("delay gate detail: instance=%q process_start=%s ignore_delay=%v",
				info.InstanceID, rm.processStart.Format(time.RFC3339Nano), ignoreDelay)
			return
		}
	}

	key := sessionKey(info.InstanceID, info.FriendlyName, m.Index())

	rm.mu.Lock()
	if rm.stopping {
		rm.mu.Unlock()
		rm.logger.Warnf("shutdown in progress, ignore recovery for %q (%s)", info.FriendlyName, key)
		return
	}
	sess, ok := rm.sessions[key]
	if !ok {
		sess = &deviceSession{
			state:      StateIdle,
			cfgIdx:     m.Index(),
			maxRetries: dcfg.MaxRetries,
			name:       info.FriendlyName,
			instanceID: info.InstanceID,
		}
		rm.sessions[key] = sess
	}
	rm.mu.Unlock()

	sess.mu.Lock()
	if sess.state == StateExhausted {
		sess.mu.Unlock()
		rm.logger.Infof("device Exhausted, skip automatic recovery for %q (%s); use check to clear", info.FriendlyName, key)
		return
	}
	if sess.running || sess.state == StateRecovering {
		sess.mu.Unlock()
		rm.logger.Infof("recovery already in progress for %q (%s), coalesce event", info.FriendlyName, key)
		return
	}
	sess.running = true
	sess.state = StateRecovering
	sess.mu.Unlock()

	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		defer func() {
			sess.mu.Lock()
			sess.running = false
			if sess.state == StateRecovering {
				sess.state = StateIdle
			}
			sess.mu.Unlock()
		}()
		rm.runSession(ctx, sess, key, info, dcfg, m)
	}()
}

func (rm *RecoveryManager) refreshDevice(info *DeviceInfo) *DeviceInfo {
	if info == nil {
		return nil
	}
	if info.InstanceID != "" {
		if cur, err := rm.ops.GetByInstanceID(info.InstanceID); err == nil && cur != nil {
			return cur
		}
	}
	if info.FriendlyName != "" {
		if cur, err := rm.ops.FindByFriendlyName(info.FriendlyName); err == nil && cur != nil {
			return cur
		}
	}
	return info
}

func (rm *RecoveryManager) runSession(ctx context.Context, sess *deviceSession, key string, initial *DeviceInfo, dcfg *DeviceConfig, m *Matcher) {
	rm.logger.Infof("recovery session start: name=%q pattern=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d max_retries=%d",
		initial.FriendlyName, m.Pattern(), initial.InstanceID, initial.DevInst, initial.Status, initial.ProblemCode, dcfg.MaxRetries)

	for {
		select {
		case <-ctx.Done():
			rm.logger.Warnf("recovery cancelled for %q (%s)", initial.FriendlyName, key)
			return
		default:
		}

		sess.mu.Lock()
		sess.attempts++
		attempt := sess.attempts
		maxR := sess.maxRetries
		sess.mu.Unlock()

		if attempt > maxR {
			rm.logger.Errorf("recovery exhausted max_retries=%d for %q (%s); marking Exhausted (check to clear; no auto-retry on PnP)",
				maxR, initial.FriendlyName, key)
			sess.mu.Lock()
			sess.state = StateExhausted
			sess.attempts = 0 // reset for display; next check session starts at 1
			sess.mu.Unlock()
			return
		}

		rm.logger.Infof("recovery attempt %d/%d for %q (%s)", attempt, maxR, initial.FriendlyName, key)

		info := rm.refreshDevice(initial)
		if info.InstanceID != "" {
			if info.DevInst != 0 {
				rm.logger.Infof("resolved_DEVINST=%d instance=%q (after LocateDevNode)", info.DevInst, info.InstanceID)
			} else {
				rm.logger.Infof("event_devinst_unset after GetDeviceByInstanceID instance=%q", info.InstanceID)
			}
		}

		if info.IsHealthy() {
			rm.logger.Infof("device already healthy before disable/enable: name=%q instance=%q; reset counter",
				info.FriendlyName, info.InstanceID)
			sess.mu.Lock()
			sess.attempts = 0
			sess.state = StateIdle
			sess.mu.Unlock()
			return
		}

		// Re-apply ProblemCode filter on refreshed state (device may have changed).
		if dcfg.HasProblemCodeFilter() && !dcfg.MatchesProblemCode(info.ProblemCode) {
			rm.logger.Infof("skip Recover mid-session: ProblemCode=%d not in set %v for %q; ending session without Exhausted",
				info.ProblemCode, []uint32(dcfg.ProblemCode), info.FriendlyName)
			sess.mu.Lock()
			sess.attempts = 0
			sess.state = StateIdle
			sess.mu.Unlock()
			return
		}

		// Before Disable: must be ENABLED; if already disabled, skip Disable.
		info = rm.refreshDevice(info)
		if info.IsDisabled() {
			rm.logger.Infof("skip Disable: device already disabled (ProblemCode=%d status=0x%X DN_STARTED=%v) name=%q instance=%q",
				info.ProblemCode, info.Status, info.Status&DN_STARTED != 0, info.FriendlyName, info.InstanceID)
		} else {
			rm.logger.Infof("disable device: name=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d",
				info.FriendlyName, info.InstanceID, info.DevInst, info.Status, info.ProblemCode)
			if err := rm.ops.Disable(info); err != nil {
				rm.logger.Errorf("disable failed for %q (%s): %v", info.FriendlyName, key, err)
			} else {
				rm.logger.Infof("disable OK for %q (%s)", info.FriendlyName, key)
			}
		}

		if !sleepOrDone(ctx, rm.delay) {
			rm.logger.Warnf("recovery cancelled during post-disable sleep for %q", info.FriendlyName)
			return
		}

		// Before Enable: must be DISABLED; if already enabled, skip Enable.
		info = rm.refreshDevice(info)
		if !info.IsDisabled() {
			rm.logger.Infof("skip Enable: device not disabled (ProblemCode=%d status=0x%X DN_STARTED=%v) name=%q instance=%q; will not force",
				info.ProblemCode, info.Status, info.Status&DN_STARTED != 0, info.FriendlyName, info.InstanceID)
		} else {
			rm.logger.Infof("enable device: name=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d",
				info.FriendlyName, info.InstanceID, info.DevInst, info.Status, info.ProblemCode)
			if err := rm.ops.Enable(info); err != nil {
				rm.logger.Errorf("enable failed for %q (%s): %v", info.FriendlyName, key, err)
			} else {
				rm.logger.Infof("enable OK for %q (%s)", info.FriendlyName, key)
			}
		}

		if !sleepOrDone(ctx, rm.delay) {
			rm.logger.Warnf("recovery cancelled during post-enable sleep for %q", info.FriendlyName)
			return
		}

		post := rm.refreshDevice(info)
		if post.InstanceID != "" && post.DevInst != 0 {
			rm.logger.Infof("resolved_DEVINST=%d instance=%q (post-recovery recheck)", post.DevInst, post.InstanceID)
		}

		rm.logger.Infof("scan/recheck: reason=recovery_postcheck name=%q instance=%q resolved_DEVINST=%d status=0x%X problem=%d healthy=%v",
			post.FriendlyName, post.InstanceID, post.DevInst, post.Status, post.ProblemCode, post.IsHealthy())

		if post.IsHealthy() {
			rm.logger.Infof("recovery SUCCESS for %q (%s) on attempt %d; reset counter", post.FriendlyName, key, attempt)
			sess.mu.Lock()
			sess.attempts = 0
			sess.state = StateIdle
			sess.mu.Unlock()
			return
		}

		rm.logger.Warnf("recovery attempt %d/%d did not restore %q (%s); status=0x%X problem=%d",
			attempt, maxR, post.FriendlyName, key, post.Status, post.ProblemCode)
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (rm *RecoveryManager) sessionView(key string) (state string, attempt int) {
	rm.mu.Lock()
	sess := rm.sessions[key]
	rm.mu.Unlock()
	if sess == nil {
		return StateIdle, 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.state, sess.attempts
}

func (rm *RecoveryManager) sessionViewByCfg(cfgIdx int) (state, instance string, attempt int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, sess := range rm.sessions {
		sess.mu.Lock()
		if sess.cfgIdx == cfgIdx {
			st, att, inst := sess.state, sess.attempts, sess.instanceID
			sess.mu.Unlock()
			return st, inst, att
		}
		sess.mu.Unlock()
	}
	return StateIdle, "", 0
}

// DeviceStatuses builds per-configured-device status rows.
func (rm *RecoveryManager) DeviceStatuses() []DeviceStatus {
	devices, err := rm.ops.Enumerate()
	if err != nil {
		return rm.statusesFromConfig(nil)
	}
	return rm.statusesFromConfig(devices)
}

func (rm *RecoveryManager) statusesFromConfig(devices []DeviceInfo) []DeviceStatus {
	var out []DeviceStatus
	for _, m := range rm.matchers {
		dcfg := rm.cfg.Devices[m.Index()]
		found := false
		for i := range devices {
			dev := &devices[i]
			if !m.Match(dev.FriendlyName) {
				continue
			}
			found = true
			key := sessionKey(dev.InstanceID, dev.FriendlyName, m.Index())
			state, attempt := rm.sessionView(key)
			out = append(out, DeviceStatus{
				Instance:     dev.InstanceID,
				FriendlyName: dev.FriendlyName,
				Healthy:      dev.IsHealthy(),
				Problem:      dev.ProblemCode,
				State:        state,
				Attempt:      attempt,
				MaxRetries:   dcfg.MaxRetries,
			})
		}
		if !found {
			state, inst, attempt := rm.sessionViewByCfg(m.Index())
			out = append(out, DeviceStatus{
				Instance:     inst,
				FriendlyName: m.Pattern(),
				Healthy:      false,
				Problem:      0,
				State:        state,
				Attempt:      attempt,
				MaxRetries:   dcfg.MaxRetries,
			})
		}
	}
	return out
}
