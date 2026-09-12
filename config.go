package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// DeviceConfig describes one monitored device entry from config.json.
type DeviceConfig struct {
	FriendlyName string `json:"friendly_name"`
	MaxRetries   int    `json:"max_retries"`
}

// Config is the root configuration loaded from config.json.
type Config struct {
	Devices    []DeviceConfig `json:"devices"`
	RetryDelay int            `json:"retry_delay"`
	LogFile    string         `json:"log_file"`
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
	if len(c.Devices) == 0 {
		return fmt.Errorf("config validation failed: devices list is empty")
	}
	if c.RetryDelay < 0 {
		return fmt.Errorf("config validation failed: retry_delay must be >= 0, got %d", c.RetryDelay)
	}
	if strings.TrimSpace(c.LogFile) == "" {
		return fmt.Errorf("config validation failed: log_file must not be empty")
	}

	for i, d := range c.Devices {
		name := strings.TrimSpace(d.FriendlyName)
		if name == "" {
			return fmt.Errorf("config validation failed: devices[%d].friendly_name is empty", i)
		}
		if d.MaxRetries <= 0 {
			return fmt.Errorf("config validation failed: devices[%d].max_retries must be > 0, got %d", i, d.MaxRetries)
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
