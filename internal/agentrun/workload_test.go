package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// recordHandle records the activities the workload reports. Safe for
// concurrent use: Run reports from its own goroutine while a test may poll
// count() from the test goroutine.
type recordHandle struct {
	mu  sync.Mutex
	got []covemaster.Activity
}

func (h *recordHandle) Report(a covemaster.Activity) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, a)
}

// count returns how many times Activity a has been reported so far.
func (h *recordHandle) count(a covemaster.Activity) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, x := range h.got {
		if x == a {
			n++
		}
	}
	return n
}

// scriptedProc runs a closure as its Wait.
type scriptedProc struct{ wait func() error }

func (p scriptedProc) Wait() error { return p.wait() }

type fakeSpawner struct {
	bin, dir string
	args     []string
	proc     Process
	err      error
}

func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error) {
	f.bin, f.args, f.dir = bin, args, dir
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}

func writeResult(t *testing.T, dir, body string) {
	t.Helper()
	atTask := filepath.Join(dir, ".at-task")
	if err := os.MkdirAll(atTask, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(atTask, "worker-result.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mcpConfigFile writes a minimal MCP config into dir and returns its path, so
// the Run guard (COV-190) — which refuses to start when --mcp-config is missing
// — passes in tests that exercise the normal run path.
func mcpConfigFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newWL(t *testing.T, dir string, f *fakeSpawner) (*Workload, *recordHandle) {
	t.Helper()
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	return w, &recordHandle{}
}

func TestRunOK(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

// TestRunNeedsInput checks that a needs-input turn reports Waiting; with no
// Wake and a short MaxWait, Run then gives up and ends the unit (nil).
// (Resuming on Wake and blocking past MaxWait are covered by
// TestRunResumesOnWake / TestRunMaxWaitEndsUnit.)
func TestRunNeedsInput(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MaxWait: 40 * time.Millisecond, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	want := []covemaster.Activity{covemaster.Running, covemaster.Waiting}
	if len(h.got) != 2 || h.got[0] != want[0] || h.got[1] != want[1] {
		t.Fatalf("activities: want [Running Waiting], got %v", h.got)
	}
}

func TestRunErrorResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"error":{"message":"boom"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	err := w.Run(context.Background(), h)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

func TestRunNoResultFile(t *testing.T) {
	dir := t.TempDir() // no .at-task/worker-result.json
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for missing result, got nil")
	}
	_ = h
}

func TestRunUnparseableResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{}}`) // no variant set → Active() errors
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for empty status, got nil")
	}
	_ = h
}

func TestRunTeardownCancel(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`) // present, but must NOT be consulted
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { <-ctx.Done(); return ctx.Err() }}}
	w, h := newWL(t, dir, f)
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx, h) }()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run: want ctx error on teardown, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running] (no Waiting from the ok file), got %v", h.got)
	}
	w.Control(covemaster.Control{Kind: covemaster.Teardown}) // must not panic
}

func TestRunSpawnArgs(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	mcp := mcpConfigFile(t, dir)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MCPConfigPath: mcp, Spawner: f}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if f.bin != "claude" {
		t.Errorf("bin: want claude, got %q", f.bin)
	}
	want := []string{"-p", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config", "do the thing"}
	if len(f.args) != len(want) {
		t.Fatalf("args: want %v, got %v", want, f.args)
	}
	for i := range want {
		if f.args[i] != want[i] {
			t.Errorf("args[%d]: want %q, got %q (full: want %v, got %v)", i, want[i], f.args[i], want, f.args)
		}
	}
	if f.dir != dir {
		t.Errorf("dir: want %q, got %q", dir, f.dir)
	}
}

func TestRunSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{err: os.ErrPermission}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want spawn error, got nil")
	}
	if len(h.got) != 0 {
		t.Fatalf("activities: want none (spawn failed before Running), got %v", h.got)
	}
}

