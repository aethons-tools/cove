package agentrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// recordHandle records the activities the workload reports. Safe for
// concurrent use: Run reports from its own goroutine while a test may poll
// count() from the test goroutine.
type recordHandle struct {
	mu         sync.Mutex
	got        []covemaster.Activity
	events     []recordedEvent
	connectors []string
}

func (h *recordHandle) ConnectorApplied(fp string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connectors = append(h.connectors, fp)
}

type recordedEvent struct {
	turn    uint32
	raw     string
	dropped uint64
}

func (h *recordHandle) Event(turn uint32, raw []byte, dropped uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, recordedEvent{turn, string(raw), dropped})
}

func (h *recordHandle) eventList() []recordedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedEvent(nil), h.events...)
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
	env      []string
	proc     Process
	err      error
	stdout   io.Writer
}

func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	f.bin, f.args, f.dir, f.env, f.stdout = bin, args, dir, env, stdout
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
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config", "do the thing"}
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
	env      []string
}

// scriptedSpawner scripts a worker-result.json body per call: call i's Wait
// writes results[i] (if present) into dir/.at-task/worker-result.json before
// returning nil.
type scriptedSpawner struct {
	mu      sync.Mutex
	results []string
	lines   [][]string // per call: stdout lines written before the result file
	dir     string
	calls   []scriptedCall
}

func (f *scriptedSpawner) Spawn(_ context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, scriptedCall{bin: bin, args: append([]string(nil), args...), dir: dir, env: append([]string(nil), env...)})
	f.mu.Unlock()
	return scriptedProc{wait: func() error {
		if i < len(f.lines) && stdout != nil {
			for _, l := range f.lines[i] {
				io.WriteString(stdout, l+"\n")
			}
		}
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
	if got := strings.Join(f.calls[1].args[:5], " "); got != "-p --continue --output-format stream-json --verbose" {
		t.Fatalf("2nd turn argv prefix = %q", got)
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

// residentWL builds a resident Workload over a scriptedSpawner.
func residentWL(t *testing.T, dir string, f *scriptedSpawner, maxWait time.Duration) *Workload {
	t.Helper()
	return New(Config{WorkDir: dir, Prompt: "p", Resident: true, MaxWait: maxWait, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
}

// runAsync starts Run in a goroutine and returns its result channel.
func runAsync(ctx context.Context, w *Workload, h covemaster.Handle) chan error {
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, h) }()
	return done
}

// assertBlocked fails if Run returns within d.
func assertBlocked(t *testing.T, done chan error, d time.Duration) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("resident Run returned early (%v); it must keep waiting", err)
	case <-time.After(d):
	}
}

// TestResidentWaitsAfterEveryOutcome: in resident mode an ok, error, or
// missing worker-result turn reports Waiting and keeps waiting (no Done), even
// past a short MaxWait.
func TestResidentWaitsAfterEveryOutcome(t *testing.T) {
	for name, result := range map[string]string{
		"ok":          `{"status":{"ok":{}}}`,
		"needs-input": `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`,
		"error":       `{"status":{"error":{"message":"boom"}}}`,
		"unparseable": `{"status":{}}`,
		"missing":     "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := &scriptedSpawner{dir: dir}
			if result != "" {
				f.results = []string{result}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := residentWL(t, dir, f, 20*time.Millisecond)
			h := &recordHandle{}
			done := runAsync(ctx, w, h)
			waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
			assertBlocked(t, done, 150*time.Millisecond) // well past MaxWait
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("Run: want ctx error on shutdown, got nil")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not return after ctx cancel")
			}
		})
	}
}

// TestResidentResumesOnWake: each Wake resumes with --continue and the
// resident resume prompt; the loop keeps going after an ok turn.
func TestResidentResumesOnWake(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{
		results: []string{`{"status":{"ok":{}}}`, `{"status":{"ok":{}}}`, `{"status":{"ok":{}}}`},
		dir:     dir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := residentWL(t, dir, f, time.Minute)
	h := &recordHandle{}
	done := runAsync(ctx, w, h)
	for i := 1; i <= 2; i++ {
		waitFor(t, func() bool { return h.count(covemaster.Waiting) == i })
		w.Control(covemaster.Control{Kind: covemaster.Wake})
	}
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 3 })
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 3 {
		t.Fatalf("want 3 spawns, got %d", len(f.calls))
	}
	if hasArg(f.calls[0].args, "--continue") || f.calls[0].args[len(f.calls[0].args)-1] != "p" {
		t.Fatalf("1st turn: want the original prompt without --continue, got %v", f.calls[0].args)
	}
	for _, c := range f.calls[1:] {
		if !hasArg(c.args, "--continue") {
			t.Fatalf("resumed turn missing --continue: %v", c.args)
		}
		if got := c.args[len(c.args)-1]; got != residentResumePrompt {
			t.Fatalf("resumed turn prompt = %q; want residentResumePrompt", got)
		}
	}
}

// TestNonResidentOKStillEnds guards that Resident defaults off: an ok turn
// ends the unit with no Waiting.
func TestNonResidentOKStillEnds(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{results: []string{`{"status":{"ok":{}}}`}, dir: dir}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.count(covemaster.Waiting) != 0 {
		t.Fatalf("non-resident ok must not wait; got %v", h.got)
	}
}

