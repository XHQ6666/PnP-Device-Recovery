package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const ipcMaxBytes = 64 * 1024

// ErrDaemonNotRunning is returned when check/status cannot reach a daemon.
var ErrDaemonNotRunning = fmt.Errorf("daemon not running")

// IPCRequest is a local JSON command sent over the named pipe / stub.
type IPCRequest struct {
	Cmd string `json:"cmd"`
}

// DeviceStatus is one configured device row in a status dump.
type DeviceStatus struct {
	Instance     string `json:"instance"`
	FriendlyName string `json:"friendly_name"`
	Healthy      bool   `json:"healthy"`
	Problem      uint32 `json:"problem"`
	State        string `json:"state"`
	Attempt      int    `json:"attempt"`
	MaxRetries   int    `json:"max_retries"`
}

// StatusSnapshot is the daemon status payload (CLI + IPC).
type StatusSnapshot struct {
	Phase              string         `json:"phase"`
	ListenerRegistered bool           `json:"listener_registered"`
	Devices            []DeviceStatus `json:"devices"`
}

// IPCResponse is the daemon reply.
type IPCResponse struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Status *StatusSnapshot `json:"status,omitempty"`
}

func validateIPCRequest(req IPCRequest) error {
	switch req.Cmd {
	case "check", "status":
		return nil
	default:
		return fmt.Errorf("invalid cmd %q", req.Cmd)
	}
}

func writeJSONLine(w io.Writer, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > ipcMaxBytes {
		return fmt.Errorf("IPC payload too large")
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readJSONLine(r io.Reader, v interface{}) error {
	reader := bufio.NewReader(io.LimitReader(r, ipcMaxBytes+2))
	line, err := reader.ReadBytes('\n')
	if err != nil && !(err == io.EOF && len(line) > 0) {
		return err
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return fmt.Errorf("empty IPC message")
	}
	if !json.Valid(line) {
		return fmt.Errorf("invalid IPC JSON")
	}
	return json.Unmarshal(line, v)
}
