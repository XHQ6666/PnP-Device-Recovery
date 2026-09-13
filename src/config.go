package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	LogLevelNormal     = "normal"
	LogLevelDebug      = "debug"
	defaultLogFilename = "PnP-Device-Recovery.log"
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

// DeviceConfig describes one monitored device entry from config.json.
type DeviceConfig struct {
	FriendlyName string         `json:"friendly_name"`
	MaxRetries   int            `json:"max_retries"`
	Delay        int            `json:"delay"` // seconds after process start before auto Recover; omit = 0
	ProblemCode  ProblemCodeSet `json:"ProblemCode"`
}

// DelaySeconds returns the per-device auto-recover delay (0 if unset).
func (d *DeviceConfig) DelaySeconds() int {
	if d == nil {
		return 0
	}
	return d.Delay
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

// Config is the root configuration loaded from config.json.
type Config struct {
	Log        *LogConfig     `json:"log"`
	Devices    []DeviceConfig `json:"devices"`
	RetryDelay int            `json:"retry_delay"`
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

// Validate checks all config fields and returns a clear error on failure.
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
	if c.RetryDelay < 0 {
		return fmt.Errorf("config validation failed: retry_delay must be >= 0, got %d", c.RetryDelay)
	}

	for i, d := range c.Devices {
		name := strings.TrimSpace(d.FriendlyName)
		if name == "" {
			return fmt.Errorf("config validation failed: devices[%d].friendly_name is empty", i)
		}
		if d.MaxRetries <= 0 {
			return fmt.Errorf("config validation failed: devices[%d].max_retries must be > 0, got %d", i, d.MaxRetries)
		}
		if d.Delay < 0 {
			return fmt.Errorf("config validation failed: devices[%d].delay must be >= 0, got %d", i, d.Delay)
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
	return nil
}

// ResolveLogPath returns the absolute log path using exe directory for relative paths.
// Empty path → defaultLogFilename under the executable directory.
func (c *Config) ResolveLogPath() (string, error) {
	if c == nil || c.Log == nil {
		return ResolvePathRelativeToExe("")
	}
	return ResolvePathRelativeToExe(c.Log.Path)
}
