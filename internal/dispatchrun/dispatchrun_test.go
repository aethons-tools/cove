package dispatchrun

import (
	"errors"
	"io"
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
	ran       bool
	ranImage  string // the image RunEphemeral was asked to run
	removed   bool
}

func (f *fakeOps) RunEphemeral(image, name, _ string) (backend.Instance, error) {
	f.ran = true
	f.ranImage = image
	return backend.Instance{Container: name, Image: image}, nil
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
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var injected string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+taskVMPath) {
			injected = c.Stdin
		}
	}
	if !strings.Contains(injected, `"name": "acme/myrepo"`) ||
		!strings.Contains(injected, `"source-branch": "main"`) {
		t.Fatalf("injected task missing filled repo:\n%s", injected)
	}
	calls := allCalls(r)
	// the three bracket steps ran, in order, cd'd to the workdir
	for _, want := range []string{"at-task prepare", "claude -p", "at-task complete"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing bracket step %q:\n%s", want, calls)
		}
	}
	if strings.Index(calls, "at-task prepare") > strings.Index(calls, "claude -p") ||
		strings.Index(calls, "claude -p") > strings.Index(calls, "at-task complete") {
		t.Fatalf("bracket steps out of order:\n%s", calls)
	}
	if !strings.Contains(calls, "cat "+resultVMPath) {
		t.Fatalf("did not extract the result:\n%s", calls)
	}
	if b := readFile(t, out); !strings.Contains(b, `"ok"`) {
		t.Fatalf("result not written: %q", b)
	}
	// The run path consumes the pre-built installed image via RunEphemeral and
	// never builds (COV-38): the image passed must be the one from install.json,
	// and no `docker build` may appear on the recorded argv.
	if !ops.ran || ops.ranImage != "at-cove-for-w" {
		t.Fatalf("RunEphemeral must run the installed image; ran=%v image=%q", ops.ran, ops.ranImage)
	}
	if strings.Contains(calls, "docker build") || strings.Contains(calls, "build --build-arg") {
		t.Fatalf("dispatch must not build on the run path:\n%s", calls)
	}
}

// TestDispatchAirGapsTokenFromAgent is THE security test: the code-host token
// must reach the VM for prepare and complete, but must never be present during
// the agent step, and must never appear on any argv. Per-git-step minting means
// the token is resolved TWICE (once per git step), each time freshly.
func TestDispatchAirGapsTokenFromAgent(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	const tok1 = "ghp-secret-token-value-1"
	const tok2 = "ghp-secret-token-value-2"
	// Call order: base secrets first (OTHER, since AT_TASK_GIT_TOKEN is split out
	// of baseSpecs), then a fresh mint() before prepare, then a fresh mint()
	// before complete, then the final `cat` for the result.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "other-value\n"},        // OTHER (base secret, resolved once)
		{Stdout: tok1 + "\n"},            // mint() for prepare
		{Stdout: tok2 + "\n"},            // mint() for complete
		{Stdout: `{"status":{"ok":{}}}`}, // cat result
	}}
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{
			Name: "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{
				Project: "acme/myrepo", MainBranch: "main",
				Secrets: map[string]kit.SecretConfig{
					"AT_TASK_GIT_TOKEN": {},
				},
			}},
			Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Secrets: []secret.Spec{
			{Name: "OTHER", Command: []string{"echo", "x"}},
		},
		GitToken: secret.Spec{Name: "AT_TASK_GIT_TOKEN", Command: []string{"gh", "auth", "token"}},
		Image:    "at-cove-for-w", Name: "disp-ag", InputPath: in, OutputPath: out,
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
	if !strings.Contains(envWrites[0], tok1) {
		t.Fatal("prepare env must carry the freshly-minted token (tok1)")
	}
	if strings.Contains(envWrites[1], tok1) || strings.Contains(envWrites[1], tok2) {
		t.Fatal("AIR-GAP BREACH: the agent step's env carried AT_TASK_GIT_TOKEN")
	}
	if !strings.Contains(envWrites[1], "other-value") {
		t.Fatal("agent step should still carry other secrets")
	}
	if !strings.Contains(envWrites[2], tok2) {
		t.Fatal("complete env must carry the freshly-minted token (tok2)")
	}
	// and neither token must ever appear on any argv
	for _, c := range r.Calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, tok1) || strings.Contains(joined, tok2) {
			t.Fatalf("token leaked onto argv: %v", c.Args)
		}
	}
}

