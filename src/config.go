package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	LogLevelNormal     = "normal"
	LogLevelDebug      = "debug"
	defaultLogFilename = "PnP-Device-Recovery.log"

	recDelayBasedUptime = "uptime"
	recDelayBasedDaemon = "daemon"
	recDelayBasedDevice = "device"

	defaultRetryIntervalSec      = 3
	defaultReadyMinUptimeSec     = 1
	defaultReadyPollMs           = 200
	defaultReadyMaxWaitSec       = 60
	defaultPnpDebounceMs         = 500
	defaultLogMaxBytes           = int64(10 * 1024 * 1024) // 10 MiB
	defaultPnpRegisterWaitSec    = 3
	defaultUptimeBypassSec       = 60
	defaultDisableEnableGapSec   = 3 // hardcoded; not a config field
)

// LogConfig controls logging (required in config.json).
type LogConfig struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	Path    string `json:"path"`
}

// ProblemCodeSet accepts a single number or an array of numbers in JSON.
type ProblemCodeSet []uint32

// UnmarshalJSON accepts either a JSON number or an array of numbers.
func (p *ProblemCodeSet) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*p = nil
		return nil
	}
	if data[0] == '[' {
		var arr []uint32
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("ProblemCode array: %w", err)
		}
		*p = arr
		return nil
	}
	var one uint32
	if err := json.Unmarshal(data, &one); err != nil {
		return fmt.Errorf("ProblemCode: %w", err)
	}
	*p = ProblemCodeSet{one}
	return nil
}

// RecDelaySpec is the wait-before-automatic-Recover gate for one device.
// Omit → no extra wait. Bare JSON number N ≡ {based:"uptime", sec:N}.
type RecDelaySpec struct {
	Present bool
	Based   string
	Sec     int
}

// UnmarshalJSON accepts a bare number or an object with both based and sec.
func (r *RecDelaySpec) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*r = RecDelaySpec{}
		return nil
	}
	if data[0] == '{' {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("rec_delay: %w", err)
		}
		basedRaw, hasBased := obj["based"]
		secRaw, hasSec := obj["sec"]
		if !hasBased || !hasSec {
			return fmt.Errorf("rec_delay object requires both \"based\" and \"sec\"")
		}
		var based string
		var sec int
		if err := json.Unmarshal(basedRaw, &based); err != nil {
			return fmt.Errorf("rec_delay.based: %w", err)
		}
		if err := json.Unmarshal(secRaw, &sec); err != nil {
			return fmt.Errorf("rec_delay.sec: %w", err)
		}
		*r = RecDelaySpec{Present: true, Based: strings.TrimSpace(based), Sec: sec}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("rec_delay: %w", err)
	}
	*r = RecDelaySpec{Present: true, Based: recDelayBasedUptime, Sec: n}
	return nil
}

// DeviceConfig describes one monitored device entry from config.json.
type DeviceConfig struct {
	FriendlyName string         `json:"friendly_name"`
	MaxRetries   int            `json:"max_retries"`
	RecDelay     RecDelaySpec   `json:"rec_delay"`
	ProblemCode  ProblemCodeSet `json:"ProblemCode"`
}

// HasProblemCodeFilter reports whether ProblemCode was configured (non-empty set).
func (d *DeviceConfig) HasProblemCodeFilter() bool {
	return d != nil && len(d.ProblemCode) > 0
}

// MatchesProblemCode returns true when no filter is set, or code is in the set.
func (d *DeviceConfig) MatchesProblemCode(code uint32) bool {
	if d == nil || len(d.ProblemCode) == 0 {
		return true
	}
	for _, c := range d.ProblemCode {
		if c == code {
			return true
		}
	}
	return false
}

// AdvancedConfig holds optional tuning knobs. Omit object or field → code defaults.
// Pointers distinguish omitted fields from explicit zeros (e.g. uptime_bypass: 0).
type AdvancedConfig struct {
	RetryInterval      *int   `json:"retry_interval"`
	ReadyMinUptimeSec  *int   `json:"ready_min_uptime_sec"`
	ReadyPollMs        *int   `json:"ready_poll_ms"`
	ReadyMaxWaitSec    *int   `json:"ready_max_wait_sec"`
	PnpDebounceMs      *int   `json:"pnp_debounce_ms"`
	LogMaxBytes        *int64 `json:"log_max_bytes"`
	PnpRegisterWaitSec *int   `json:"pnp_register_wait_sec"`
	UptimeBypass       *int   `json:"uptime_bypass"`
}

// AdvancedValues are resolved advanced settings after defaults are applied.
type AdvancedValues struct {
	RetryInterval      int
	ReadyMinUptimeSec  int
	ReadyPollMs        int
	ReadyMaxWaitSec    int
	PnpDebounceMs      int
	LogMaxBytes        int64
	PnpRegisterWaitSec int
	UptimeBypass       int // seconds; 0 = bypass off
}

// Config is the root configuration loaded from config.json.
type Config struct {
	Log      *LogConfig      `json:"log"`
	Devices  []DeviceConfig  `json:"devices"`
	Advanced *AdvancedConfig `json:"advanced"`

	// resolved is filled by Validate / LoadConfig (not from JSON).
	resolved AdvancedValues
}

// AdvancedResolved returns validated advanced values (defaults applied).
func (c *Config) AdvancedResolved() AdvancedValues {
	if c == nil {
		return defaultAdvancedValues()
	}
	return c.resolved
}

