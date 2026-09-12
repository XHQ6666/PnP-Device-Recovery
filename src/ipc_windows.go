//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	ipcPipeName             = `\\.\pipe\PnPDeviceRecovery`
	pipeRejectRemoteClients = 0x00000008
	ipcClientRetries        = 8
	ipcClientRetryDelay     = 50 * time.Millisecond
)

type pipeSlot struct {
	mu sync.Mutex
	h  windows.Handle
}

func (s *pipeSlot) take(h windows.Handle) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

func (s *pipeSlot) releaseWithoutClose() {
	s.mu.Lock()
	s.h = 0
	s.mu.Unlock()
}

func (s *pipeSlot) close() {
	s.mu.Lock()
	h := s.h
	s.h = 0
	s.mu.Unlock()
	if h != 0 {
		_ = windows.CloseHandle(h)
	}
}

// StartIPCServer accepts local named-pipe JSON commands until ctx is cancelled.
func StartIPCServer(ctx context.Context, handler func(IPCRequest) IPCResponse, logger *Logger) {
	slot := &pipeSlot{}
	go func() {
		<-ctx.Done()
		slot.close()
	}()
	go func() {
		for ctx.Err() == nil {
			if err := serveOnePipe(ctx, handler, logger, slot); err != nil {
				if ctx.Err() != nil {
					return
				}
				if logger != nil {
					logger.Warnf("IPC pipe: %v", err)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

func serveOnePipe(ctx context.Context, handler func(IPCRequest) IPCResponse, logger *Logger, slot *pipeSlot) error {
	name, err := windows.UTF16PtrFromString(ipcPipeName)
	if err != nil {
		return err
	}
	h, err := windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|pipeRejectRemoteClients,
		windows.PIPE_UNLIMITED_INSTANCES,
		8192,
		8192,
		1000,
		nil,
	)
	if err != nil {
		return fmt.Errorf("CreateNamedPipe: %w", err)
	}
	slot.take(h)

	err = windows.ConnectNamedPipe(h, nil)
	if err != nil && err != windows.ERROR_PIPE_CONNECTED {
		slot.close()
		return err
	}
	slot.releaseWithoutClose()
	if ctx.Err() != nil {
		_ = windows.CloseHandle(h)
		return ctx.Err()
	}

	f := os.NewFile(uintptr(h), "pipe")
	defer f.Close()

	var req IPCRequest
	if err := readJSONLine(f, &req); err != nil {
		return fmt.Errorf("read IPC: %w", err)
	}
	if err := validateIPCRequest(req); err != nil {
		_ = writeJSONLine(f, IPCResponse{OK: false, Error: err.Error()})
		return nil
	}
	resp := handler(req)
	if err := writeJSONLine(f, resp); err != nil {
		return fmt.Errorf("write IPC: %w", err)
	}
	_ = logger
	return nil
}

// CallIPC connects to the local daemon pipe. Remote clients are rejected by the server.
func CallIPC(req IPCRequest) (IPCResponse, error) {
	if err := validateIPCRequest(req); err != nil {
		return IPCResponse{}, err
	}
	name, err := windows.UTF16PtrFromString(ipcPipeName)
	if err != nil {
		return IPCResponse{}, err
	}

	var last error
	for i := 0; i < ipcClientRetries; i++ {
		h, err := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err == nil {
			f := os.NewFile(uintptr(h), "pipe")
			defer f.Close()
			if err := writeJSONLine(f, req); err != nil {
				return IPCResponse{}, err
			}
			var resp IPCResponse
			if err := readJSONLine(f, &resp); err != nil {
				return IPCResponse{}, err
			}
			return resp, nil
		}
		last = err
		if err == windows.ERROR_FILE_NOT_FOUND {
			return IPCResponse{}, ErrDaemonNotRunning
		}
		if err == windows.ERROR_PIPE_BUSY {
			time.Sleep(ipcClientRetryDelay)
			continue
		}
		return IPCResponse{}, err
	}
	if last == windows.ERROR_PIPE_BUSY {
		return IPCResponse{}, fmt.Errorf("IPC pipe busy: %w", last)
	}
	return IPCResponse{}, last
}
