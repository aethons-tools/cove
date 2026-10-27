package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"
)

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFakeRecordsCalls(t *testing.T) {
	f := &Fake{}
	err := f.Run("sbx", "run", "box")
	if err != nil {
		t.Fatal(err)
	}
	err = f.Run("sbx", "remove", "box")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(f.Calls))
	}
	if f.Calls[0].Name != "sbx" {
		t.Fatalf("name = %q", f.Calls[0].Name)
	}
	if !equal(f.Calls[0].Args, []string{"run", "box"}) {
		t.Fatalf("args = %v", f.Calls[0].Args)
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	sentinel := errors.New("boom")
	f := &Fake{Err: sentinel}
	err := f.Run("sbx", "run")
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
}

// TestProbeDiscardsStderr guards the reachability-probe contract: Probe is used
// for checks like `docker info` whose stderr (e.g. benign "bridge-nf-call-iptables
// is disabled" daemon warnings) is pure noise. Unlike Output, it must not leak the
// child's stderr to the terminal.
func TestProbeDiscardsStderr(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	probeErr := OS{}.Probe("sh", "-c", "echo bridge-nf-noise >&2")
	w.Close()
	os.Stderr = old
	leaked, _ := io.ReadAll(r)
	if probeErr != nil {
		t.Fatalf("Probe of a succeeding command errored: %v", probeErr)
	}
	if len(leaked) != 0 {
		t.Fatalf("Probe leaked child stderr to os.Stderr: %q", leaked)
	}
}

func TestProbePropagatesExitCode(t *testing.T) {
	var xe *ExitError
	err := OS{}.Probe("sh", "-c", "exit 5")
	if !errors.As(err, &xe) {
		t.Fatalf("got %T (%v), want *ExitError", err, err)
	}
	if xe.ExitCode() != 5 {
		t.Fatalf("ExitCode() = %d, want 5", xe.ExitCode())
	}
}

func TestFakeProbeRecordsAndReturnsErr(t *testing.T) {
	sentinel := errors.New("unreachable")
	f := &Fake{Err: sentinel}
	if err := f.Probe("docker", "info"); !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want %v", err, sentinel)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "docker" || !equal(f.Calls[0].Args, []string{"info"}) {
		t.Fatalf("Probe should record the call: %+v", f.Calls)
	}
}

func TestOSPropagatesExitCode(t *testing.T) {
	var xe *ExitError
	err := OS{}.Run("sh", "-c", "exit 7")
	if !errors.As(err, &xe) {
		t.Fatalf("got %T (%v), want *ExitError", err, err)
	}
	if xe.ExitCode() != 7 {
		t.Fatalf("ExitCode() = %d, want 7", xe.ExitCode())
	}
}

func TestOSSuccess(t *testing.T) {
	err := OS{}.Run("true")
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestOSOutputCaptures(t *testing.T) {
	out, err := OS{}.Output("sh", "-c", "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("Output = %q, want %q", out, "hello")
	}
}

func TestFakeOutputReturnsQueuedResults(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "one"}, {Stdout: "two"}}}
	a, _ := f.Output("docker", "port", "x")
	b, _ := f.Output("docker", "inspect", "x")
	if a != "one" || b != "two" {
		t.Fatalf("got %q,%q want one,two", a, b)
	}
	if len(f.Calls) != 2 || f.Calls[0].Name != "docker" {
		t.Fatalf("calls not recorded: %+v", f.Calls)
	}
}

func TestFakeOutputEnvRecordsEnvAndReturnsQueuedResult(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "tok"}}}
	out, err := f.OutputEnv([]string{"K=V"}, "mint", "x")
	if err != nil {
		t.Fatal(err)
	}
	if out != "tok" {
		t.Fatalf("OutputEnv = %q, want %q", out, "tok")
	}
	if len(f.Calls) != 1 || len(f.Calls[0].Env) != 1 || f.Calls[0].Env[0] != "K=V" {
		t.Fatalf("env not recorded: %+v", f.Calls)
	}
}

