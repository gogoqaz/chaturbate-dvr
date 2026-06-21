package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type activeWorkStub struct {
	active bool
}

func (s activeWorkStub) HasActiveWork() bool {
	return s.active
}

func TestShutdownGuardAllowsExitWhenNoActiveWork(t *testing.T) {
	var out bytes.Buffer
	guard := newShutdownGuard(activeWorkStub{}, &out)

	if !guard.ShouldExit() {
		t.Fatal("ShouldExit() = false, want true when no recording or compression is active")
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty output", out.String())
	}
}

func TestShutdownGuardRequiresSecondSignalWhenActiveWorkExists(t *testing.T) {
	var out bytes.Buffer
	guard := newShutdownGuard(activeWorkStub{active: true}, &out)
	now := time.Date(2026, 6, 10, 3, 13, 0, 0, time.UTC)
	guard.now = func() time.Time { return now }

	if guard.ShouldExit() {
		t.Fatal("first ShouldExit() = true, want false while recording or compression is active")
	}
	if !strings.Contains(out.String(), "Recording or compression is still running") {
		t.Fatalf("output = %q, want active-work warning", out.String())
	}

	if !guard.ShouldExit() {
		t.Fatal("second ShouldExit() = false, want true after confirmation signal")
	}
}

func TestShutdownGuardRewarnsAfterConfirmationWindow(t *testing.T) {
	var out bytes.Buffer
	guard := newShutdownGuard(activeWorkStub{active: true}, &out)
	now := time.Date(2026, 6, 10, 3, 13, 0, 0, time.UTC)
	guard.now = func() time.Time { return now }

	if guard.ShouldExit() {
		t.Fatal("first ShouldExit() = true, want false")
	}
	now = now.Add(11 * time.Second)
	if guard.ShouldExit() {
		t.Fatal("ShouldExit() after timeout = true, want another warning before exit")
	}
}
