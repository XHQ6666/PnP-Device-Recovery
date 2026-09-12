package main

import (
	"context"
	"testing"
	"time"
)

func TestWaitForSystemReadyStub(t *testing.T) {
	start := time.Now()
	if err := WaitForSystemReady(context.Background(), testLogger(t)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Linux stub should return immediately")
	}
}
