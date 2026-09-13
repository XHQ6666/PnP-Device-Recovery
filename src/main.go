package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultConfigName = "config.json"

func main() {
	os.Exit(run())
}

func run() int {
	cmd, err := parseCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err)
		printUsage(os.Stderr)
		return 1
	}
	switch cmd {
	case cmdHelp:
		printUsage(os.Stdout)
		return 0
	case cmdStatus:
		return runStatus()
	case cmdCheck:
		return runCheck()
	case cmdDaemon:
		return runDaemon()
	default:
		printUsage(os.Stderr)
		return 1
	}
}

func runStatus() int {
	resp, err := CallIPC(IPCRequest{Cmd: "status"})
	if err != nil {
		if errors.Is(err, ErrDaemonNotRunning) {
			fmt.Println("PnP Device Recovery: not running")
			return 0
		}
		fmt.Fprintf(os.Stderr, "status error: %v\n", err)
		return 1
	}
	printStatus(resp.Status)
	return 0
}

func runCheck() int {
	resp, err := CallIPC(IPCRequest{Cmd: "check"})
	if err == nil {
		printStatus(resp.Status)
		return 0
	}
	if !errors.Is(err, ErrDaemonNotRunning) {
		fmt.Fprintf(os.Stderr, "check IPC error: %v\n", err)
		return 1
	}
	return runOneShotCheck()
}

func runOneShotCheck() int {
	cfgPath := resolveConfigPath()
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}
	logger, err := NewLogger(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		return 1
	}
	defer logger.Close()

	logger.Infof("one-shot check (daemon not running); config=%s", cfgPath)
	if err := EnsureElevated(logger); err != nil {
		if errors.Is(err, ErrNotElevated) {
			logger.Errorf("%v", err)
			return 1
		}
		logger.Errorf("privilege check: %v", err)
		return 1
	}

	// Same singleton as the daemon so concurrent one-shot checks cannot overlap.
	release, err := AcquireSingleton()
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			logger.Errorf("%v", err)
			fmt.Fprintf(os.Stderr, "another instance is already running\n")
			return 1
		}
		logger.Errorf("singleton: %v", err)
		return 1
	}
	defer release()

	matchers, err := BuildMatchers(cfg.Devices)
	if err != nil {
		logger.Errorf("matcher build failed: %v", err)
		return 1
	}

	rm := NewRecoveryManager(cfg, matchers, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rm.Scan(ctx, ScanReason{Reason: "check", IgnoreDelay: true})
	rm.Wait()
	printStatus(&StatusSnapshot{
		Phase:              "ONE_SHOT",
		ListenerRegistered: false,
		Devices:            rm.DeviceStatuses(),
	})
	return 0
}

func runDaemon() int {
	cfgPath := resolveConfigPath()
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}

	logger, err := NewLogger(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		return 1
	}
	defer logger.Close()

	logger.Infof("PnP Device Recovery daemon starting; config=%s", cfgPath)

	// Elevate before taking the singleton so a non-elevated parent does not
	// hold the mutex while the elevated child starts.
	if err := EnsureElevated(logger); err != nil {
		if errors.Is(err, ErrNotElevated) {
			logger.Errorf("%v", err)
			return 1
		}
		logger.Errorf("privilege check: %v", err)
		return 1
	}

	release, err := AcquireSingleton()
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			logger.Errorf("%v", err)
			fmt.Fprintf(os.Stderr, "another instance is already running\n")
			return 1
		}
		logger.Errorf("singleton: %v", err)
		return 1
	}
	defer release()

	matchers, err := BuildMatchers(cfg.Devices)
	if err != nil {
		logger.Errorf("matcher build failed: %v", err)
		return 1
	}

	d := NewDaemon(cfg, matchers, logger)
	d.SetPhase(PhaseStarting)

	StartIPCServer(d.ctx, d.HandleIPC, logger)

	listener, err := NewPnPListener(logger, d.OnPnPEvent)
	if err != nil {
		logger.Errorf("PnP listener create failed: %v", err)
		return 1
	}
	d.listener = listener

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- listener.Start(d.ctx)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			logger.Infof("shutdown signal received: %v", sig)
			d.cancel()
		case err := <-listenErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Errorf("PnP listener exited: %v", err)
			}
			d.cancel()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if listener.Registered() {
			break
		}
		if d.ctx.Err() != nil {
			return d.Shutdown()
		}
		time.Sleep(50 * time.Millisecond)
	}
	d.SetListenerRegistered(listener.Registered())
	logger.Infof("REGISTER_PNP_NOTIFICATION: registered=%v", listener.Registered())

	d.SetPhase(PhaseWaitForSystemReady)
	if err := WaitForSystemReady(d.ctx, logger); err != nil {
		logger.Infof("ready wait ended: %v", err)
		return d.Shutdown()
	}

	logger.Infof("INITIAL_SCAN reason=startup")
	d.rm.Scan(d.ctx, ScanReason{Reason: "startup"})
	d.ClearPendingStartup()

	d.SetPhase(PhaseNormalRunning)
	logger.Infof("event-driven PnP monitor running with trailing-edge coalescing")

	<-d.ctx.Done()
	return d.Shutdown()
}
