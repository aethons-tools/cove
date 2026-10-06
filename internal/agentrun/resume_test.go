package agentrun

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// conversationWL is a Workload of kind whose conversation marker lives at
// marker.
func conversationWL(t *testing.T, dir, kind, marker string, f *scriptedSpawner) *Workload {
	t.Helper()
	return New(Config{WorkDir: dir, Prompt: "p", SessionKind: kind, Resident: kind != "ephemeral",
		ConversationMarker: marker, Harness: testClaude(t, dir), Spawner: f}, nil)
}

// A standing session restarted over its persisted /agent-data (marker
// present) resumes: its first episode runs --continue with the restart prompt,
// not the original raise prompt (COV-249).
func TestStandingFirstEpisodeResumesWithMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".cove-conversation")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &scriptedSpawner{dir: dir}
	h := &recordHandle{}
	if err := runEpisodes(conversationWL(t, dir, "standing", marker, f), h, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !hasArg(f.calls[0].args, "--continue") {
		t.Fatalf("first episode must --continue: %+v", f.calls)
	}
	if got := f.calls[0].in.next(t); got != standingRestartPrompt {
		t.Fatalf("first prompt = %q; want standingRestartPrompt", got)
	}
}

// assistantLine is a claude stream-json reply: the agent's conversation exists.
const assistantLine = `{"type":"assistant","message":{}}`

// Without a marker a standing session starts fresh with its raise prompt, and
// writes the marker once the agent is seen replying — not merely spawned (or
// initialized), which can fail before any conversation is saved.
func TestStandingFirstEpisodeFreshWritesMarker(t *testing.T) {
	for name, c := range map[string]struct {
		lines []string
		want  bool
	}{
		"no output": {nil, false},
		"init only": {[]string{`{"type":"system","subtype":"init"}`}, false},
		"replied":   {[]string{`{"type":"system","subtype":"init"}`, assistantLine}, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, ".cove-conversation")
			f := &scriptedSpawner{dir: dir, lines: [][]string{c.lines}}
			if err := runEpisodes(conversationWL(t, dir, "standing", marker, f), &recordHandle{}, 1); err != nil {
				t.Fatal(err)
			}
			if hasArg(f.calls[0].args, "--continue") {
				t.Fatalf("fresh first episode must not --continue: %v", f.calls[0].args)
			}
			if got := f.calls[0].in.next(t); got != "p" {
				t.Fatalf("first prompt = %q; want the raise prompt", got)
			}
			if _, err := os.Stat(marker); (err == nil) != c.want {
				t.Fatalf("marker written = %v, want %v", err == nil, c.want)
			}
		})
	}
}

// A resumed episode that fails before the agent ever replies (e.g. no
// conversation to continue) drops the marker and is retried once, fresh, with
// the raise prompt.
func TestStandingResumeFailureFallsBackFresh(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".cove-conversation")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &scriptedSpawner{dir: dir, errs: []error{errors.New("exit status 1")}, lines: [][]string{nil, {assistantLine}}}
	h := &recordHandle{}
	if err := runEpisodes(conversationWL(t, dir, "standing", marker, f), h, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("want the failed resume and one fresh retry; spawns = %d", len(f.calls))
	}
	if !hasArg(f.calls[0].args, "--continue") || hasArg(f.calls[1].args, "--continue") {
		t.Fatalf("args: resume %v, retry %v", f.calls[0].args, f.calls[1].args)
	}
	f.calls[0].in.next(t)
	if got := f.calls[1].in.next(t); got != "p" {
		t.Fatalf("retry prompt = %q; want the raise prompt", got)
	}
	if h.count(covemaster.Waiting) != 1 {
		t.Fatalf("the failed resume must not report Waiting; got %v", h.got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the fresh retry replied: the marker must be written again")
	}
}

// The fallback is once only, and never for a resume that replied before
// failing (its conversation exists).
func TestStandingResumeFallbackOnceAndOnlyWithoutReply(t *testing.T) {
	for name, c := range map[string]struct {
		lines  [][]string
		errs   []error
		spawns int
	}{
		"fresh retry also fails": {nil, []error{errors.New("x"), errors.New("y")}, 2},
		"replied then failed":    {[][]string{{assistantLine}}, []error{errors.New("x")}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, ".cove-conversation")
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			f := &scriptedSpawner{dir: dir, lines: c.lines, errs: c.errs}
			if err := runEpisodes(conversationWL(t, dir, "standing", marker, f), &recordHandle{}, 1); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != c.spawns {
				t.Fatalf("spawns = %d, want %d", len(f.calls), c.spawns)
			}
		})
	}
}

// The stream log is rotated at Run start once it exceeds the cap: the old
// content moves to <path>.1 (one kept), a fresh file continues.
func TestStreamLogRotatesOverCap(t *testing.T) {
	defer func(c int64) { streamLogCap = c }(streamLogCap)
	streamLogCap = 8
	for name, c := range map[string]struct {
		old     string
		rotated bool
	}{"over cap": {"0123456789", true}, "under cap": {"0123", false}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "agent-stream.jsonl")
			if err := os.WriteFile(log, []byte(c.old), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(log+".1", []byte("older"), 0o600); err != nil {
				t.Fatal(err)
			}
			f := &scriptedSpawner{dir: dir, lines: [][]string{{"x"}}}
			w := New(Config{WorkDir: dir, Prompt: "p", StreamLogPath: log, Harness: testClaude(t, dir), Spawner: f}, nil)
			if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
				t.Fatal(err)
			}
			cur, _ := os.ReadFile(log)
			prev, _ := os.ReadFile(log + ".1")
			if c.rotated && (string(prev) != c.old || string(cur) != "x\n") {
				t.Fatalf("rotated: .1=%q cur=%q", prev, cur)
			}
			if !c.rotated && (string(prev) != "older" || string(cur) != c.old+"x\n") {
				t.Fatalf("not rotated: .1=%q cur=%q", prev, cur)
			}
		})
	}
}

// Ephemeral and personal sessions never continue a prior conversation, marker
// or not, and never write one.
func TestNonStandingNeverContinues(t *testing.T) {
	for _, kind := range []string{"", "ephemeral", "personal"} {
		for _, present := range []bool{false, true} {
			dir := t.TempDir()
			marker := filepath.Join(dir, ".cove-conversation")
			if present {
				if err := os.WriteFile(marker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f := &scriptedSpawner{dir: dir}
			if err := runEpisodes(conversationWL(t, dir, kind, marker, f), &recordHandle{}, 1); err != nil {
				t.Fatal(err)
			}
			if hasArg(f.calls[0].args, "--continue") {
				t.Fatalf("%q (marker=%v): first episode must not --continue", kind, present)
			}
			if got := f.calls[0].in.next(t); got != "p" {
				t.Fatalf("%q: first prompt = %q", kind, got)
			}
			if _, err := os.Stat(marker); !present && err == nil {
				t.Fatalf("%q: must not write a marker", kind)
			}
		}
	}
}

// A resumed standing session's later wakes use the standing resume text.
func TestStandingResumedThenWakes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".cove-conversation")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &scriptedSpawner{dir: dir}
	w := conversationWL(t, dir, "standing", marker, f)
	h := &recordHandle{}
	done := runAsyncEpisodes(w, h, 2)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
	f.calls[0].in.next(t)
	if got := f.calls[1].in.next(t); got != standingResumePrompt {
		t.Fatalf("wake prompt = %q", got)
	}
}
