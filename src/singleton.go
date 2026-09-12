package main

import "fmt"

// ErrAlreadyRunning is returned when the single-instance lock is held.
var ErrAlreadyRunning = fmt.Errorf("another instance is already running")
