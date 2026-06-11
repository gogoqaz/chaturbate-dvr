package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const shutdownConfirmWindow = 10 * time.Second

type activeWorkSource interface {
	HasActiveWork() bool
}

type shutdownGuard struct {
	source       activeWorkSource
	out          io.Writer
	now          func() time.Time
	confirmBy    time.Time
	warnedActive bool
}

func newShutdownGuard(source activeWorkSource, out io.Writer) *shutdownGuard {
	return &shutdownGuard{
		source: source,
		out:    out,
		now:    time.Now,
	}
}

func (g *shutdownGuard) ShouldExit() bool {
	if g.source == nil || !g.source.HasActiveWork() {
		return true
	}

	now := g.now()
	if g.warnedActive && now.Before(g.confirmBy) {
		return true
	}

	g.warnedActive = true
	g.confirmBy = now.Add(shutdownConfirmWindow)
	fmt.Fprintln(g.out, "Recording or compression is still running. Send Ctrl+C again within 10 seconds to exit anyway.")
	return false
}

func installShutdownGuard(source activeWorkSource) {
	guard := newShutdownGuard(source, os.Stderr)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for sig := range signals {
			if guard.ShouldExit() {
				signal.Stop(signals)
				os.Exit(signalExitCode(sig))
			}
		}
	}()
}

func signalExitCode(sig os.Signal) int {
	if sig == os.Interrupt {
		return 130
	}
	return 143
}
