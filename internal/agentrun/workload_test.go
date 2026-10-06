package agentrun

import (
	"context"
	"errors"
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
	gates      []covemaster.GateResult
}

func (h *recordHandle) GateResult(g covemaster.GateResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gates = append(h.gates, g)
}

func (h *recordHandle) gateResults() []covemaster.GateResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]covemaster.GateResult(nil), h.gates...)
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

// scriptedProc runs a closure as its Wait; in (optional) records stdin.
type scriptedProc struct {
	wait func() error
	in   io.WriteCloser
}

func (p scriptedProc) Wait() error { return p.wait() }

func (p scriptedProc) Input() io.WriteCloser {
	if p.in != nil {
		return p.in
	}
	return discardInput{}
}

// discardInput is stdin for fakes that ignore it.
type discardInput struct{}

func (discardInput) Write(b []byte) (int, error) { return len(b), nil }
func (discardInput) Close() error                { return nil }

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

// testClaude is a Claude harness whose generated MCP config lands in dir, fed
// by an empty baked kit mcp-servers file — so Validate passes hermetically.
func testClaude(t *testing.T, dir string) Claude {
	t.Helper()
	kit := filepath.Join(dir, "kit-mcp-servers.json")
	if err := os.WriteFile(kit, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Claude{MCPConfigPath: filepath.Join(dir, "mcp.json"), KitMCPServersPath: kit}
}

func newWL(t *testing.T, dir string, f *fakeSpawner) (*Workload, *recordHandle) {
	t.Helper()
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Harness: testClaude(t, dir), Spawner: f}, nil)
	return w, &recordHandle{}
}

func TestRunTeardownCancel(t *testing.T) {
	dir := t.TempDir()
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
	c := testClaude(t, dir)
	mcp := c.MCPConfigPath
	in := newPipeInput()
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }, in: in}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Harness: c, Spawner: f}, nil)
	h := &recordHandle{}
	if err := runEpisodes(w, h, 1); err != nil {
		t.Fatal(err)
	}
	if f.bin != "claude" {
		t.Errorf("bin: want claude, got %q", f.bin)
	}
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config"}
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
	if got := in.next(t); got != "do the thing" {
		t.Errorf("stdin prompt = %q", got)
	}
}

func TestRunSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{err: os.ErrPermission}
	w, h := newWL(t, dir, f)
	if err := runEpisodes(w, h, 1); err == nil {
		t.Fatal("Run: want spawn error, got nil")
	}
	if len(h.got) != 0 {
		t.Fatalf("activities: want none (spawn failed before Running), got %v", h.got)
	}
}

// TestNewDefaultsMCPConfigPath guards that the production default MCP config
// path is the harness's generated per-run file (COV-240), not the baked
// /etc/claude-code/mcp.json.
func TestNewDefaultsMCPConfigPath(t *testing.T) {
	w := New(Config{WorkDir: t.TempDir(), Prompt: "x"}, nil)
	if c, ok := w.cfg.Harness.(Claude); !ok || c.MCPConfigPath != "" || c.KitMCPServersPath != "" || c.SettingsPath != "" || c.CLIVersion != nil {
		t.Fatalf("default Harness = %#v; want Claude{}", w.cfg.Harness)
	}
	_, args, _ := w.cfg.Harness.Command(Episode{})
	if !slices.Contains(args, "/dev/shm/cove-agent-mcp.json") {
		t.Fatalf("default harness argv lacks the generated MCP config: %q", args)
	}
}

// TestRunFailsLoudOnMissingMCPConfig guards COV-190: when the --mcp-config file
// can't be generated (here: its directory is missing), Run must fail before
// spawning the agent (never launch a toolless claude), not proceed. Verified by an error return AND the spawner never being
// invoked (f.bin stays empty; no Running reported).
func TestRunFailsLoudOnMissingMCPConfig(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Harness: Claude{MCPConfigPath: filepath.Join(dir, "no-such-dir", "mcp.json"), KitMCPServersPath: testClaude(t, dir).KitMCPServersPath}, Spawner: f}, nil)
	h := &recordHandle{}
	err := runEpisodes(w, h, 1)
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

