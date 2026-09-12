package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RecoveryManager coordinates per-device recovery sessions.
// Same device never runs concurrent recoveries (events coalesce).
// Exhausting max_retries ends that session only — listener stays alive.
type RecoveryManager struct {
	cfg      *Config
	matchers []*Matcher
	logger   *Logger
	delay    time.Duration

	mu       sync.Mutex
	sessions map[string]*deviceSession // key: InstanceID
	wg       sync.WaitGroup
	stopping bool
}

type deviceSession struct {
	mu         sync.Mutex
	running    bool
	attempts   int
	cfgIdx     int
	maxRetries int
	name       string // matched friendly name / pattern
}

// NewRecoveryManager creates a manager for the given config and matchers.
func NewRecoveryManager(cfg *Config, matchers []*Matcher, logger *Logger) *RecoveryManager {
	return &RecoveryManager{
		cfg:      cfg,
		matchers: matchers,
		logger:   logger,
		delay:    time.Duration(cfg.RetryDelay) * time.Second,
		sessions: make(map[string]*deviceSession),
	}
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

// HandleDeviceProblem is called from PnP event workers (never from the PnP callback itself).
// It starts a recovery session if the device matches and is unhealthy, coalescing duplicates.
func (rm *RecoveryManager) HandleDeviceProblem(ctx context.Context, info *DeviceInfo) {
	if info == nil {
		return
	}
	m, dcfg := rm.FindMatcher(info.FriendlyName)
	if m == nil {
		return
	}
	if info.IsHealthy() {
		rm.logger.Infof("device healthy, skip recovery: name=%q instance=%q DEVINST=%d status=0x%X problem=%d",
			info.FriendlyName, info.InstanceID, info.DevInst, info.Status, info.ProblemCode)
		return
	}

	key := info.InstanceID
	if key == "" {
		key = fmt.Sprintf("cfg:%d:%s", m.Index(), info.FriendlyName)
	}

	rm.mu.Lock()
	if rm.stopping {
		rm.mu.Unlock()
		rm.logger.Warnf("shutdown in progress, ignore recovery for %q (%s)", info.FriendlyName, key)
		return
	}
	sess, ok := rm.sessions[key]
	if !ok {
		sess = &deviceSession{
			cfgIdx:     m.Index(),
			maxRetries: dcfg.MaxRetries,
			name:       info.FriendlyName,
		}
		rm.sessions[key] = sess
	}
	rm.mu.Unlock()

	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		rm.logger.Infof("recovery already in progress for %q (%s), coalesce event", info.FriendlyName, key)
		return
	}
	sess.running = true
	sess.mu.Unlock()

	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		defer func() {
			sess.mu.Lock()
			sess.running = false
			sess.mu.Unlock()
		}()
		rm.runSession(ctx, sess, key, info, dcfg, m)
	}()
}

func (rm *RecoveryManager) runSession(ctx context.Context, sess *deviceSession, key string, initial *DeviceInfo, dcfg *DeviceConfig, m *Matcher) {
	rm.logger.Infof("recovery session start: name=%q pattern=%q instance=%q DEVINST=%d status=0x%X problem=%d max_retries=%d",
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
			rm.logger.Errorf("recovery exhausted max_retries=%d for %q (%s); ending session (listener continues)",
				maxR, initial.FriendlyName, key)
			sess.mu.Lock()
			sess.attempts = 0 // next session starts at attempt 1
			sess.mu.Unlock()
			return
		}

		rm.logger.Infof("recovery attempt %d/%d for %q (%s)", attempt, maxR, initial.FriendlyName, key)

		// Re-fetch current device state by instance ID when possible.
		info := initial
		if initial.InstanceID != "" {
			if cur, err := GetDeviceByInstanceID(initial.InstanceID); err == nil && cur != nil {
				info = cur
			}
		}

		if info.IsHealthy() {
			rm.logger.Infof("device already healthy before disable/enable: name=%q instance=%q; reset counter",
				info.FriendlyName, info.InstanceID)
			sess.mu.Lock()
			sess.attempts = 0
			sess.mu.Unlock()
			return
		}

		rm.logger.Infof("disable device: name=%q instance=%q DEVINST=%d", info.FriendlyName, info.InstanceID, info.DevInst)
		if err := DisableDevice(info); err != nil {
			rm.logger.Errorf("disable failed for %q (%s): %v", info.FriendlyName, key, err)
		} else {
			rm.logger.Infof("disable OK for %q (%s)", info.FriendlyName, key)
		}

		if !sleepOrDone(ctx, rm.delay) {
			rm.logger.Warnf("recovery cancelled during post-disable sleep for %q", info.FriendlyName)
			return
		}

		rm.logger.Infof("enable device: name=%q instance=%q DEVINST=%d", info.FriendlyName, info.InstanceID, info.DevInst)
		if err := EnableDevice(info); err != nil {
			rm.logger.Errorf("enable failed for %q (%s): %v", info.FriendlyName, key, err)
		} else {
			rm.logger.Infof("enable OK for %q (%s)", info.FriendlyName, key)
		}

		if !sleepOrDone(ctx, rm.delay) {
			rm.logger.Warnf("recovery cancelled during post-enable sleep for %q", info.FriendlyName)
			return
		}

		// Recheck status
		post := info
		if info.InstanceID != "" {
			if cur, err := GetDeviceByInstanceID(info.InstanceID); err == nil && cur != nil {
				post = cur
			} else if err != nil {
				rm.logger.Warnf("recheck GetDeviceByInstanceID failed for %s: %v", info.InstanceID, err)
			}
		} else {
			// Fall back to friendly-name scan
			if cur, err := FindDeviceByFriendlyName(info.FriendlyName); err == nil && cur != nil {
				post = cur
			}
		}

		rm.logger.Infof("post-recovery status: name=%q instance=%q DEVINST=%d status=0x%X problem=%d healthy=%v",
			post.FriendlyName, post.InstanceID, post.DevInst, post.Status, post.ProblemCode, post.IsHealthy())

		if post.IsHealthy() {
			rm.logger.Infof("recovery SUCCESS for %q (%s) on attempt %d; reset counter", post.FriendlyName, key, attempt)
			sess.mu.Lock()
			sess.attempts = 0
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

// ScanAndRecoverMissing checks configured devices at startup.
// Missing devices are logged; listening continues.
func (rm *RecoveryManager) ScanAndRecoverMissing(ctx context.Context) {
	devices, err := EnumerateDevices()
	if err != nil {
		rm.logger.Errorf("initial enumerate failed: %v (will keep listening)", err)
		return
	}

	matched := make(map[int]bool)
	for _, m := range rm.matchers {
		found := false
		for i := range devices {
			dev := &devices[i]
			if !m.Match(dev.FriendlyName) {
				continue
			}
			found = true
			matched[m.Index()] = true
			rm.logger.Infof("startup match: pattern=%q name=%q instance=%q DEVINST=%d status=0x%X problem=%d healthy=%v",
				m.Pattern(), dev.FriendlyName, dev.InstanceID, dev.DevInst, dev.Status, dev.ProblemCode, dev.IsHealthy())
			if !dev.IsHealthy() {
				rm.HandleDeviceProblem(ctx, dev)
			}
		}
		if !found {
			rm.logger.Warnf("configured device not found at startup: pattern=%q (will keep listening)", m.Pattern())
		}
	}
	_ = matched
}
