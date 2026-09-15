package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultMaxLogBytes = 10 * 1024 * 1024 // 10 MiB — default overwrite/truncate threshold

// Logger writes UTF-8 log lines with size-based rotation (truncate when > maxBytes).
// When disabled, all methods are no-ops (no file writes and no console output).
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	size     int64
	maxBytes int64
	enabled  bool
	debug    bool
}

// NewLogger opens a logger from LogConfig. Relative paths resolve against the exe directory.
// When Enabled is false, no file is opened and methods no-op.
func NewLogger(cfg *LogConfig) (*Logger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("log config is required")
	}
	level := strings.ToLower(strings.TrimSpace(cfg.Level))
	debug := level == LogLevelDebug
	if !cfg.Enabled {
		return &Logger{enabled: false, debug: debug, maxBytes: defaultMaxLogBytes}, nil
	}
	path, err := ResolvePathRelativeToExe(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve log path: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to stat log file %s: %w", path, err)
	}
	return &Logger{file: f, path: path, size: info.Size(), maxBytes: defaultMaxLogBytes, enabled: true, debug: debug}, nil
}

// SetMaxBytes sets the rotation threshold (from advanced.log_max_bytes).
// Non-positive values are ignored (keep current / default).
func (l *Logger) SetMaxBytes(n int64) {
	if l == nil || n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxBytes = n
}

// NewLoggerAtPath is a test helper: enabled normal-level logger at an absolute path.
func NewLoggerAtPath(path string) (*Logger, error) {
	return NewLogger(&LogConfig{Enabled: true, Level: LogLevelNormal, Path: path})
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// rotateIfNeededLocked truncates when current_size + nextBytes would exceed maxBytes.
func (l *Logger) rotateIfNeededLocked(nextBytes int) error {
	limit := l.maxBytes
	if limit <= 0 {
		limit = defaultMaxLogBytes
	}
	if l.size+int64(nextBytes) <= limit {
		return nil
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		l.file = nil
		return fmt.Errorf("failed to truncate log file %s: %w", l.path, err)
	}
	l.file = f
	l.size = 0
	header := fmt.Sprintf("[%s] === log rotated (exceeded %d bytes) ===\n",
		time.Now().Format("2006-01-02 15:04:05.000"), limit)
	n, err := l.file.WriteString(header)
	l.size += int64(n)
	return err
}

func (l *Logger) writeLine(line string) {
	if l == nil || !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		fmt.Print(line)
		return
	}
	if err := l.rotateIfNeededLocked(len(line)); err != nil {
		fmt.Fprintf(os.Stderr, "log rotate error: %v\n", err)
	}
	n, err := l.file.WriteString(line)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log write error: %v\n", err)
	}
	l.size += int64(n)
	_ = l.file.Sync()
	fmt.Print(line)
}

// Log writes a timestamped line to the log file and stdout (no-op when disabled).
func (l *Logger) Log(format string, args ...interface{}) {
	if l == nil || !l.enabled {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)
	l.writeLine(line)
}

// Infof logs at normal level (and debug).
func (l *Logger) Infof(format string, args ...interface{}) { l.Log(format, args...) }

// Debugf logs only when log.level is debug.
func (l *Logger) Debugf(format string, args ...interface{}) {
	if l == nil || !l.enabled || !l.debug {
		return
	}
	l.Log("DEBUG: "+format, args...)
}

// Errorf logs an error-prefixed message.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.Log("ERROR: "+format, args...)
}

// Warnf logs a warning-prefixed message.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.Log("WARN: "+format, args...)
}

// Enabled reports whether logging is active.
func (l *Logger) Enabled() bool {
	return l != nil && l.enabled
}

// DebugEnabled reports whether debug-level logging is active.
func (l *Logger) DebugEnabled() bool {
	return l != nil && l.enabled && l.debug
}