// TestDispatchPassesRunParamsToResolvers confirms the resolver commands receive
// the run's parameters (COVE_RUN_*) in their environment, so a per-task minter
// script can scope what it mints (e.g. to the issue/repo/class).
func TestDispatchPassesRunParamsToResolvers(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"issue":{"key":"AET-24"},"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "other-value\n"},        // OTHER (base secret)
		{Stdout: `{"status":{"ok":{}}}`}, // cat result
	}}
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Secrets: []secret.Spec{
			{Name: "OTHER", Command: []string{"echo", "x"}},
		},
		Image: "at-cove-for-w", Name: "disp-runparams", InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var found bool
	for _, c := range r.Calls {
		if c.Name == "echo" {
			found = true
			for _, want := range []string{"COVE_RUN_REPO=acme/myrepo", "COVE_RUN_ISSUE=AET-24", "COVE_RUN_CLASS=implement"} {
				if !containsEnv(c.Env, want) {
					t.Fatalf("resolver env = %v; want %q", c.Env, want)
				}
			}
		}
	}
	if !found {
		t.Fatal("resolver command (echo, for OTHER) was never called")
	}
}

// TestWorkerSecretsInjectedOnlyAtAgentStep confirms the worker-class secret
// bucket (Options.WorkerSecrets) is resolved lazily and merged into the agent
// step's env ONLY — root/shared secrets (Options.Secrets) still reach every
// step, but the worker bucket must never reach prepare/complete.
func TestWorkerSecretsInjectedOnlyAtAgentStep(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":{"ok":{}}}`)
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Secrets:       []secret.Spec{{Name: "SHARED", Value: "shared-secret-abc", Literal: true}},
		WorkerSecrets: []secret.Spec{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "worker-tok-xyz", Literal: true}},
		Image:         "at-cove-for-w", Name: "disp-worker",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// the env-file writes, in call order, are prepare / agent / complete. Values
	// are shell-quoted in the written script (export KEY='value'), so match on
	// the distinctive value/key substrings rather than a literal KEY=VALUE join.
	var envWrites []string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+envVMPath) {
			envWrites = append(envWrites, c.Stdin)
		}
	}
	if len(envWrites) != 3 {
		t.Fatalf("want 3 env-file writes (prepare/agent/complete); got %d", len(envWrites))
	}
	if strings.Contains(envWrites[0], "ANTHROPIC_AUTH_TOKEN") || strings.Contains(envWrites[0], "worker-tok-xyz") {
		t.Fatal("worker secret must NOT reach the prepare step")
	}
	if !strings.Contains(envWrites[1], "ANTHROPIC_AUTH_TOKEN") || !strings.Contains(envWrites[1], "worker-tok-xyz") {
		t.Fatal("worker secret must be in the agent step env")
	}
	if !strings.Contains(envWrites[1], "SHARED") || !strings.Contains(envWrites[1], "shared-secret-abc") {
		t.Fatal("root shared secret must still reach the agent")
	}
	if strings.Contains(envWrites[2], "ANTHROPIC_AUTH_TOKEN") || strings.Contains(envWrites[2], "worker-tok-xyz") {
		t.Fatal("worker secret must NOT reach the complete step")
	}
}

// containsEnv reports whether env (a slice of "KEY=VALUE" strings) contains want.
func containsEnv(env []string, want string) bool {
	for _, v := range env {
		if v == want {
			return true
		}
	}
	return false
}

