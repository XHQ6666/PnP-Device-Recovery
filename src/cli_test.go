package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		args    []string
		want    cliCommand
		wantErr bool
	}{
		{nil, cmdDaemon, false},
		{[]string{}, cmdDaemon, false},
		{[]string{"check"}, cmdCheck, false},
		{[]string{"CHECK"}, cmdCheck, false},
		{[]string{"status"}, cmdStatus, false},
		{[]string{"help"}, cmdHelp, false},
		{[]string{"-h"}, cmdHelp, false},
		{[]string{"--help"}, cmdHelp, false},
		{[]string{"foo"}, 0, true},
		{[]string{"-x"}, 0, true},
		{[]string{"config.json"}, 0, true},
	}
	for _, c := range cases {
		got, err := parseCommand(c.args)
		if c.wantErr {
			if err == nil {
				t.Fatalf("args=%v: expected error", c.args)
			}
			continue
		}
		if err != nil {
			t.Fatalf("args=%v: %v", c.args, err)
		}
		if got != c.want {
			t.Fatalf("args=%v: got %v want %v", c.args, got, c.want)
		}
	}
}

func TestPrintUsage(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	s := buf.String()
	for _, need := range []string{"check", "status", "help", "config.json"} {
		if !strings.Contains(s, need) {
			t.Fatalf("usage missing %q:\n%s", need, s)
		}
	}
}

func TestValidateIPCRequest(t *testing.T) {
	if err := validateIPCRequest(IPCRequest{Cmd: "check"}); err != nil {
		t.Fatal(err)
	}
	if err := validateIPCRequest(IPCRequest{Cmd: "status"}); err != nil {
		t.Fatal(err)
	}
	if err := validateIPCRequest(IPCRequest{Cmd: "help"}); err == nil {
		t.Fatal("help is not a valid IPC cmd")
	}
	if err := validateIPCRequest(IPCRequest{Cmd: ""}); err == nil {
		t.Fatal("empty cmd should fail")
	}
}
