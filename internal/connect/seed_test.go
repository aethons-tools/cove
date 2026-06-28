package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestSeedLoopWorkspaceFreshRunsSetup(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-unseeded\n"}}}
	if err := SeedLoopWorkspace(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "git clone https://x ."); err != nil {
		t.Fatal(err)
	}
	// Three calls: sentinel probe (Output), write env, run seed.
	if len(f.Calls) != 3 {
		t.Fatalf("want 3 calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[2].Args[len(f.Calls[2].Args)-1]
	if !strings.Contains(last, "find /home/agent/workspace -mindepth 1 -delete") {
		t.Fatalf("seed must clear the workspace first: %q", last)
	}
	if !strings.Contains(last, "git clone https://x . && touch /agent-data/.cove-loop-seeded") {
		t.Fatalf("seed must run setup then write the sentinel on success: %q", last)
	}
}

func TestSeedLoopWorkspaceSkipsWhenSeeded(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-seeded\n"}}}
	if err := SeedLoopWorkspace(f, rawTarget(), nil, "git clone x ."); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 { // only the sentinel probe
		t.Fatalf("a present sentinel must skip seeding: %+v", f.Calls)
	}
}

func TestSeedLoopWorkspaceEmptyNoop(t *testing.T) {
	f := &runner.Fake{}
	if err := SeedLoopWorkspace(f, rawTarget(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("empty setup must do nothing: %+v", f.Calls)
	}
}

func TestResetLoopWorkspace(t *testing.T) {
	f := &runner.Fake{}
	if err := ResetLoopWorkspace(f, rawTarget()); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[0].Args[len(f.Calls[0].Args)-1]
	if !strings.Contains(last, "rm -f /agent-data/.cove-loop-seeded") ||
		!strings.Contains(last, "find /home/agent/workspace -mindepth 1 -delete") {
		t.Fatalf("reset must remove the sentinel and clear the workspace: %q", last)
	}
}