func TestDispatchUndeclaredClassErrors(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"nope"}}`)
	err := Dispatch(Options{
		Ops: &fakeOps{}, R: &runner.Fake{},
		Cfg:   kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		Image: "at-cove-for-w", Name: "x", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error for an undeclared worker class")
	}
}

// TestDispatchSourceBranchOverride confirms a task-supplied source-branch is honored
// rather than clobbered by the kit's main-branch default.
func TestDispatchSourceBranchOverride(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"},"repo":{"source-branch":"release"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":{"ok":{}}}`)
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-override",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var injected string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+taskVMPath) {
			injected = c.Stdin
		}
	}
	if !strings.Contains(injected, `"source-branch": "release"`) {
		t.Fatalf("task-supplied source-branch was overwritten:\n%s", injected)
	}
	if strings.Contains(injected, `"source-branch": "main"`) {
		t.Fatalf("expected the override to be kept, not the kit's main-branch:\n%s", injected)
	}
}

// TestDispatchRequiresOrigin confirms dispatch refuses to run when the kit
// declares no origin — the repo has exactly one source of truth now.
func TestDispatchRequiresOrigin(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	err := Dispatch(Options{
		Ops: &fakeOps{}, R: &runner.Fake{},
		Cfg:   kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		Image: "at-cove-for-w", Name: "disp-noorigin", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error when the kit declares no origin")
	}
}

func TestDispatchRemovesContainerOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	r := &runner.Fake{} // no cat output → extraction fails
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-2", InputPath: in, OutputPath: dir + "/o.json",
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

// prepareFailer fails only the `at-task prepare` ssh step; every other RunStdin
// succeeds. It records each call (via the embedded Fake) for inspection.
type prepareFailer struct{ *runner.Fake }

func (p *prepareFailer) RunStdin(stdin io.Reader, name string, args ...string) error {
	_ = p.Fake.RunStdin(stdin, name, args...) // record the call
	if strings.Contains(strings.Join(args, " "), "at-task prepare") {
		return &runner.ExitError{Code: 128, Err: errors.New("git fetch origin main: exit status 128")}
	}
	return nil
}

// A failed `at-task prepare` must abort the run with an error naming the step, so
// the real cause (e.g. a git 403) surfaces — the agent and `at-task complete`
// must NOT run, which would otherwise mask it as a "no worker result".
func TestDispatchPrepareFailureAborts(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	pf := &prepareFailer{Fake: &runner.Fake{}}

	err := Dispatch(Options{
		Ops: &fakeOps{}, R: pf,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "at-task prepare") {
		t.Fatalf("prepare failure must abort with an at-task prepare error; got %v", err)
	}
	calls := allCalls(pf.Fake)
	if strings.Contains(calls, "claude -p") {
		t.Fatalf("agent must not run after a failed prepare:\n%s", calls)
	}
	if strings.Contains(calls, "at-task complete") {
		t.Fatalf("complete must not run after a failed prepare:\n%s", calls)
	}
}

// A dispatched worker must NOT receive the interactive OAuth credentials — it
// authenticates via an injected ANTHROPIC_API_KEY. With CredentialsFile empty
// (as doWork passes), no credentials file is seeded into the VM.
func TestDispatchDoesNotSeedCredentialsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":{"ok":{}}}`)
	err := Dispatch(Options{
		Ops: &fakeOps{}, R: r,
		Cfg: kit.Config{
			Name:          "w",
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
			Workers:       map[string]kit.Worker{"implement": {Prompt: "do it"}},
		},
		Image: "at-cove-for-w", Name: "disp-1",
		InputPath: in, OutputPath: out,
		CredentialsFile: "", // no OAuth creds on the work path
		IdentityFile:    "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if strings.Contains(allCalls(r), credsVMPath) {
		t.Fatalf("worker must not receive OAuth credentials (%s):\n%s", credsVMPath, allCalls(r))
	}
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
