package main

import (
	"context"
	"sync"
	"time"
)

// Daemon phases (status dump). INITIAL_SCAN is a step, not a reported phase.
const (
	PhaseStarting           = "STARTING"
	PhaseWaitForSystemReady = "WAIT_FOR_SYSTEM_READY"
	PhaseNormalRunning      = "NORMAL_RUNNING"
)

// Default trailing-edge debounce when advanced.pnp_debounce_ms is omitted.
const defaultDebounceInterval = 500 * time.Millisecond

// ScanReason describes why a configured-device scan was started.
type ScanReason struct {
	Reason          string
	TriggerInstance string
	TriggerAction   string
	IgnoreDelay     bool // true for IPC/CLI check — recover immediately despite devices[].rec_delay
}

// eventCoalescer is a trailing-edge debounce: each Trigger resets the timer;
// when it fires, fire() runs once with the last instance/action.
type eventCoalescer struct {
	interval time.Duration
	mu       sync.Mutex
	timer    *time.Timer
	instance string
	action   string
	fire     func(instance, action string)
}

func newEventCoalescer(interval time.Duration, fire func(string, string)) *eventCoalescer {
	return &eventCoalescer{interval: interval, fire: fire}
}

func (c *eventCoalescer) Trigger(instance, action string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instance = instance
	c.action = action
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(c.interval, func() {
		c.mu.Lock()
		inst, act := c.instance, c.action
		c.mu.Unlock()
		if c.fire != nil {
			c.fire(inst, act)
		}
	})
}

func (c *eventCoalescer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

func (c *eventCoalescer) Last() (instance, action string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instance, c.action
}

// Daemon owns phases, PnP coalescing, recovery, and IPC handlers.
type Daemon struct {
	cfg      *Config
	logger   *Logger
	rm       *RecoveryManager
	listener PnPListener
	matchers []*Matcher

	mu                 sync.Mutex
	phase              string
	listenerRegistered bool
	pendingStartupPnP  bool

	coalescer *eventCoalescer
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewDaemon constructs a daemon in phase STARTING.
func NewDaemon(cfg *Config, matchers []*Matcher, logger *Logger) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		cfg:      cfg,
		logger:   logger,
		rm:       NewRecoveryManager(cfg, matchers, logger),
		matchers: matchers,
		phase:    PhaseStarting,
		ctx:      ctx,
		cancel:   cancel,
	}
	debounce := defaultDebounceInterval
	if cfg != nil {
		ms := cfg.AdvancedResolved().PnpDebounceMs
		if ms >= 0 {
			debounce = time.Duration(ms) * time.Millisecond
		}
	}
	d.coalescer = newEventCoalescer(debounce, d.onDebounced)
	return d
}

// SetPhase updates the reported lifecycle phase.
func (d *Daemon) SetPhase(p string) {
	d.mu.Lock()
	d.phase = p
	d.mu.Unlock()
	d.logger.Infof("phase=%s", p)
}

// SetListenerRegistered records whether the PnP listener finished registration.
func (d *Daemon) SetListenerRegistered(v bool) {
	d.mu.Lock()
	d.listenerRegistered = v
	d.mu.Unlock()
}

func (d *Daemon) Phase() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phase
}

func (d *Daemon) pendingStartup() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pendingStartupPnP
}

func (d *Daemon) ClearPendingStartup() {
	d.mu.Lock()
	pending := d.pendingStartupPnP
	d.pendingStartupPnP = false
	d.mu.Unlock()
	if pending {
		d.logger.Infof("startup PnP events were pending; covered by initial scan")
	}
}

// OnPnPEvent is called from the listener worker (never from the raw callback).
// During STARTING / WAIT_FOR_SYSTEM_READY it only sets a pending flag.
// DISPLAY events are coalesced like any other — they trigger a scan of
// configured targets, never a direct mapping of the DISPLAY instance to GPU recovery.
func (d *Daemon) OnPnPEvent(ev PnPEvent) {
	if ev.DevInst == 0 {
		d.logger.Infof("PnP event: kind=%s instance=%q event_devinst_unset", ev.Kind, ev.InstanceID)
	} else {
		d.logger.Infof("PnP event: kind=%s instance=%q DEVINST=%d", ev.Kind, ev.InstanceID, ev.DevInst)
	}

	d.mu.Lock()
	phase := d.phase
	d.mu.Unlock()

	if phase != PhaseNormalRunning {
		d.mu.Lock()
		d.pendingStartupPnP = true
		d.mu.Unlock()
		d.logger.Infof("startup PnP event pending: kind=%s instance=%q (phase=%s, recovery deferred)",
			ev.Kind, ev.InstanceID, phase)
		return
	}

	d.coalescer.Trigger(ev.InstanceID, ev.Kind)
}

func (d *Daemon) onDebounced(instance, action string) {
	if d.ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	phase := d.phase
	d.mu.Unlock()
	if phase != PhaseNormalRunning {
		return
	}
	d.logger.Infof("coalesced PnP scan: reason=pnp_event trigger_instance=%q trigger_action=%q", instance, action)
	d.rm.Scan(d.ctx, ScanReason{
		Reason:          "pnp_event",
		TriggerInstance: instance,
		TriggerAction:   action,
	})
}

// HandleIPC serves {"cmd":"check"} / {"cmd":"status"}.
func (d *Daemon) HandleIPC(req IPCRequest) IPCResponse {
	if err := validateIPCRequest(req); err != nil {
		return IPCResponse{OK: false, Error: err.Error()}
	}
	switch req.Cmd {
	case "status":
		return IPCResponse{OK: true, Status: d.Snapshot()}
	case "check":
		d.logger.Infof("IPC check: clearing Exhausted and rescanning (IgnoreDelay)")
		d.rm.Scan(d.ctx, ScanReason{Reason: "check", IgnoreDelay: true})
		return IPCResponse{OK: true, Status: d.Snapshot()}
	default:
		return IPCResponse{OK: false, Error: "unknown cmd"}
	}
}

// Snapshot returns the current phase, listener flag, and per-device rows.
func (d *Daemon) Snapshot() *StatusSnapshot {
	d.mu.Lock()
	phase := d.phase
	registered := d.listenerRegistered
	d.mu.Unlock()
	return &StatusSnapshot{
		Phase:              phase,
		ListenerRegistered: registered,
		Devices:            d.rm.DeviceStatuses(),
	}
}

// Shutdown stops coalescing, new recovery, the listener, and waits for workers.
func (d *Daemon) Shutdown() int {
	d.logger.Infof("shutting down: stop new recovery sessions")
	if d.coalescer != nil {
		d.coalescer.Stop()
	}
	d.rm.StopNewWork()
	d.cancel()
	if d.listener != nil {
		d.logger.Infof("unregistering PnP listener")
		_ = d.listener.Stop()
	}
	d.logger.Infof("waiting for recovery workers")
	done := make(chan struct{})
	go func() {
		d.rm.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		d.logger.Warnf("timeout waiting for recovery workers")
	}
	d.logger.Infof("PnP Device Recovery stopped cleanly")
	return 0
}