func TestRunForwardsStdoutLinesWithTurns(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir,
		lines:   [][]string{{`{"type":"system"}`, `{"type":"result"}`}, {`{"type":"assistant"}`}},
		results: []string{`{"status":{"needs-input":{}}}`, `{"status":{"ok":{}}}`}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := runAsync(context.Background(), w, h)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := h.eventList()
	want := []recordedEvent{{1, `{"type":"system"}`, 0}, {1, `{"type":"result"}`, 0}, {2, `{"type":"assistant"}`, 0}}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestRunForwardsTrailingPartialLine(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{}
	f.proc = scriptedProc{wait: func() error { io.WriteString(f.stdout, `{"no":"newline"}`); return nil }}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if ev := h.eventList(); len(ev) != 1 || ev[0].raw != `{"no":"newline"}` {
		t.Fatalf("events: %+v", ev)
	}
}

func TestRunWritesStdoutToStreamLogAndEmitsEvents(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "agent-stream.jsonl")
	f := &scriptedSpawner{dir: dir,
		lines:   [][]string{{`{"type":"system"}`, `{"type":"result"}`}},
		results: []string{`{"status":{"ok":{}}}`}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f, StreamLogPath: logPath}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{\"type\":\"system\"}\n{\"type\":\"result\"}\n" {
		t.Fatalf("stream log %q", b)
	}
	if n := len(h.eventList()); n != 2 {
		t.Fatalf("events: %+v", h.eventList())
	}
	if fi, _ := os.Stat(logPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestRunAppliesConnectorPerSpawn(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return []string{"PATH=/bin"} }}}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if envMap(f.env)["GH_HOST"] != "jam.example" {
		t.Fatalf("spawn env = %v", f.env)
	}
	if len(h.connectors) != 1 || h.connectors[0] != snippet.Fingerprint(src.c) {
		t.Fatalf("reported = %v", h.connectors)
	}
}

func TestRunWithoutConnectorInherits(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if f.env != nil || len(h.connectors) != 0 {
		t.Fatalf("no connector config must inherit env and report nothing: env=%v reports=%v", f.env, h.connectors)
	}
}

// seqSource returns connectors[i] on its i-th Fetch (the last one thereafter).
type seqSource struct {
	mu         sync.Mutex
	n          int
	connectors []snippet.Connector
}

func (s *seqSource) Fetch(context.Context) (snippet.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectors[min(s.n, len(s.connectors)-1)]
	s.n++
	return c, nil
}

// Across turns (needs-input → wake → needs-input → wake → ok) each spawn gets the
// connector current at its start: a change between turns reaches the next
// spawn's env and is reported once; an unchanged one is not re-reported.
func TestRunRefreshesConnectorAcrossTurns(t *testing.T) {
	dir := t.TempDir()
	needsInput := `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`
	f := &scriptedSpawner{results: []string{needsInput, needsInput, `{"status":{"ok":{}}}`}, dir: dir}
	a := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "OLD": "1"}}
	b := snippet.Connector{Env: map[string]string{"GH_HOST": "{base}/gh"}}
	src := &seqSource{connectors: []snippet.Connector{a, b, b}}
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, MCPConfigPath: mcpConfigFile(t, dir), Spawner: f,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return []string{"PATH=/bin"} }}}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background(), h) }()
	for n := 1; n <= 2; n++ {
		waitFor(t, func() bool { return h.count(covemaster.Waiting) == n })
		w.Control(covemaster.Control{Kind: covemaster.Wake})
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 3 {
		t.Fatalf("want 3 spawns, got %d", len(f.calls))
	}
	if m := envMap(f.calls[0].env); m["GH_HOST"] != "jam.example" || m["OLD"] != "1" {
		t.Fatalf("turn 1 env = %v", m)
	}
	for _, i := range []int{1, 2} {
		m := envMap(f.calls[i].env)
		if m["GH_HOST"] != "https://jam.example/gh" {
			t.Fatalf("turn %d env must carry the new connector: %v", i+1, m)
		}
		if _, ok := m["OLD"]; ok {
			t.Fatalf("turn %d env kept a dropped key: %v", i+1, m)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	want := []string{snippet.Fingerprint(a), snippet.Fingerprint(b)}
	if !slices.Equal(h.connectors, want) {
		t.Fatalf("reported %v, want one report per distinct connector %v", h.connectors, want)
	}
}

func TestRunSpawnArgsWithContext(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	mcp := mcpConfigFile(t, dir)
	cdir := filepath.Join(dir, "context")
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	b := &sessionctx.Bundle{Core: "CORE", Files: map[string]string{"INDEX.md": "I"}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", MCPConfigPath: mcp, Spawner: f, Context: b, ContextDir: cdir}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config",
		"--append-system-prompt-file", filepath.Join(cdir, "CORE.md"), "--system-prompt-snapshot", "off", "do the thing"}
	if !slices.Equal(f.args, want) {
		t.Fatalf("args:\nwant %v\ngot  %v", want, f.args)
	}
	if got, _ := os.ReadFile(filepath.Join(cdir, "CORE.md")); string(got) != "CORE" {
		t.Fatalf("CORE.md = %q", got)
	}
}

// If the bundle cannot be written, run without the flags: claude hard-fails
// on a missing --append-system-prompt-file.
func TestRunContextWriteFailureRunsWithoutFlags(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o644)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f,
		Context: &sessionctx.Bundle{Core: "C"}, ContextDir: filepath.Join(blocker, "context")}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	if hasArg(f.args, "--append-system-prompt-file") || hasArg(f.args, "--system-prompt-snapshot") {
		t.Fatalf("flags must be absent when the bundle was not written: %v", f.args)
	}
}

// With no bundle in effect, a previous raise's context must not linger:
// SANDBOX.md keys "you are a Jam session" on CORE.md existing.
func TestRunWithoutContextRemovesStaleDir(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	cdir := filepath.Join(dir, "context")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "CORE.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f, ContextDir: cdir}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cdir); !os.IsNotExist(err) {
		t.Fatalf("stale context dir must be removed, stat err = %v", err)
	}
}