// scriptedCall records one Spawn call's arguments and its stdin.
type scriptedCall struct {
	bin, dir string
	args     []string
	env      []string
	in       *pipeInput
}

// scriptedSpawner scripts each call's stdout: call i's Wait writes lines[i]
// (if present) and returns errs[i] (nil if absent).
type scriptedSpawner struct {
	mu    sync.Mutex
	lines [][]string // per call: stdout lines
	errs  []error    // per call: Wait's result
	dir   string
	calls []scriptedCall
}

func (f *scriptedSpawner) Spawn(_ context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	in := newPipeInput()
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, scriptedCall{bin: bin, args: append([]string(nil), args...), dir: dir, env: append([]string(nil), env...), in: in})
	f.mu.Unlock()
	return scriptedProc{in: in, wait: func() error {
		if i < len(f.lines) && stdout != nil {
			for _, l := range f.lines[i] {
				io.WriteString(stdout, l+"\n")
			}
		}
		if i < len(f.errs) {
			return f.errs[i]
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

// TestRunResumesOnWake: a turn, then a Wake, then a second turn → two spawns,
// the 2nd has --continue.
func TestRunResumesOnWake(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir}
	w := New(Config{WorkDir: dir, Prompt: "do it", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- runEpisodes(w, h, 2) }()
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
	if got := strings.Join(f.calls[1].args[:6], " "); got != "-p --continue --input-format stream-json --output-format stream-json" {
		t.Fatalf("2nd turn argv prefix = %q", got)
	}
}

// TestRunCtxCancelWhileWaiting: cancel ctx while waiting for a wake → Run
// returns ctx.Err().
func TestRunCtxCancelWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f}, nil)
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
	return New(Config{WorkDir: dir, Prompt: "p", Resident: true, MaxWait: maxWait, Harness: testClaude(t, dir), Spawner: f}, nil)
}

// runAsync starts Run in a goroutine and returns its result channel.
// stopHandle wraps a Handle and cancels the run once Waiting has been reported
// n times: every episode ends in Waiting, so this is how a test ends a run
// after n episodes.
type stopHandle struct {
	covemaster.Handle
	n      int
	cancel context.CancelFunc
	mu     sync.Mutex
	seen   int
}

func (s *stopHandle) Report(a covemaster.Activity) {
	s.Handle.Report(a)
	if a != covemaster.Waiting {
		return
	}
	s.mu.Lock()
	s.seen++
	if s.seen == s.n {
		s.cancel()
	}
	s.mu.Unlock()
}

// runEpisodes runs w for n episodes and returns Run's error (nil for the
// cancel that ends it).
func runEpisodes(w *Workload, h covemaster.Handle, n int) error {
	return <-runAsyncEpisodes(w, h, n)
}

// runAsyncEpisodes is runEpisodes in the background.
func runAsyncEpisodes(w *Workload, h covemaster.Handle, n int) chan error {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		defer cancel()
		err := w.Run(ctx, &stopHandle{Handle: h, n: n, cancel: cancel})
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		out <- err
	}()
	return out
}

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