func TestFakeRunEnvRecordsEnv(t *testing.T) {
	f := &Fake{}
	if err := f.RunEnv([]string{"K=V"}, "ssh", "host"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || len(f.Calls[0].Env) != 1 || f.Calls[0].Env[0] != "K=V" {
		t.Fatalf("env not recorded: %+v", f.Calls)
	}
}

func TestFakeRunStdinRecordsCall(t *testing.T) {
	f := &Fake{}
	if err := f.RunStdin(strings.NewReader("export X=1\n"), "ssh", "host", "cat > /x"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh" || f.Calls[0].Args[0] != "host" {
		t.Fatalf("call not recorded: %+v", f.Calls)
	}
}

func TestOSRunStdinFeedsStdin(t *testing.T) {
	// `cat` echoes stdin to stdout; with stdout not captured this just must not error.
	if err := (OS{}).RunStdin(strings.NewReader("hi"), "cat"); err != nil {
		t.Fatal(err)
	}
}

// TestOSRunIOStreamsToInjectedWriters is the injection contract on the production
// runner: when a caller supplies stdout/stderr writers, the child's streams flow
// there instead of the host console.
func TestOSRunIOStreamsToInjectedWriters(t *testing.T) {
	var out, errBuf bytes.Buffer
	err := (OS{}).RunIO(nil, &out, &errBuf, "sh", "-c", "printf out; printf err >&2")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "out" {
		t.Fatalf("stdout = %q, want %q", out.String(), "out")
	}
	if errBuf.String() != "err" {
		t.Fatalf("stderr = %q, want %q", errBuf.String(), "err")
	}
}

// TestOSRunIONilWritersDefaultToProcessStreams guards the unchanged-by-default
// behaviour: nil writers fall back to the process's os.Stdout/os.Stderr, so
// existing callers keep streaming live.
func TestOSRunIONilWritersDefaultToProcessStreams(t *testing.T) {
	if err := (OS{}).RunIO(nil, nil, nil, "true"); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestOSRunIOPropagatesExitCode(t *testing.T) {
	var xe *ExitError
	err := (OS{}).RunIO(nil, nil, nil, "sh", "-c", "exit 9")
	if !errors.As(err, &xe) {
		t.Fatalf("got %T (%v), want *ExitError", err, err)
	}
	if xe.ExitCode() != 9 {
		t.Fatalf("ExitCode() = %d, want 9", xe.ExitCode())
	}
}

// TestFakeRunIORedirectsOutputToBuffer is the DoD scenario: a host caller
// redirects a runner's output into a buffer and reads it back via Fake. Fake
// streams its queued stdout to the injected writer (mirroring OS) and records the
// writers it was handed so the caller can assert redirection.
func TestFakeRunIORedirectsOutputToBuffer(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "vm output\n"}}}
	var buf bytes.Buffer
	if err := f.RunIO(nil, &buf, nil, "ssh", "host", "at-task"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "vm output\n" {
		t.Fatalf("redirected output = %q, want %q", buf.String(), "vm output\n")
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh" {
		t.Fatalf("call not recorded: %+v", f.Calls)
	}
	if f.Calls[0].Stdout != &buf {
		t.Fatalf("Fake did not record injected stdout writer")
	}
}

// TestFakeRunIORecordsStdin confirms RunIO records piped stdin like RunStdin.
func TestFakeRunIORecordsStdin(t *testing.T) {
	f := &Fake{}
	if err := f.RunIO(strings.NewReader("payload"), nil, nil, "ssh", "host", "cat > /x"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Stdin != "payload" {
		t.Fatalf("stdin not recorded: %+v", f.Calls)
	}
}

func TestFakeRunIOStreamsQueuedStderr(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "out", Stderr: "err", Err: errors.New("x")}}}
	var o, e bytes.Buffer
	if err := f.RunIO(nil, &o, &e, "docker"); err == nil || o.String() != "out" || e.String() != "err" {
		t.Fatalf("err %v stdout %q stderr %q", err, o.String(), e.String())
	}
}

// Scoped copies Run's output and Output's stderr to the log; Output's
// stdout is the caller's, Probe's output is dropped.
func TestScopedCopiesOutputToLog(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{
		{Stdout: "built\n", Stderr: "warn\n"},
		{Stdout: "sha256:id\n", Stderr: "inspect-noise\n"},
		{Stdout: "probe-out", Stderr: "probe-err"},
	}}
	var log bytes.Buffer
	r := Scoped(context.Background(), f, &log)
	if err := r.Run("docker", "build"); err != nil {
		t.Fatal(err)
	}
	out, err := r.Output("docker", "inspect")
	if err != nil || out != "sha256:id\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if err := r.Probe("docker", "info"); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != "built\nwarn\ninspect-noise\n" {
		t.Fatalf("log = %q", got)
	}
	if len(f.Calls) != 3 || f.Calls[0].Args[0] != "build" || f.Calls[2].Args[0] != "info" {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

// RunIO keeps the caller's stdout (a streamed build's image id) and tees
// its stderr into the log.
func TestScopedRunIOKeepsStdoutTeesStderr(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "sha256:child\n", Stderr: "#3 RUN make\n"}}}
	var log, out, errb bytes.Buffer
	if err := Scoped(context.Background(), f, &log).RunIO(strings.NewReader("TAR"), &out, &errb, "docker", "build", "-"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "sha256:child\n" || errb.String() != "#3 RUN make\n" || log.String() != "#3 RUN make\n" || f.Calls[0].Stdin != "TAR" {
		t.Fatalf("out %q err %q log %q stdin %q", out.String(), errb.String(), log.String(), f.Calls[0].Stdin)
	}
}

// A done context refuses every command, the call still recorded.
func TestScopedDoneContextRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &Fake{}
	r := Scoped(ctx, f, io.Discard)
	if err := r.Run("docker", "build"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := r.Output("docker", "inspect"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Output = %v", err)
	}
	if err := r.RunEnv(nil, "docker"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunEnv = %v", err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %+v (RunEnv checks ctx before delegating)", f.Calls)
	}
}

