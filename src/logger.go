package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const maxLogBytes = 10 * 1024 * 1024 // 10 MiB — overwrite/truncate when exceeded

// Logger writes UTF-8 log lines with size-based rotation (truncate when > maxLogBytes).
type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
}

// NewLogger opens or creates the log file at path.
func NewLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to stat log file %s: %w", path, err)
	}
	return &Logger{file: f, path: path, size: info.Size()}, nil
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// rotateIfNeededLocked truncates when current_size + nextBytes would exceed 10 MiB.
func (l *Logger) rotateIfNeededLocked(nextBytes int) error {
	if l.size+int64(nextBytes) <= maxLogBytes {
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
		time.Now().Format("2006-01-02 15:04:05.000"), maxLogBytes)
	n, err := l.file.WriteString(header)
	l.size += int64(n)
	return err
}

// Log writes a timestamped line to the log file and stdout.
func (l *Logger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)

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

// Infof is an alias for Log.
func (l *Logger) Infof(format string, args ...interface{}) { l.Log(format, args...) }

// Errorf logs an error-prefixed message.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.Log("ERROR: "+format, args...)
}

// Warnf logs a warning-prefixed message.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.Log("WARN: "+format, args...)
}
