package app

import (
	"context"
	"io"
	"net"
	"os"
	"os/signal"
	"time"
)

type checkDeps struct {
	stdout              io.Writer
	stderr              io.Writer
	lookupIPAddr        func(ctx context.Context, host string) ([]net.IPAddr, error)
	dialTarget          func(network, address string, timeout time.Duration) (float64, error)
	now                 func() time.Time
	after               func(time.Duration) <-chan time.Time
	createFile          func(name string, mode os.FileMode) (io.WriteCloser, error)
	enableConsoleColors func()
	setupInterrupt      func() (<-chan os.Signal, func())
}

func realCheckDeps() checkDeps {
	return checkDeps{
		stdout:       os.Stdout,
		stderr:       os.Stderr,
		lookupIPAddr: net.DefaultResolver.LookupIPAddr,
		dialTarget:   dialTarget,
		now:          time.Now,
		after:        time.After,
		createFile: func(name string, mode os.FileMode) (io.WriteCloser, error) {
			return createOutputFile(name, mode)
		},
		enableConsoleColors: enableConsoleColors,
		setupInterrupt:      setupInterrupt,
	}
}

func setupInterrupt() (<-chan os.Signal, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	return sig, func() {
		signal.Stop(sig)
		close(sig)
	}
}

func fillCheckDeps(deps checkDeps) checkDeps {
	if deps.stdout == nil {
		deps.stdout = os.Stdout
	}
	if deps.stderr == nil {
		deps.stderr = os.Stderr
	}
	if deps.lookupIPAddr == nil {
		deps.lookupIPAddr = net.DefaultResolver.LookupIPAddr
	}
	if deps.dialTarget == nil {
		deps.dialTarget = dialTarget
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.after == nil {
		deps.after = time.After
	}
	if deps.createFile == nil {
		deps.createFile = func(name string, mode os.FileMode) (io.WriteCloser, error) {
			return createOutputFile(name, mode)
		}
	}
	if deps.enableConsoleColors == nil {
		deps.enableConsoleColors = enableConsoleColors
	}
	if deps.setupInterrupt == nil {
		deps.setupInterrupt = setupInterrupt
	}
	return deps
}
