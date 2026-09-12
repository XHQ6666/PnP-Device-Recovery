//go:build !windows

package main

import (
	"context"
	"fmt"
)

// stubListener is used on non-Windows platforms.
type stubListener struct {
	logger  *Logger
	started bool
}

// NewPnPListener returns a stub on non-Windows (real: CM_Register_Notification / WM_DEVICECHANGE).
func NewPnPListener(logger *Logger, onEvent func(PnPEvent)) (PnPListener, error) {
	_ = onEvent
	return &stubListener{logger: logger}, nil
}

func (s *stubListener) Start(ctx context.Context) error {
	s.started = true
	s.logger.Warnf("PnP listener is a stub on this platform (Windows-only event-driven monitor)")
	<-ctx.Done()
	return ctx.Err()
}

func (s *stubListener) Registered() bool {
	return s.started
}

func (s *stubListener) Stop() error {
	return nil
}

var _ PnPListener = (*stubListener)(nil)

// ErrPnPUnsupported is returned when PnP APIs are unavailable.
var ErrPnPUnsupported = fmt.Errorf("PnP notifications are only available on Windows")