// TestResidentWaitsAfterEveryOutcome: a resident turn, clean or crashed,
// reports Waiting and keeps waiting (no Done).
func TestResidentWaitsAfterEveryOutcome(t *testing.T) {
	for name, exitErr := range map[string]error{"clean": nil, "crashed": errors.New("exit status 1")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := &fakeSpawner{proc: scriptedProc{wait: func() error { return exitErr }}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := New(Config{WorkDir: dir, Prompt: "p", Resident: true, Harness: testClaude(t, dir), Spawner: f}, nil)
			h := &recordHandle{}
			done := runAsync(ctx, w, h)
			waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
			assertBlocked(t, done, 150*time.Millisecond)
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
	f := &scriptedSpawner{dir: dir}
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
	if hasArg(f.calls[0].args, "--continue") {
		t.Fatalf("1st turn: want no --continue, got %v", f.calls[0].args)
	}
	if got := f.calls[0].in.next(t); got != "p" {
		t.Fatalf("1st turn prompt = %q; want the original prompt", got)
	}
	for _, c := range f.calls[1:] {
		if !hasArg(c.args, "--continue") {
			t.Fatalf("resumed turn missing --continue: %v", c.args)
		}
		if got := c.in.next(t); got != residentResumePrompt {
			t.Fatalf("resumed turn prompt = %q; want residentResumePrompt", got)
		}
	}
}

func TestRunForwardsStdoutLinesWithTurns(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir,
		lines: [][]string{{`{"type":"system"}`, `{"type":"result"}`}, {`{"type":"assistant"}`}}}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := runAsyncEpisodes(w, h, 2)
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
	f := &fakeSpawner{}
	f.proc = scriptedProc{wait: func() error { io.WriteString(f.stdout, `{"no":"newline"}`); return nil }}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	if err := runEpisodes(w, h, 1); err != nil {
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
		lines: [][]string{{`{"type":"system"}`, `{"type":"result"}`}}}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f, StreamLogPath: logPath}, nil)
	h := &recordHandle{}
	if err := runEpisodes(w, h, 1); err != nil {
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
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return []string{"PATH=/bin"} }}}, nil)
	h := &recordHandle{}
	if err := runEpisodes(w, h, 1); err != nil {
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
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := runEpisodes(w, h, 1); err != nil {
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

// Across turns (turn → wake → turn → wake → turn) each spawn gets the
// connector current at its start: a change between turns reaches the next
// spawn's env and is reported once; an unchanged one is not re-reported.
func TestRunRefreshesConnectorAcrossTurns(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir}
	a := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "OLD": "1"}}
	b := snippet.Connector{Env: map[string]string{"GH_HOST": "{base}/gh"}}
	src := &seqSource{connectors: []snippet.Connector{a, b, b}}
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return []string{"PATH=/bin"} }}}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- runEpisodes(w, h, 3) }()
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
	c := testClaude(t, dir)
	mcp := c.MCPConfigPath
	cdir := filepath.Join(dir, "context")
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	b := &sessionctx.Bundle{Core: "CORE", Files: map[string]string{"INDEX.md": "I"}}
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Harness: c, Spawner: f, Context: b, ContextDir: cdir}, nil)
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config",
		"--append-system-prompt-file", filepath.Join(cdir, "CORE.md"), "--system-prompt-snapshot", "off"}
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
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o644)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f,
		Context: &sessionctx.Bundle{Core: "C"}, ContextDir: filepath.Join(blocker, "context")}, nil)
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
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
	cdir := filepath.Join(dir, "context")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "CORE.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := New(Config{WorkDir: dir, Prompt: "p", Harness: testClaude(t, dir), Spawner: f, ContextDir: cdir}, nil)
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cdir); !os.IsNotExist(err) {
		t.Fatalf("stale context dir must be removed, stat err = %v", err)
	}
}

// Before each later spawn the context is refreshed; that episode's first
// message carries the new-episode notice when it changed.
func TestRunRefreshesContextPerEpisode(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, "context")
	f := &scriptedSpawner{dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	one, two := compileRole("ONE"), compileRole("TWO")
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f,
		Resident: true, SessionKind: "standing", ConversationMarker: filepath.Join(dir, ".cove-conversation"), Context: &one, ContextDir: cdir,
		ContextSource: &seqContext{bundles: []sessionctx.Bundle{one, two}}}, nil)
	h := &recordHandle{}
	done := runAsync(ctx, w, h)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 2 })
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.calls[0].in.next(t); got != "p" {
		t.Fatalf("1st episode = %q; want the prompt alone", got)
	}
	want := standingResumePrompt + "\n\nSession context changed (role) since your last turn — your system prompt is current; re-open any leaf you rely on."
	if got := f.calls[1].in.next(t); got != want {
		t.Fatalf("2nd episode = %q\nwant %q", got, want)
	}
	if core, _ := os.ReadFile(filepath.Join(cdir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatalf("CORE.md not refreshed: %s", core)
	}
}

