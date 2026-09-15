package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type cliCommand int

const (
	cmdDaemon cliCommand = iota
	cmdCheck
	cmdStatus
	cmdHelp
)

func parseCommand(args []string) (cliCommand, error) {
	if len(args) == 0 {
		return cmdDaemon, nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "check":
		return cmdCheck, nil
	case "status":
		return cmdStatus, nil
	case "help", "-h", "--help":
		return cmdHelp, nil
	default:
		return 0, fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `PnP Device Recovery — Windows PnP device auto-recovery

Usage:
  PnP-Device-Recovery.exe              Run as daemon (single instance)
  PnP-Device-Recovery.exe check        Clear Exhausted and rescan (IPC or one-shot)
  PnP-Device-Recovery.exe status       Print daemon status
  PnP-Device-Recovery.exe help         Show this help

Config: config.json next to the executable (log / devices / friendly_name / max_retries / rec_delay / ProblemCode / optional advanced).
`)
}

func resolveConfigPath() string {
	name := defaultConfigName
	if _, err := os.Stat(name); err == nil {
		return name
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return name
}

func printStatus(s *StatusSnapshot) {
	if s == nil {
		fmt.Println("PnP Device Recovery: no status")
		return
	}
	fmt.Printf("phase: %s\n", s.Phase)
	fmt.Printf("listener_registered: %v\n", s.ListenerRegistered)
	if len(s.Devices) == 0 {
		fmt.Println("devices: (none)")
		return
	}
	for _, d := range s.Devices {
		fmt.Println("device:")
		fmt.Printf("  instance: %s\n", d.Instance)
		fmt.Printf("  friendly_name: %s\n", d.FriendlyName)
		fmt.Printf("  healthy: %v\n", d.Healthy)
		fmt.Printf("  problem: %d\n", d.Problem)
		fmt.Printf("  state: %s\n", d.State)
		fmt.Printf("  attempt: %d\n", d.Attempt)
		fmt.Printf("  max_retries: %d\n", d.MaxRetries)
	}
}