// A runner that is not a ContextRunner still gets its output logged and a
// done ctx refused before the command starts.
func TestScopedPlainRunnerChecksCtx(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "built\n", Stderr: "warn\n"}}}
	var log bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	r := Scoped(ctx, plainRunner{f}, &log)
	if err := r.Run("docker", "build"); err != nil || log.String() != "built\nwarn\n" {
		t.Fatalf("Run = %v, log %q", err, log.String())
	}
	cancel()
	if err := r.Run("docker", "build"); !errors.Is(err, context.Canceled) || len(f.Calls) != 1 {
		t.Fatalf("Run after cancel = %v, calls %+v", err, f.Calls)
	}
}

// plainRunner hides Fake's RunIOContext, so Scoped sees a plain Runner.
type plainRunner struct{ Runner }

// Fake.RunIOContext consumes and streams queued results by RunIO's rule.
func TestFakeRunIOContextMatchesRunIO(t *testing.T) {
	queue := []FakeResult{{Stdout: "a", Stderr: "b"}, {Stdout: "c", Err: errors.New("boom")}}
	f, g := &Fake{Outputs: queue}, &Fake{Outputs: queue}
	for i := 0; i < 3; i++ {
		var fo, fe, gox, ge bytes.Buffer
		ferr := f.RunIO(strings.NewReader("in"), &fo, &fe, "x")
		gerr := g.RunIOContext(context.Background(), strings.NewReader("in"), &gox, &ge, "x")
		if fo.String() != gox.String() || fe.String() != ge.String() || fmt.Sprint(ferr) != fmt.Sprint(gerr) {
			t.Fatalf("call %d: RunIO (%q, %q, %v) != RunIOContext (%q, %q, %v)", i, fo.String(), fe.String(), ferr, gox.String(), ge.String(), gerr)
		}
	}
	// Like RunIO, a nil stdout consumes nothing.
	h := &Fake{Outputs: []FakeResult{{Stdout: "kept"}}}
	_ = h.RunIOContext(context.Background(), nil, nil, nil, "x")
	if out, _ := h.Output("x"); out != "kept" {
		t.Fatalf("a nil-stdout RunIOContext consumed a result: next Output = %q", out)
	}
}

// OnRun lets a test hold a command until its context is cancelled.
func TestFakeOnRunBlocksUntilCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &Fake{OnRun: func(ctx context.Context, c Call) error {
		cancel() // the operator cancels while the command runs
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := Scoped(ctx, f, io.Discard).Run("docker", "build"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

// helperEnv makes the test binary, re-run as a child, act as a process that
// blocks until it is interrupted (TestHelperBlockUntilInterrupt).
const helperEnv = "JAM_RUNNER_TEST_HELPER"

// TestHelperBlockUntilInterrupt is not a test: run as a child with
// helperEnv=block it prints "ready" and exits once it gets an interrupt.
func TestHelperBlockUntilInterrupt(t *testing.T) {
	if os.Getenv(helperEnv) != "block" {
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	fmt.Println("ready")
	<-sig
	os.Exit(0)
}

// readyWriter closes ready on the child's first output.
type readyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

// A cancelled OS command gets an interrupt, and the call returns ctx.Err().
func TestOSRunIOContextStopsOnCancel(t *testing.T) {
	t.Setenv(helperEnv, "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &readyWriter{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- OS{}.RunIOContext(ctx, nil, w, io.Discard, os.Args[0], "-test.run=^TestHelperBlockUntilInterrupt$")
	}()
	select {
	case <-w.ready:
	case err := <-done:
		t.Fatalf("the helper exited before it was cancelled: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("the helper never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("a cancelled command did not stop")
	}
}

func TestOSRunIOContextPropagatesExitCode(t *testing.T) {
	err := OS{}.RunIOContext(context.Background(), nil, io.Discard, io.Discard, "sh", "-c", "exit 3")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want exit 3", err)
	}
}

// cancelOnWrite cancels its ctx a moment after the child's output reaches
// it: by then the child has exited 0 and exec has seen it exit.
type cancelOnWrite struct {
	once   sync.Once
	cancel context.CancelFunc
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	w.once.Do(func() {
		time.Sleep(200 * time.Millisecond)
		w.cancel()
	})
	return len(p), nil
}

// A command that succeeded keeps its success when the cancel lands only as
// the call returns: ctx.Err() is returned for a failed command only.
func TestOSRunIOContextKeepsASuccessTheCancelMissed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancelOnWrite{cancel: cancel}
	err := OS{}.RunIOContext(ctx, nil, w, io.Discard, "sh", "-c", "printf hi")
	if err != nil || ctx.Err() == nil {
		t.Fatalf("err = %v (ctx %v)", err, ctx.Err())
	}
}