// A wake written into a live episode carries the live notice, once for a
// burst of coalesced wakes.
func TestRunWakeIntoLiveEpisodeCarriesNotice(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	one, two := compileRole("ONE"), compileRole("TWO")
	w := streamWL(t, dir, s, func(c *Config) {
		c.Context, c.ContextDir = &one, filepath.Join(dir, "context")
		c.ContextSource = &seqContext{bundles: []sessionctx.Bundle{one, two}}
	})
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	for i := 0; i < 3; i++ {
		w.Control(covemaster.Control{Kind: covemaster.Wake})
		time.Sleep(5 * time.Millisecond)
	}
	p.in.noMessage(t, 50*time.Millisecond)
	p.emit(lnResult)
	want := resumePrompt + "\n\nSession context changed (role) — re-read /agent-data/context/CORE.md now; your system prompt catches up at your next episode."
	if got := p.in.next(t); got != want {
		t.Fatalf("delivered %q\nwant %q", got, want)
	}
	p.in.noMessage(t, 50*time.Millisecond)
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestResumeTextPerKind(t *testing.T) {
	for kind, want := range map[string]string{"standing": standingResumePrompt, "personal": residentResumePrompt, "": resumePrompt, "ephemeral": resumePrompt} {
		w := New(Config{SessionKind: kind, Resident: kind == "standing" || kind == "personal"}, nil)
		if got := w.resumeText(); got != want {
			t.Errorf("%q: resume = %q", kind, got)
		}
	}
}

// The first episode refreshes too (a cove-master restart, or edits made while
// the kit built), silently: its prompt carries no notice.
func TestRunRefreshesFirstEpisodeSilently(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, "context")
	f := &scriptedSpawner{dir: dir}
	one, two := compileRole("ONE"), compileRole("TWO")
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f,
		Context: &one, ContextDir: cdir, ContextSource: &seqContext{bundles: []sessionctx.Bundle{two}}}, nil)
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[0].in.next(t); got != "p" {
		t.Fatalf("1st episode = %q; want the prompt alone", got)
	}
	if core, _ := os.ReadFile(filepath.Join(cdir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatalf("first episode must start on the refreshed bundle: %s", core)
	}
}

type blockingContext struct{}

func (blockingContext) Fetch(ctx context.Context) (sessionctx.Bundle, error) {
	select {
	case <-ctx.Done():
		return sessionctx.Bundle{}, ctx.Err()
	case <-time.After(2 * time.Second): // unbounded: fail the test, don't hang it
		return sessionctx.Bundle{}, errors.New("fetch was not bounded")
	}
}

// A hung Jam must not hold the first episode for the client's full timeout.
func TestFirstEpisodeFetchIsBounded(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir}
	one := compileRole("ONE")
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: f,
		Context: &one, ContextDir: filepath.Join(dir, "context"), ContextSource: blockingContext{}, ContextFetchTimeout: 30 * time.Millisecond}, nil)
	start := time.Now()
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first episode waited %v on a hung Jam", d)
	}
}

// Ticket studios no longer read worker-result: a crashed turn (non-zero exit,
// no result) waits for a Wake instead of ending the unit.
func TestRunCrashedTurnWaits(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil) // a ticket (non-resident) studio
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.exit <- errors.New("exit status 1")
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatalf("crashed turn did not report Waiting")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v; want it to wait", err)
	case <-time.After(50 * time.Millisecond):
	}
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	p2 := s.next(t)
	if !hasArg(p2.args, "--continue") || p2.in.next(t) != resumePrompt {
		t.Fatalf("resume: args=%v", p2.args)
	}
	cancel()
	<-done
}

// An old worker-result.json in the workspace is never read: it doesn't end
// the unit.
func TestRunNeverReadsWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t)
	p.exit <- nil
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatal("did not wait")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v on a worker-result; want it to wait", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
}