// TestNewDefaultsMCPConfigPath guards that the production default MCP config
// path is the baked hardening path (so a real cove points --mcp-config at the
// image's mcp.json unless a caller overrides it).
func TestNewDefaultsMCPConfigPath(t *testing.T) {
	w := New(Config{WorkDir: t.TempDir(), Prompt: "x"}, nil)
	if w.cfg.MCPConfigPath != mcpConfigPath {
		t.Fatalf("default MCPConfigPath = %q; want %q", w.cfg.MCPConfigPath, mcpConfigPath)
	}
}

// TestRunFailsLoudOnMissingMCPConfig guards COV-190: when the --mcp-config file
// is missing, Run must fail before spawning the agent (never launch a toolless
// claude), not proceed. Verified by an error return AND the spawner never being
// invoked (f.bin stays empty; no Running reported).
func TestRunFailsLoudOnMissingMCPConfig(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MCPConfigPath: filepath.Join(dir, "does-not-exist.json"), Spawner: f}, nil)
	h := &recordHandle{}
	err := w.Run(context.Background(), h)
	if err == nil {
		t.Fatal("Run: want error for missing MCP config, got nil")
	}
	if f.bin != "" {
		t.Fatalf("Run must NOT spawn the agent when the MCP config is missing; spawned %q", f.bin)
	}
	if h.count(covemaster.Running) != 0 {
		t.Fatalf("Run must not report Running when the MCP config is missing; got %v", h.got)
	}
}

func TestControlWakeNoop(t *testing.T) {
	w := New(Config{WorkDir: t.TempDir(), Prompt: "x", Spawner: &fakeSpawner{}}, nil)
	w.Control(covemaster.Control{Kind: covemaster.Wake}) // must not panic
}

// scriptedCall records one Spawn call's arguments.
type scriptedCall struct {
	bin, dir string
	args     []string
}

// scriptedSpawner scripts a worker-result.json body per call: call i's Wait
// writes results[i] (if present) into dir/.at-task/worker-result.json before
// returning nil.
type scriptedSpawner struct {
	mu      sync.Mutex
	results []string
	dir     string
	calls   []scriptedCall
}

func (f *scriptedSpawner) Spawn(_ context.Context, bin string, args []string, dir string) (Process, error) {
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, scriptedCall{bin: bin, args: append([]string(nil), args...), dir: dir})
	f.mu.Unlock()
	return scriptedProc{wait: func() error {
		if i < len(f.results) {
			atTask := filepath.Join(f.dir, ".at-task")
			if err := os.MkdirAll(atTask, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(atTask, "worker-result.json"), []byte(f.results[i]), 0o644); err != nil {
				return err
			}
		}
		return nil
	}}, nil
}

// waitFor polls cond until it returns true or fails the test after a timeout.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("waitFor: condition not met in time")
}

// hasArg reports whether want appears among args.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestRunResumesOnWake: needs-input turn, then a Wake, then an ok turn → two
// spawns, 2nd has --continue.
func TestRunResumesOnWake(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{
		results: []string{
			`{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`,
			`{"status":{"ok":{}}}`,
		},
		dir: dir,
	}
	w := New(Config{WorkDir: dir, Prompt: "do it", MaxWait: time.Minute, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background(), h) }()
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 }) // first turn reported Waiting
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after wake")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 2 {
		t.Fatalf("want 2 spawns, got %d", len(f.calls))
	}
	if !hasArg(f.calls[1].args, "--continue") {
		t.Fatalf("2nd turn missing --continue: %v", f.calls[1].args)
	}
}

// TestRunMaxWaitEndsUnit: needs-input, no wake → after MaxWait, Run returns
// nil (Done), one spawn.
func TestRunMaxWaitEndsUnit(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{
		results: []string{`{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`},
		dir:     dir,
	}
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: 40 * time.Millisecond, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("want 1 spawn (no resume), got %d", len(f.calls))
	}
}

// TestRunCtxCancelWhileWaiting: needs-input, cancel ctx while waiting for a
// wake → Run returns ctx.Err().
func TestRunCtxCancelWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{
		results: []string{`{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`},
		dir:     dir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, h) }()
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run: want ctx error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
