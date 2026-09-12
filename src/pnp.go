package main

import "context"

// PnPEvent is a device arrival/removal/problem notification.
type PnPEvent struct {
	Kind       string // "arrival", "removal", "problem", "change", ...
	InstanceID string
	DevInst    uint32
}

// PnPListener is the event-driven PnP monitor interface.
type PnPListener interface {
	Start(ctx context.Context) error
	Stop() error
	Registered() bool
}
