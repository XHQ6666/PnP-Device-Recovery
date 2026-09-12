package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIPCRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSONLine(&buf, IPCRequest{Cmd: "status"}); err != nil {
		t.Fatal(err)
	}
	var req IPCRequest
	if err := readJSONLine(&buf, &req); err != nil {
		t.Fatal(err)
	}
	if req.Cmd != "status" {
		t.Fatalf("cmd=%q", req.Cmd)
	}
}

func TestIPCRejectsInvalidJSON(t *testing.T) {
	err := readJSONLine(strings.NewReader("not-json\n"), &IPCRequest{})
	if err == nil {
		t.Fatal("expected invalid JSON")
	}
}

func TestIPCRejectsEmpty(t *testing.T) {
	err := readJSONLine(strings.NewReader("\n"), &IPCRequest{})
	if err == nil {
		t.Fatal("expected empty message error")
	}
}

func TestCallIPCNoDaemon(t *testing.T) {
	_, err := CallIPC(IPCRequest{Cmd: "status"})
	if err != ErrDaemonNotRunning {
		t.Fatalf("got %v", err)
	}
}
