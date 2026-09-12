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
	lg, err := NewLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	lg.Log("hello %s", "world")
	lg.Infof("info line")
	lg.Warnf("warn line")
	lg.Errorf("err line")

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
}

func TestLoggerUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf8.log")
	lg, err := NewLogger(path)
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
