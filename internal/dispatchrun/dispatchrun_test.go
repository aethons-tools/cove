package dispatchrun

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
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
	// the ssh `cat /home/agent/work/.at-work/task-result.json` returns the worker's output
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":"OK"}`) // helper: make any `cat .../task-result.json` ssh return this
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{Name: "w", Dispatch: kit.DispatchConfig{
			Command: []string{"run-worker.sh"},
			Input:   "/home/agent/work/.at-work/task.json",
			Output:  "/home/agent/work/.at-work/task-result.json",
		}},
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
	if !strings.Contains(allCalls(r), "cat /home/agent/work/.at-work/task-result.json") {
		t.Fatalf("did not extract from the configured output path:\n%s", allCalls(r))
	}
}

func TestDispatchRemovesContainerOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{}`)
	r := &runner.Fake{} // no cat output → extraction fails
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{Name: "w", Dispatch: kit.DispatchConfig{
			Command: []string{"x"},
			Input:   "/home/agent/work/.at-work/task.json",
			Output:  "/home/agent/work/.at-work/task-result.json",
		}},
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

// TestDispatchSecretNeverOnArgv locks the "secrets never on argv" guarantee: a
// declared secret's resolved value must reach the VM only via the env-script
// stdin body that runWork pipes over ssh (see writeVM/runWork in
// dispatchrun.go), and must never appear in any recorded call's argv — the
// resolver command's own args, the ssh invocations, or anything else.
func TestDispatchSecretNeverOnArgv(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{}`)
	out := dir + "/output.json"
	const secretValue = "s3cr3t-token-value"
	// Outputs is consumed in call order: secret.Resolve's r.Output (the
	// resolver command) runs first, then the final `cat .../task-result.json`.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: secretValue + "\n"},
		{Stdout: `{"status":"OK"}`},
	}}
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{Name: "w", Dispatch: kit.DispatchConfig{
			Command: []string{"run-worker.sh"},
			Input:   "/home/agent/work/.at-work/task.json",
			Output:  "/home/agent/work/.at-work/task-result.json",
		}},
		BuildDir: dir, Name: "disp-secret",
		Secrets:   []secret.Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}}},
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	foundInStdin := false
	for _, c := range r.Calls {
		if strings.Contains(c.Stdin, secretValue) {
			foundInStdin = true
		}
		if strings.Contains(strings.Join(c.Args, " "), secretValue) {
			t.Fatalf("secret value leaked onto argv: name=%s args=%v", c.Name, c.Args)
		}
	}
	if !foundInStdin {
		t.Fatal("secret value was never injected via any call's stdin (expected the env script)")
	}
}

// flakyRunner fails its first failFirst Run calls, then succeeds — for waitForSSH.
type flakyRunner struct {
	*runner.Fake
	failFirst int
	runs      int
}

func (f *flakyRunner) Run(name string, args ...string) error {
	f.runs++
	if f.runs <= f.failFirst {
		return errors.New("connection refused")
	}
	return nil
}

func TestWaitForSSHRetriesThenSucceeds(t *testing.T) {
	f := &flakyRunner{Fake: &runner.Fake{}, failFirst: 2}
	err := waitForSSH(f, sshargs.Target{Host: "h", Port: 22}, 5, time.Millisecond, func(time.Duration) {})
	if err != nil {
		t.Fatalf("waitForSSH: %v", err)
	}
	if f.runs != 3 {
		t.Fatalf("probed %d times; want 3 (2 fail + 1 success)", f.runs)
	}
}

func TestWaitForSSHExhausts(t *testing.T) {
	f := &flakyRunner{Fake: &runner.Fake{}, failFirst: 100}
	err := waitForSSH(f, sshargs.Target{Host: "h", Port: 22}, 3, time.Millisecond, func(time.Duration) {})
	if err == nil {
		t.Fatal("expected an error when sshd never comes up")
	}
	if f.runs != 3 {
		t.Fatalf("probed %d times; want exactly 3 attempts", f.runs)
	}
}

// --- test helpers, against the real runner.Fake shape ---
//
// runner.Fake.Outputs is an ordered []FakeResult consumed by Output() calls in
// call order (a counter, not a keyed map). In this orchestration the only
// r.Output(...) call is the final `ssh ... cat .../task-result.json` (secret
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

// setOutputForCat queues r's next Output(...) result (the `cat .../task-result.json`
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
