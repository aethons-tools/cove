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

func TestDispatchRunsBracket(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":{"ok":{}}}`) // the final `cat …/task-result.json`
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		BuildDir: dir, Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	calls := allCalls(r)
	// the three bracket steps ran, in order, cd'd to the workdir
	for _, want := range []string{"at-work prepare", "claude -p", "at-work complete"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing bracket step %q:\n%s", want, calls)
		}
	}
	if strings.Index(calls, "at-work prepare") > strings.Index(calls, "claude -p") ||
		strings.Index(calls, "claude -p") > strings.Index(calls, "at-work complete") {
		t.Fatalf("bracket steps out of order:\n%s", calls)
	}
	if !strings.Contains(calls, "cat "+resultVMPath) {
		t.Fatalf("did not extract the result:\n%s", calls)
	}
	if b := readFile(t, out); !strings.Contains(b, `"ok"`) {
		t.Fatalf("result not written: %q", b)
	}
}

// TestDispatchAirGapsTokenFromAgent is THE security test: the code-host token
// must reach the VM for prepare and complete, but must never be present during
// the agent step, and must never appear on any argv.
func TestDispatchAirGapsTokenFromAgent(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	const tok = "ghp-secret-token-value"
	// secret.Resolve calls r.Output per resolver (in name order: AT_WORK_GIT_TOKEN, OTHER),
	// then the final `cat` for the result.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: tok + "\n"},             // AT_WORK_GIT_TOKEN resolver
		{Stdout: "other-value\n"},        // OTHER resolver
		{Stdout: `{"status":{"ok":{}}}`}, // cat result
	}}
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		Secrets: []secret.Spec{
			{Name: "AT_WORK_GIT_TOKEN", Command: []string{"gh", "auth", "token"}},
			{Name: "OTHER", Command: []string{"echo", "x"}},
		},
		BuildDir: dir, Name: "disp-ag", InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// the env-file writes, in call order, are prepare / agent / complete.
	var envWrites []string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+envVMPath) {
			envWrites = append(envWrites, c.Stdin)
		}
	}
	if len(envWrites) != 3 {
		t.Fatalf("want 3 env-file writes (prepare/agent/complete); got %d", len(envWrites))
	}
	if !strings.Contains(envWrites[0], tok) {
		t.Fatal("prepare env must carry AT_WORK_GIT_TOKEN")
	}
	if strings.Contains(envWrites[1], tok) {
		t.Fatal("AIR-GAP BREACH: the agent step's env carried AT_WORK_GIT_TOKEN")
	}
	if !strings.Contains(envWrites[1], "other-value") {
		t.Fatal("agent step should still carry other secrets")
	}
	if !strings.Contains(envWrites[2], tok) {
		t.Fatal("complete env must carry AT_WORK_GIT_TOKEN")
	}
	// and the token must never appear on any argv
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), tok) {
			t.Fatalf("token leaked onto argv: %v", c.Args)
		}
	}
}

func TestDispatchUndeclaredClassErrors(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"nope"}}`)
	err := Dispatch(Options{
		Ops: &fakeOps{}, R: &runner.Fake{},
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		BuildDir: dir, Name: "x", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error for an undeclared worker class")
	}
}

func TestDispatchRemovesContainerOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	r := &runner.Fake{} // no cat output → extraction fails
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
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
// call order (a counter, not a keyed map). In this orchestration the r.Output(...)
// calls are secret.Resolve's resolver commands (in Secrets slice order) followed by
// the final `ssh ... cat .../task-result.json`.

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
