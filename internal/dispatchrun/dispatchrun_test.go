package dispatchrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
)

// fakeOps records DispatchOps calls; Dial returns a fixed endpoint.
type fakeOps struct {
	scavenged bool
	built     bool
	ran       bool
	removed   bool
}

func (f *fakeOps) BuildImage(_, _ string) error { f.built = true; return nil }
func (f *fakeOps) RunEphemeral(_, name, _ string) (backend.Instance, error) {
	f.ran = true
	return backend.Instance{Container: name}, nil
}
func (f *fakeOps) Dial(string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeOps) RemoveContainer(string) error { f.removed = true; return nil }
func (f *fakeOps) ScavengeLabeled(string, time.Duration, time.Time) (int, error) {
	f.scavenged = true
	return 0, nil
}

func TestDispatchHappyPath(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{"issue":{}}`)
	out := dir + "/output.json"
	// the ssh `cat /out/output.json` returns the worker's output
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":"OK"}`) // helper: make any `cat /out/output.json` ssh return this
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Dispatch: kit.DispatchConfig{Command: []string{"run-worker.sh"}}},
		BuildDir: dir, Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !ops.scavenged || !ops.built || !ops.ran || !ops.removed {
		t.Fatalf("ops sequence incomplete: %+v", ops)
	}
	if b := readFile(t, out); !strings.Contains(b, `"status":"OK"`) {
		t.Fatalf("output not extracted: %q", b)
	}
	// the worker's dispatch command ran, timeout-wrapped, secrets sourced
	joined := allCalls(r)
	if !strings.Contains(joined, "run-worker.sh") || !strings.Contains(joined, "timeout ") {
		t.Fatalf("dispatch command not run with timeout:\n%s", joined)
	}
}

func TestDispatchRemovesContainerOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{}`)
	r := &runner.Fake{} // no cat output → extraction fails
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Dispatch: kit.DispatchConfig{Command: []string{"x"}}},
		BuildDir: dir, Name: "disp-2", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error when no output is produced")
	}
	if !ops.removed {
		t.Fatal("container must be removed even on failure")
	}
}

// --- test helpers, against the real runner.Fake shape ---
//
// runner.Fake.Outputs is an ordered []FakeResult consumed by Output() calls in
// call order (a counter, not a keyed map). In this orchestration the only
// r.Output(...) call is the final `ssh ... cat /out/output.json` (secret
// resolution uses r.Output too, but these tests declare no Secrets), so queuing
// exactly one FakeResult reliably serves that one call.

// writeFile creates dir/name with content and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writeFile %s: %v", p, err)
	}
	return p
}

// readFile returns the contents of path as a string.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %s: %v", path, err)
	}
	return string(b)
}

// setOutputForCat queues r's next Output(...) result (the `cat /out/output.json`
// ssh invocation) to return stdout.
func setOutputForCat(r *runner.Fake, stdout string) {
	r.Outputs = append(r.Outputs, runner.FakeResult{Stdout: stdout})
}

// allCalls joins every recorded call's argv (name + args) across all Fake
// methods, one call per line, so tests can substring-match on it.
func allCalls(r *runner.Fake) string {
	var b strings.Builder
	for _, c := range r.Calls {
		b.WriteString(c.Name)
		b.WriteString(" ")
		b.WriteString(strings.Join(c.Args, " "))
		b.WriteString("\n")
	}
	return b.String()
}
