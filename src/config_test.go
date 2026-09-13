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

func validLogJSON() string {
	return `"log": {"enabled": true, "level": "normal", "path": "test.log"}`
}

func TestLoadConfigOK(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "Foo Device", "max_retries": 3, "delay": 60, "ProblemCode": [43]}],
		"retry_delay": 2
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].FriendlyName != "Foo Device" {
		t.Fatalf("unexpected devices: %+v", cfg.Devices)
	}
	if cfg.RetryDelay != 2 || cfg.Log == nil || !cfg.Log.Enabled || cfg.Log.Path != "test.log" {
		t.Fatalf("unexpected fields: %+v", cfg)
	}
	if cfg.Devices[0].Delay != 60 || !cfg.Devices[0].MatchesProblemCode(43) || cfg.Devices[0].MatchesProblemCode(10) {
		t.Fatalf("unexpected device fields: %+v", cfg.Devices[0])
	}
}

func TestLoadConfigProblemCodeNumber(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "Foo", "max_retries": 1, "ProblemCode": 43}],
		"retry_delay": 0
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices[0].ProblemCode) != 1 || cfg.Devices[0].ProblemCode[0] != 43 {
		t.Fatalf("ProblemCode: %v", cfg.Devices[0].ProblemCode)
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

func TestValidateMissingLog(t *testing.T) {
	cfg := &Config{Devices: []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}}, RetryDelay: 0}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log object is required") {
		t.Fatalf("expected missing log error, got %v", err)
	}
}

func TestValidateBadLogLevel(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "verbose", Path: ""},
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
		RetryDelay: 0,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log.level") {
		t.Fatalf("expected log.level error, got %v", err)
	}
}

func TestValidateEmptyDevices(t *testing.T) {
	cfg := &Config{Log: &LogConfig{Enabled: true, Level: "normal"}, Devices: nil, RetryDelay: 1}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty devices error, got %v", err)
	}
}

func TestValidateEmptyName(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "  ", MaxRetries: 1}},
		RetryDelay: 1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "friendly_name") {
		t.Fatalf("expected empty name error, got %v", err)
	}
}

func TestValidateBadRegex(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "regex:[", MaxRetries: 1}},
		RetryDelay: 1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected bad regex error, got %v", err)
	}
}

func TestValidateEmptyRegex(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "regex:", MaxRetries: 1}},
		RetryDelay: 1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty regex") {
		t.Fatalf("expected empty regex error, got %v", err)
	}
}

func TestValidateMaxRetries(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 0}},
		RetryDelay: 1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_retries") {
		t.Fatalf("expected max_retries error, got %v", err)
	}
}

func TestValidateRetryDelay(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
		RetryDelay: -1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "retry_delay") {
		t.Fatalf("expected retry_delay error, got %v", err)
	}
}

func TestValidateNegativeDelay(t *testing.T) {
	cfg := &Config{
		Log:        &LogConfig{Enabled: true, Level: "normal"},
		Devices:    []DeviceConfig{{FriendlyName: "X", MaxRetries: 1, Delay: -5}},
		RetryDelay: 0,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "delay") {
		t.Fatalf("expected delay error, got %v", err)
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
	if cfg.Log == nil {
		t.Fatal("sample must have log object")
	}
}
