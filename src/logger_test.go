package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.log")
	lg, err := NewLoggerAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	lg.Log("hello %s", "world")
	lg.Infof("info line")
	lg.Warnf("warn line")
	lg.Errorf("err line")
	lg.Debugf("should not appear at normal")

	// Force rotation by setting size past limit
	lg.mu.Lock()
	lg.size = maxLogBytes
	lg.mu.Unlock()
	lg.Log("after rotate")

	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "after rotate") {
		t.Fatalf("expected rotated content, got %q", s)
	}
	if !strings.Contains(s, "log rotated") {
		t.Fatalf("expected rotation header, got %q", s)
	}
	if strings.Contains(s, "should not appear") {
		t.Fatal("debug line must not appear at normal level")
	}
}

func TestLoggerDebugLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbg.log")
	lg, err := NewLogger(&LogConfig{Enabled: true, Level: LogLevelDebug, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	lg.Debugf("debug detail %d", 42)
	_ = lg.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "debug detail 42") {
		t.Fatalf("expected debug line, got %q", data)
	}
}

func TestLoggerDisabledNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disabled.log")
	lg, err := NewLogger(&LogConfig{Enabled: false, Level: LogLevelDebug, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	lg.Infof("nope")
	lg.Debugf("nope2")
	lg.Warnf("nope3")
	lg.Errorf("nope4")
	_ = lg.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled logger must not create file, err=%v", err)
	}
	if lg.Enabled() {
		t.Fatal("Enabled should be false")
	}
}

func TestLoggerUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf8.log")
	lg, err := NewLoggerAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	lg.Log("中文测试 device=%q", "GPU")
	_ = lg.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "中文测试") {
		t.Fatal("UTF-8 not preserved")
	}
}

func TestLoggerSizeBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "before.log")
	lg, err := NewLoggerAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	// current_size + len(line) > 10MiB even if current_size itself is still below the cap
	lg.mu.Lock()
	lg.size = maxLogBytes - 10
	lg.mu.Unlock()
	lg.Log("this line is longer than ten bytes so it must rotate first")
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "log rotated") {
		t.Fatalf("expected rotate-before-write, got %q", text)
	}
	if !strings.Contains(text, "this line is longer") {
		t.Fatalf("expected new line after truncate, got %q", text)
	}
	if strings.Contains(text, "hello") {
		t.Fatal("old content should have been truncated")
	}
}
