package studioagent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the fake code-server: with IDE_FAKE=1 the test binary
// records its pid and argv as one line in IDE_FAKE_COUNT and (unless IDE_FAKE_MODE=nolisten)
// listens on --bind-addr until SIGTERM. Tests never run the real binary.
func TestMain(m *testing.M) {
	if os.Getenv("IDE_FAKE") == "1" {
		os.Exit(fakeCodeServer(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeCodeServer(args []string) int {
	var addr string
	for i, a := range args {
		if a == "--bind-addr" && i+1 < len(args) {
			addr = args[i+1]
		}
	}
	if f := os.Getenv("IDE_FAKE_COUNT"); f != "" {
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 3
		}
		_, _ = fh.WriteString(strings.Join(append([]string{strconv.Itoa(os.Getpid())}, args...), "\t") + "\n")
		_ = fh.Close()
	}
	if f := os.Getenv("IDE_FAKE_COUNT"); f != "" {
		_ = os.WriteFile(f+".env", []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if os.Getenv("IDE_FAKE_MODE") != "nolisten" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return 4
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	<-ctx.Done()
	return 0
}

// fakeIDE returns an IDEConfig whose Bin is a script exec'ing the test binary
// as the fake code-server, plus the file its starts are counted in.
func fakeIDE(t *testing.T, mode string) (IDEConfig, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "starts")
	bin := filepath.Join(dir, "code-server")
	script := "#!/bin/sh\nIDE_FAKE=1 IDE_FAKE_COUNT='" + count + "' IDE_FAKE_MODE='" + mode + "' exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return IDEConfig{Bin: bin, Workspace: dir, Addr: freeAddr(t), StartWait: 10 * time.Second, Idle: time.Hour, Grace: 2 * time.Second}, count
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func starts(t *testing.T, count string) []int {
	t.Helper()
	raw, err := os.ReadFile(count)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		p, err := strconv.Atoi(strings.SplitN(l, "\t", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, p)
	}
	return pids
}

func accepts(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitUntil polls cond (bounded) — the IDE's state changes asynchronously.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting: " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitStopped waits until the IDE has no process and no start or stop in
// flight.
func waitStopped(t *testing.T, ide *IDE) {
	t.Helper()
	waitUntil(t, "ide stopped", func() bool { return ide.pid() == 0 && !ide.busy() })
}

// stopIDE leaves no fake code-server behind when the test ends.
func stopIDE(t *testing.T, ide *IDE) {
	t.Cleanup(func() {
		waitUntil(t, "start settled", func() bool { return !ide.busy() })
		ide.stopNow()
		waitStopped(t, ide)
	})
}

func TestIDEAcquireStartsAndReuses(t *testing.T) {
	cfg, count := fakeIDE(t, "")
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	ctx := context.Background()
	if err := ide.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if !accepts(cfg.Addr) {
		t.Fatal("port does not accept after Acquire")
	}
	if err := ide.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if got := starts(t, count); len(got) != 1 {
		t.Fatalf("starts = %v, want one process reused", got)
	}
	raw, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\t")[1:]
	want := []string{"--bind-addr", cfg.Addr, "--auth", "none", "--disable-telemetry", "--disable-update-check", cfg.Workspace}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	ide.Release()
	ide.Release()
}

// Close stops code-server at once, open tunnels or not, and waits until it is
// reaped; later Acquires fail rather than start a new one.
func TestIDECloseStopsAndWaits(t *testing.T) {
	cfg, count := fakeIDE(t, "")
	ide := NewIDE(cfg, nil)
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	pid := starts(t, count)[0]
	ide.Close()
	if alive(pid) {
		t.Fatalf("pid %d alive after Close returned", pid)
	}
	if accepts(cfg.Addr) {
		t.Fatal("port still accepts after Close")
	}
	if ide.pid() != 0 || ide.busy() {
		t.Fatal("state not cleared by Close")
	}
	if err := ide.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire after Close started a new IDE")
	}
	if got := starts(t, count); len(got) != 1 {
		t.Fatalf("starts = %v after Close", got)
	}
	ide.Release() // the first Acquire's; harmless after Close
	ide.Close()   // idempotent
}

// Close during a start aborts it at once (not after StartWait) and stops
// what it started.
func TestIDECloseDuringStart(t *testing.T) {
	cfg, count := fakeIDE(t, "nolisten")
	ide := NewIDE(cfg, nil)
	errc := make(chan error, 1)
	go func() { errc <- ide.Acquire(context.Background()) }()
	waitUntil(t, "process started", func() bool { return len(starts(t, count)) == 1 })
	begin := time.Now()
	ide.Close()
	if d := time.Since(begin); d > cfg.StartWait/2 {
		t.Fatalf("Close took %s: it waited out the start", d)
	}
	if pid := starts(t, count)[0]; alive(pid) {
		t.Fatalf("pid %d alive after Close returned", pid)
	}
	if err := <-errc; err == nil {
		t.Fatal("Acquire succeeded though Close stopped the start")
	}
}

func TestIDEIdleStopsThenRestarts(t *testing.T) {
	cfg, count := fakeIDE(t, "")
	cfg.Idle = 200 * time.Millisecond
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	ctx := context.Background()
	if err := ide.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	first := starts(t, count)
	ide.Release()
	waitStopped(t, ide)
	if accepts(cfg.Addr) {
		t.Fatal("port still accepts after the idle stop")
	}
	if alive(first[0]) {
		t.Fatalf("pid %d still alive after the idle stop", first[0])
	}
	if err := ide.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if got := starts(t, count); len(got) != 2 || ide.pid() != got[1] {
		t.Fatalf("starts = %v, pid = %d; want a restart", got, ide.pid())
	}
	if !accepts(cfg.Addr) {
		t.Fatal("restarted IDE does not accept")
	}
	ide.Release()
}

// An open tunnel holds the IDE up past Idle.
func TestIDENoIdleStopWhileOpen(t *testing.T) {
	cfg, _ := fakeIDE(t, "")
	cfg.Idle = 50 * time.Millisecond
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ide.Release() // one still open
	time.Sleep(300 * time.Millisecond)
	if ide.pid() == 0 || !accepts(cfg.Addr) {
		t.Fatal("ide stopped while a tunnel was open")
	}
	ide.Release()
	waitStopped(t, ide)
}

func TestIDEMissingBinUnavailable(t *testing.T) {
	ide := NewIDE(IDEConfig{Bin: filepath.Join(t.TempDir(), "no-such-code-server"), Addr: freeAddr(t)}, nil)
	if err := ide.Acquire(context.Background()); !errors.Is(err, ErrIDEUnavailable) {
		t.Fatalf("err = %v, want ErrIDEUnavailable", err)
	}
	if ide.pid() != 0 {
		t.Fatal("running state set after a failed start")
	}
}

func TestIDEStartTimeoutKillsProcess(t *testing.T) {
	cfg, count := fakeIDE(t, "nolisten")
	cfg.StartWait = 300 * time.Millisecond
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	if err := ide.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire succeeded with a binary that never listens")
	}
	got := starts(t, count)
	if len(got) != 1 {
		t.Fatalf("starts = %v", got)
	}
	if alive(got[0]) {
		t.Fatalf("pid %d still alive after the start timed out", got[0])
	}
	if ide.pid() != 0 {
		t.Fatal("running state set after a timed-out start")
	}
}

func TestIDEConcurrentAcquiresStartOne(t *testing.T) {
	cfg, count := fakeIDE(t, "")
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ide.Acquire(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := starts(t, count); len(got) != 1 {
		t.Fatalf("starts = %v, want exactly one", got)
	}
	for range 10 {
		ide.Release()
	}
}

// A code-server that dies on its own clears the running state; the next
// Acquire starts a fresh one.
func TestIDERestartsAfterCrash(t *testing.T) {
	cfg, count := fakeIDE(t, "")
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ide.Release()
	pid := ide.pid()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, ide)
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := starts(t, count); len(got) != 2 {
		t.Fatalf("starts = %v, want a restart after the crash", got)
	}
	ide.Release()
}

// A caller whose ctx ends while waiting gives up without breaking the start
// for anyone else.
func TestIDEAcquireHonoursContext(t *testing.T) {
	cfg, _ := fakeIDE(t, "nolisten")
	cfg.StartWait = 500 * time.Millisecond
	ide := NewIDE(cfg, nil)
	stopIDE(t, ide)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ide.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the ctx deadline", err)
	}
}

func TestIDEChildEnvDropsJamAndHarborVars(t *testing.T) {
	t.Setenv("AT_JAM_TEST_SECRET", "s3cret")
	t.Setenv("AT_HARBOR_TEST_SECRET", "s3cret")
	t.Setenv("IDE_ENV_HARMLESS", "ok")
	cfg, count := fakeIDE(t, "")
	ide := NewIDE(cfg, nil)
	t.Cleanup(ide.Close)
	if err := ide.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(count + ".env")
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	if strings.Contains(env, "AT_JAM_") || strings.Contains(env, "AT_HARBOR_") || strings.Contains(env, "s3cret") {
		t.Errorf("code-server env leaks cove-master secrets:\n%s", env)
	}
	if !strings.Contains(env, "IDE_ENV_HARMLESS=ok") {
		t.Errorf("harmless var missing from code-server env:\n%s", env)
	}
}

func TestLogLinesFlushKeepsUnterminatedTail(t *testing.T) {
	var out bytes.Buffer
	w := &logLines{log: slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, _ = w.Write([]byte("one\ntail"))
	w.Close()
	w.Close() // idempotent
	if got := strings.Count(out.String(), "ide output"); got != 2 || !strings.Contains(out.String(), "line=tail") {
		t.Errorf("want 2 records incl. tail, got:\n%s", out.String())
	}
}
