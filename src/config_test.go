package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadConfigOK(t *testing.T) {
	path := writeTempConfig(t, `{
		"devices": [{"friendly_name": "Foo Device", "max_retries": 3}],
		"retry_delay": 2,
		"log_file": "test.log"
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].FriendlyName != "Foo Device" {
		t.Fatalf("unexpected devices: %+v", cfg.Devices)
	}
	if cfg.RetryDelay != 2 || cfg.LogFile != "test.log" {
		t.Fatalf("unexpected fields: %+v", cfg)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestLoadConfigBadJSON(t *testing.T) {
	path := writeTempConfig(t, `{not json`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON, got %v", err)
	}
}

func TestValidateEmptyDevices(t *testing.T) {
	cfg := &Config{Devices: nil, RetryDelay: 1, LogFile: "a.log"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty devices error, got %v", err)
	}
}

func TestValidateEmptyName(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "  ", MaxRetries: 1}},
		RetryDelay: 1,
		LogFile:    "a.log",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "friendly_name") {
		t.Fatalf("expected empty name error, got %v", err)
	}
}

func TestValidateBadRegex(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "regex:[", MaxRetries: 1}},
		RetryDelay: 1,
		LogFile:    "a.log",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected bad regex error, got %v", err)
	}
}

func TestValidateEmptyRegex(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "regex:", MaxRetries: 1}},
		RetryDelay: 1,
		LogFile:    "a.log",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty regex") {
		t.Fatalf("expected empty regex error, got %v", err)
	}
}

func TestValidateMaxRetries(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 0}},
		RetryDelay: 1,
		LogFile:    "a.log",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_retries") {
		t.Fatalf("expected max_retries error, got %v", err)
	}
}

func TestValidateRetryDelay(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
		RetryDelay: -1,
		LogFile:    "a.log",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "retry_delay") {
		t.Fatalf("expected retry_delay error, got %v", err)
	}
}

func TestValidateEmptyLogFile(t *testing.T) {
	cfg := &Config{
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
		RetryDelay: 0,
		LogFile:    "  ",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log_file") {
		t.Fatalf("expected log_file error, got %v", err)
	}
}

func TestLoadSampleConfig(t *testing.T) {
	path := "config.json"
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join("..", "config.json")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("sample config.json: %v", err)
	}
	if len(cfg.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(cfg.Devices))
	}
}
