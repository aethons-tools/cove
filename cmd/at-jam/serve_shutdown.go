package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// buildShutdownWait bounds how long serve waits for cancelled kit draft
// builds to stop before it exits.
const buildShutdownWait = 15 * time.Second

// shutdownSignals delivers SIGTERM and SIGINT, which serve handles
// (stopBuildsOnSignal). A SIGINT ignored when serve started (a shell's
// background job) stays ignored.
func shutdownSignals() <-chan os.Signal {
	c := make(chan os.Signal, 2)
	sigs := []os.Signal{syscall.SIGTERM}
	if !signal.Ignored(os.Interrupt) {
		sigs = append(sigs, os.Interrupt)
	}
	signal.Notify(c, sigs...)
	return c
}

// stopBuildsOnSignal waits for a shutdown signal, then cancels the kit draft
// builds (stop; each docker child is interrupted) and waits up to
// buildShutdownWait for them to stop; a second signal ends the wait at once.
// It then calls die with the last signal (dieBySignal in serve). builds is
// nil without a launcher that builds: die then follows the signal at once.
func stopBuildsOnSignal(sigs <-chan os.Signal, stop context.CancelFunc, builds *jam.KitDraftBuilds, log *slog.Logger, die func(os.Signal)) {
	sig := <-sigs
	stop()
	if builds != nil {
		log.Info("serve: stopping kit draft builds", "signal", sig.String())
		stopped := make(chan bool, 1)
		go func() { stopped <- builds.Wait(buildShutdownWait) }()
		select {
		case ok := <-stopped:
			if !ok {
				log.Warn("kit draft builds still running at shutdown", "waited", buildShutdownWait.String())
			}
		case sig = <-sigs:
			log.Warn("serve: second signal, not waiting for kit draft builds", "signal", sig.String())
		}
	}
	die(sig)
}

// dieBySignal restores sig's default handling and raises it again, so serve
// ends killed by sig, as it did before it handled SIGINT and SIGTERM (a
// shell reports 130 or 143). The exit below runs only should the signal not
// kill the process.
func dieBySignal(sig os.Signal) {
	signal.Reset(sig)
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), s)
		time.Sleep(5 * time.Second)
	}
	os.Exit(1)
}