func defaultAdvancedValues() AdvancedValues {
	return AdvancedValues{
		RetryInterval:      defaultRetryIntervalSec,
		ReadyMinUptimeSec:  defaultReadyMinUptimeSec,
		ReadyPollMs:        defaultReadyPollMs,
		ReadyMaxWaitSec:    defaultReadyMaxWaitSec,
		PnpDebounceMs:      defaultPnpDebounceMs,
		LogMaxBytes:        defaultLogMaxBytes,
		PnpRegisterWaitSec: defaultPnpRegisterWaitSec,
		UptimeBypass:       defaultUptimeBypassSec,
	}
}

func resolveIntPtr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func resolveInt64Ptr(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}

func (c *Config) resolveAdvanced() AdvancedValues {
	def := defaultAdvancedValues()
	a := c.Advanced
	if a == nil {
		return def
	}
	return AdvancedValues{
		RetryInterval:      resolveIntPtr(a.RetryInterval, def.RetryInterval),
		ReadyMinUptimeSec:  resolveIntPtr(a.ReadyMinUptimeSec, def.ReadyMinUptimeSec),
		ReadyPollMs:        resolveIntPtr(a.ReadyPollMs, def.ReadyPollMs),
		ReadyMaxWaitSec:    resolveIntPtr(a.ReadyMaxWaitSec, def.ReadyMaxWaitSec),
		PnpDebounceMs:      resolveIntPtr(a.PnpDebounceMs, def.PnpDebounceMs),
		LogMaxBytes:        resolveInt64Ptr(a.LogMaxBytes, def.LogMaxBytes),
		PnpRegisterWaitSec: resolveIntPtr(a.PnpRegisterWaitSec, def.PnpRegisterWaitSec),
		UptimeBypass:       resolveIntPtr(a.UptimeBypass, def.UptimeBypass),
	}
}

// LoadConfig reads and validates config.json from path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid JSON in config file %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validateNonNegInt(field string, p *int) error {
	if p != nil && *p < 0 {
		return fmt.Errorf("config validation failed: %s must be >= 0, got %d", field, *p)
	}
	return nil
}

func validateNonNegInt64(field string, p *int64) error {
	if p != nil && *p < 0 {
		return fmt.Errorf("config validation failed: %s must be >= 0, got %d", field, *p)
	}
	return nil
}

// Validate checks all config fields and returns a clear error on failure.
// On success, resolved advanced defaults are applied to c.resolved.
func (c *Config) Validate() error {
	if c.Log == nil {
		return fmt.Errorf("config validation failed: log object is required")
	}
	level := strings.ToLower(strings.TrimSpace(c.Log.Level))
	if level != LogLevelNormal && level != LogLevelDebug {
		return fmt.Errorf("config validation failed: log.level must be %q or %q, got %q",
			LogLevelNormal, LogLevelDebug, c.Log.Level)
	}
	c.Log.Level = level

	if len(c.Devices) == 0 {
		return fmt.Errorf("config validation failed: devices list is empty")
	}

	if a := c.Advanced; a != nil {
		if err := validateNonNegInt("advanced.retry_interval", a.RetryInterval); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.ready_min_uptime_sec", a.ReadyMinUptimeSec); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.ready_poll_ms", a.ReadyPollMs); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.ready_max_wait_sec", a.ReadyMaxWaitSec); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.pnp_debounce_ms", a.PnpDebounceMs); err != nil {
			return err
		}
		if err := validateNonNegInt64("advanced.log_max_bytes", a.LogMaxBytes); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.pnp_register_wait_sec", a.PnpRegisterWaitSec); err != nil {
			return err
		}
		if err := validateNonNegInt("advanced.uptime_bypass", a.UptimeBypass); err != nil {
			return err
		}
	}

	for i, d := range c.Devices {
		name := strings.TrimSpace(d.FriendlyName)
		if name == "" {
			return fmt.Errorf("config validation failed: devices[%d].friendly_name is empty", i)
		}
		if d.MaxRetries <= 0 {
			return fmt.Errorf("config validation failed: devices[%d].max_retries must be > 0, got %d", i, d.MaxRetries)
		}
		if d.RecDelay.Present {
			based := strings.ToLower(strings.TrimSpace(d.RecDelay.Based))
			switch based {
			case recDelayBasedUptime, recDelayBasedDaemon, recDelayBasedDevice:
				c.Devices[i].RecDelay.Based = based
			default:
				return fmt.Errorf("config validation failed: devices[%d].rec_delay.based must be %q, %q, or %q, got %q",
					i, recDelayBasedUptime, recDelayBasedDaemon, recDelayBasedDevice, d.RecDelay.Based)
			}
			if d.RecDelay.Sec < 0 {
				return fmt.Errorf("config validation failed: devices[%d].rec_delay.sec must be >= 0, got %d", i, d.RecDelay.Sec)
			}
		}
		if strings.HasPrefix(name, "regex:") {
			pattern := strings.TrimPrefix(name, "regex:")
			if pattern == "" {
				return fmt.Errorf("config validation failed: devices[%d].friendly_name has empty regex after \"regex:\" prefix", i)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("config validation failed: devices[%d].friendly_name has invalid regex %q: %w", i, pattern, err)
			}
		}
	}

	c.resolved = c.resolveAdvanced()
	return nil
}

// ReadyDurations returns Ready wait parameters from resolved advanced config.
func (c *Config) ReadyDurations() (minUptime, poll, maxWait time.Duration) {
	v := c.AdvancedResolved()
	return time.Duration(v.ReadyMinUptimeSec) * time.Second,
		time.Duration(v.ReadyPollMs) * time.Millisecond,
		time.Duration(v.ReadyMaxWaitSec) * time.Second
}

// ResolveLogPath returns the absolute log path using exe directory for relative paths.
// Empty path → defaultLogFilename under the executable directory.
func (c *Config) ResolveLogPath() (string, error) {
	if c == nil || c.Log == nil {
		return ResolvePathRelativeToExe("")
	}
	return ResolvePathRelativeToExe(c.Log.Path)
}
