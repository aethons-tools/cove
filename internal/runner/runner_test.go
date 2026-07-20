package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
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
