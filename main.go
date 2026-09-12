package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const defaultConfigName = "config.json"

func main() {
	os.Exit(run())
}

func run() int {
	cfgPath := defaultConfigName
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	// Resolve config next to executable if relative and not found in CWD.
	if !filepath.IsAbs(cfgPath) {
		if _, err := os.Stat(cfgPath); err != nil {
			if exe, err2 := os.Executable(); err2 == nil {
				cand := filepath.Join(filepath.Dir(exe), cfgPath)
				if _, err3 := os.Stat(cand); err3 == nil {
					cfgPath = cand
				}
			}
		}
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}

	logger, err := NewLogger(cfg.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		return 1
	}
	defer logger.Close()

	logger.Infof("PnP Device Recovery starting; config=%s", cfgPath)

	if err := EnsureElevated(logger); err != nil {
		if errors.Is(err, ErrNotElevated) {
			logger.Errorf("%v", err)
			return 1
		}
		logger.Errorf("privilege check: %v", err)
		return 1
	}

	matchers, err := BuildMatchers(cfg.Devices)
	if err != nil {
		logger.Errorf("matcher build failed: %v", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rm := NewRecoveryManager(cfg, matchers, logger)

	var eventMu sync.Mutex
	handlePnP := func(ev PnPEvent) {
		logger.Infof("PnP worker handling event: kind=%s instance=%q DEVINST=%d", ev.Kind, ev.InstanceID, ev.DevInst)

		eventMu.Lock()
		defer eventMu.Unlock()

		if ev.InstanceID != "" {
			info, err := GetDeviceByInstanceID(ev.InstanceID)
			if err != nil {
				logger.Warnf("GetDeviceByInstanceID(%s): %v — scanning all devices", ev.InstanceID, err)
			} else if info != nil {
				m, _ := rm.FindMatcher(info.FriendlyName)
				if m != nil {
					rm.HandleDeviceProblem(ctx, info)
					return
				}
			}
		}

		// For nodes_changed / unknown instance: scan matched devices for problems.
		devices, err := EnumerateDevices()
		if err != nil {
			logger.Errorf("enumerate on PnP event failed: %v", err)
			return
		}
		for i := range devices {
			dev := &devices[i]
			m, _ := rm.FindMatcher(dev.FriendlyName)
			if m == nil {
				continue
			}
			logger.Infof("PnP scan match: name=%q instance=%q status=0x%X problem=%d healthy=%v",
				dev.FriendlyName, dev.InstanceID, dev.Status, dev.ProblemCode, dev.IsHealthy())
			if !dev.IsHealthy() {
				rm.HandleDeviceProblem(ctx, dev)
			}
		}
	}

	listener, err := NewPnPListener(logger, handlePnP)
	if err != nil {
		logger.Errorf("PnP listener create failed: %v", err)
		return 1
	}

	// Startup scan (missing devices logged; keep listening).
	rm.ScanAndRecoverMissing(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- listener.Start(ctx)
	}()

	logger.Infof("event-driven PnP monitor running; waiting for device events or signal")

	select {
	case sig := <-sigCh:
		logger.Infof("shutdown signal received: %v", sig)
	case err := <-listenErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Errorf("PnP listener exited: %v", err)
		}
	}

	// Ordered shutdown: stop new recovery → unregister PnP → wait workers → close log.
	logger.Infof("shutting down: stop new recovery sessions")
	rm.StopNewWork()
	cancel()

	logger.Infof("unregistering PnP listener")
	_ = listener.Stop()

	logger.Infof("waiting for recovery workers")
	done := make(chan struct{})
	go func() {
		rm.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		logger.Warnf("timeout waiting for recovery workers")
	}

	logger.Infof("PnP Device Recovery stopped cleanly")
	return 0
}
