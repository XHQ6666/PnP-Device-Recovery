//go:build !windows

package main

import "context"

// StartIPCServer is a no-op on non-Windows (CLI status prints not-running).
func StartIPCServer(ctx context.Context, handler func(IPCRequest) IPCResponse, logger *Logger) {
	_ = ctx
	_ = handler
	_ = logger
}

// CallIPC always reports the daemon as not running on non-Windows stubs.
func CallIPC(req IPCRequest) (IPCResponse, error) {
	_ = req
	return IPCResponse{}, ErrDaemonNotRunning
}
