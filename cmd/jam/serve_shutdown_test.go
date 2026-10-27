package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// blockingDraftBuilder's build signals started, then waits for release
// and, unless ignoreCtx, for its context.
type blockingDraftBuilder struct {
	started   chan struct{}
	release   chan struct{}
	ignoreCtx bool
}

func (b blockingDraftBuilder) BuildKitDraft(ctx context.Context, _ jam.KitDefinition, _ string, _ io.Writer) (string, error) {
	close(b.started)
	if b.ignoreCtx {
		<-b.release
		return "", ctx.Err()
	}
	select {
	case <-b.release:
		return "sha256:img", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (blockingDraftBuilder) HasKitDraftImage(string) (bool, error) { return false, nil }

// runningBuild is kit app's draft building over bb under a cancellable
// build context, its cancel, and the builds.
func runningBuild(t *testing.T, bb blockingDraftBuilder) (*jam.KitDraftBuilds, context.CancelFunc) {
	t.Helper()
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	builds := jam.NewKitDraftBuilds(ctx, store, jam.OneDraftManager(jam.LocalManager, bb), slog.New(slog.DiscardHandler))
	if _, err := builds.Start("app", jam.DraftETag(d.Tree), "op", ""); err != nil {
		t.Fatal(err)
	}
	<-bb.started
	return builds, stop
}

// died is what a fake die saw: the signal and, at that moment, kit app's build.
type died struct {
	sig   os.Signal
	build jam.KitDraftBuild
}

// Review Focus 2: SIGTERM cancels a running build, and serve waits for it
// to stop before it dies by the signal.
func TestShutdownCancelsDraftBuilds(t *testing.T) {
	bb := blockingDraftBuilder{started: make(chan struct{}), release: make(chan struct{})}
	builds, stop := runningBuild(t, bb)
	sigs := make(chan os.Signal)
	got := make(chan died, 1)
	go stopBuildsOnSignal(sigs, stop, builds, slog.New(slog.DiscardHandler), func(s os.Signal) {
		v, _ := builds.Status("app", "", 0)
		got <- died{s, v}
	})
	sigs <- syscall.SIGTERM
	select {
	case d := <-got:
		if d.sig != syscall.SIGTERM {
			t.Fatalf("died by %v, want SIGTERM", d.sig)
		}
		if d.build.State != jam.DraftBuildFailed || d.build.Error != "cancelled: Jam is shutting down" {
			t.Fatalf("build at exit = %+v, want it stopped by the shutdown", d.build)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
}

// A second signal while builds are stopping exits at once (well before
// buildShutdownWait), by that signal.
func TestShutdownSecondSignalExitsAtOnce(t *testing.T) {
	bb := blockingDraftBuilder{started: make(chan struct{}), release: make(chan struct{}), ignoreCtx: true}
	builds, stop := runningBuild(t, bb)
	defer close(bb.release)
	sigs := make(chan os.Signal)
	got := make(chan died, 1)
	go stopBuildsOnSignal(sigs, stop, builds, slog.New(slog.DiscardHandler), func(s os.Signal) {
		v, _ := builds.Status("app", "", 0)
		got <- died{s, v}
	})
	sigs <- syscall.SIGTERM
	sigs <- os.Interrupt // received only by the wait for the builds
	select {
	case d := <-got:
		if d.sig != os.Interrupt || d.build.State != jam.DraftBuildRunning {
			t.Fatalf("died by %v with build %s, want SIGINT while it still ran", d.sig, d.build.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal did not exit")
	}
}

// Without a launcher (no builds) the first signal exits at once.
func TestShutdownWithoutBuildsExits(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	got := make(chan os.Signal, 1)
	go stopBuildsOnSignal(sigs, func() { close(stopped) }, nil, slog.New(slog.DiscardHandler), func(s os.Signal) { got <- s })
	sigs <- os.Interrupt
	select {
	case s := <-got:
		if s != os.Interrupt {
			t.Fatalf("died by %v, want SIGINT", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
	<-stopped
}

// shutdownChild runs serve's real handler in this test binary re-run as a
// child (JAM_SHUTDOWN_CHILD=mode): stop appends "stopped;" and die the
// signal's name to the marker file (JAM_SHUTDOWN_MARKER) before
// dieBySignal. Mode "term" sends itself SIGTERM; "ignored-int" ignores
// SIGINT first (as a shell's background job does), then sends SIGINT and
// SIGTERM. It never returns.
func shutdownChild(mode string) {
	marker := os.Getenv("JAM_SHUTDOWN_MARKER")
	mark := func(s string) {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(s)
			_ = f.Close()
		}
	}
	if mode == "ignored-int" {
		signal.Ignore(os.Interrupt)
	}
	sigs := shutdownSignals() // installed before any signal is sent
	go stopBuildsOnSignal(sigs, func() { mark("stopped;") }, nil, slog.New(slog.DiscardHandler), func(s os.Signal) {
		mark(s.String())
		dieBySignal(s)
	})
	if mode == "ignored-int" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	time.Sleep(10 * time.Second)
	os.Exit(3) // not reached when the signal killed us
}

// runShutdownChild runs test (re-run as a child in mode) and returns the
// signal that killed it and its marker file's text.
func runShutdownChild(t *testing.T, test, mode string) (syscall.Signal, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), "JAM_SHUTDOWN_CHILD="+mode, "JAM_SHUTDOWN_MARKER="+marker)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("child = %v, want killed by a signal", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		t.Fatalf("child ended %v, want killed by a signal", ee)
	}
	b, _ := os.ReadFile(marker)
	return ws.Signal(), string(b)
}

// The real path: serve's handler, signalled, stops the builds and then
// ends the process killed by that signal (a shell's 143), as an unhandled
// SIGTERM did, not by an exit code.
func TestShutdownDiesByTheSignal(t *testing.T) {
	if m := os.Getenv("JAM_SHUTDOWN_CHILD"); m != "" {
		shutdownChild(m)
	}
	sig, marker := runShutdownChild(t, "TestShutdownDiesByTheSignal", "term")
	if sig != syscall.SIGTERM || marker != "stopped;terminated" {
		t.Fatalf("child killed by %v, marker %q; want SIGTERM through the handler (stopped;terminated)", sig, marker)
	}
}

// A serve started with SIGINT ignored keeps ignoring it: SIGINT neither
// stops it nor kills it, SIGTERM still does.
func TestShutdownKeepsAnIgnoredSIGINT(t *testing.T) {
	if m := os.Getenv("JAM_SHUTDOWN_CHILD"); m != "" {
		shutdownChild(m)
	}
	sig, marker := runShutdownChild(t, "TestShutdownKeepsAnIgnoredSIGINT", "ignored-int")
	if sig != syscall.SIGTERM || marker != "stopped;terminated" {
		t.Fatalf("child killed by %v, marker %q; want SIGINT ignored and SIGTERM through the handler", sig, marker)
	}
}
