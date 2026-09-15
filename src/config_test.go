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
		"devices": [{"friendly_name": "Foo Device", "max_retries": 3, "rec_delay": 60, "ProblemCode": [43]}]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].FriendlyName != "Foo Device" {
		t.Fatalf("unexpected devices: %+v", cfg.Devices)
	}
	if cfg.Log == nil || !cfg.Log.Enabled || cfg.Log.Path != "test.log" {
		t.Fatalf("unexpected fields: %+v", cfg)
	}
	rd := cfg.Devices[0].RecDelay
	if !rd.Present || rd.Based != "uptime" || rd.Sec != 60 {
		t.Fatalf("rec_delay bare number: %+v", rd)
	}
	if !cfg.Devices[0].MatchesProblemCode(43) || cfg.Devices[0].MatchesProblemCode(10) {
		t.Fatalf("unexpected ProblemCode: %+v", cfg.Devices[0])
	}
	adv := cfg.AdvancedResolved()
	if adv.RetryInterval != 3 || adv.UptimeBypass != 60 || adv.LogMaxBytes != defaultLogMaxBytes {
		t.Fatalf("expected advanced defaults, got %+v", adv)
	}
}

func TestLoadConfigRecDelayObject(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{
			"friendly_name": "Foo",
			"max_retries": 1,
			"rec_delay": {"based": "daemon", "sec": 15}
		}]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	rd := cfg.Devices[0].RecDelay
	if !rd.Present || rd.Based != "daemon" || rd.Sec != 15 {
		t.Fatalf("rec_delay object: %+v", rd)
	}
}

func TestLoadConfigRecDelayOmit(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "Foo", "max_retries": 1}]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Devices[0].RecDelay.Present {
		t.Fatalf("omit rec_delay should not be Present: %+v", cfg.Devices[0].RecDelay)
	}
}

func TestLoadConfigAdvancedOverrides(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "Foo", "max_retries": 1}],
		"advanced": {
			"retry_interval": 5,
			"uptime_bypass": 0,
			"pnp_debounce_ms": 100,
			"log_max_bytes": 1024
		}
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	adv := cfg.AdvancedResolved()
	if adv.RetryInterval != 5 || adv.UptimeBypass != 0 || adv.PnpDebounceMs != 100 || adv.LogMaxBytes != 1024 {
		t.Fatalf("advanced overrides: %+v", adv)
	}
	// Unset fields keep defaults
	if adv.ReadyMinUptimeSec != 1 || adv.PnpRegisterWaitSec != 3 {
		t.Fatalf("partial advanced should keep other defaults: %+v", adv)
	}
}

func TestLoadConfigProblemCodeNumber(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "Foo", "max_retries": 1, "ProblemCode": 43}]
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
	cfg := &Config{Devices: []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log object is required") {
		t.Fatalf("expected missing log error, got %v", err)
	}
}

func TestValidateBadLogLevel(t *testing.T) {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: "verbose", Path: ""},
		Devices: []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "log.level") {
		t.Fatalf("expected log.level error, got %v", err)
	}
}

func TestValidateEmptyDevices(t *testing.T) {
	cfg := &Config{Log: &LogConfig{Enabled: true, Level: "normal"}, Devices: nil}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty devices error, got %v", err)
	}
}

func TestValidateEmptyName(t *testing.T) {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{FriendlyName: "  ", MaxRetries: 1}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "friendly_name") {
		t.Fatalf("expected empty name error, got %v", err)
	}
}

func TestValidateBadRegex(t *testing.T) {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{FriendlyName: "regex:[", MaxRetries: 1}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected bad regex error, got %v", err)
	}
}

func TestValidateEmptyRegex(t *testing.T) {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{FriendlyName: "regex:", MaxRetries: 1}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty regex") {
		t.Fatalf("expected empty regex error, got %v", err)
	}
}

func TestValidateMaxRetries(t *testing.T) {
	cfg := &Config{
		Log:     &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{FriendlyName: "X", MaxRetries: 0}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_retries") {
		t.Fatalf("expected max_retries error, got %v", err)
	}
}

func TestValidateNegativeRecDelay(t *testing.T) {
	cfg := &Config{
		Log: &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{
			FriendlyName: "X",
			MaxRetries:   1,
			RecDelay:     RecDelaySpec{Present: true, Based: "uptime", Sec: -5},
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "rec_delay.sec") {
		t.Fatalf("expected rec_delay.sec error, got %v", err)
	}
}

func TestValidateBadRecDelayBased(t *testing.T) {
	cfg := &Config{
		Log: &LogConfig{Enabled: true, Level: "normal"},
		Devices: []DeviceConfig{{
			FriendlyName: "X",
			MaxRetries:   1,
			RecDelay:     RecDelaySpec{Present: true, Based: "boot", Sec: 1},
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "rec_delay.based") {
		t.Fatalf("expected rec_delay.based error, got %v", err)
	}
}

func TestValidateRecDelayObjectMissingField(t *testing.T) {
	path := writeTempConfig(t, `{
		`+validLogJSON()+`,
		"devices": [{"friendly_name": "X", "max_retries": 1, "rec_delay": {"based": "uptime"}}]
	}`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "rec_delay") {
		t.Fatalf("expected rec_delay object error, got %v", err)
	}
}

func TestValidateNegativeAdvanced(t *testing.T) {
	neg := -1
	cfg := &Config{
		Log:      &LogConfig{Enabled: true, Level: "normal"},
		Devices:  []DeviceConfig{{FriendlyName: "X", MaxRetries: 1}},
		Advanced: &AdvancedConfig{RetryInterval: &neg},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "retry_interval") {
		t.Fatalf("expected retry_interval error, got %v", err)
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
	if cfg.Advanced != nil {
		t.Fatal("sample should omit advanced")
	}
	rd := cfg.Devices[0].RecDelay
	if !rd.Present || rd.Based != "uptime" || rd.Sec != 10 {
		t.Fatalf("sample rec_delay: %+v", rd)
	}
}
