package agentrun

import (
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

// Without a marker a standing session starts fresh with its raise prompt, and
// its first episode writes the marker so a later restart resumes.
func TestStandingFirstEpisodeFreshWritesMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".cove-conversation")
	f := &scriptedSpawner{dir: dir}
	h := &recordHandle{}
	if err := runEpisodes(conversationWL(t, dir, "standing", marker, f), h, 1); err != nil {
		t.Fatal(err)
	}
	if hasArg(f.calls[0].args, "--continue") {
		t.Fatalf("fresh first episode must not --continue: %v", f.calls[0].args)
	}
	if got := f.calls[0].in.next(t); got != "p" {
		t.Fatalf("first prompt = %q; want the raise prompt", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker not written: %v", err)
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
