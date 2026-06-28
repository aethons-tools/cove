package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunSetupRunsWhenWorkspaceEmpty(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-empty\n"}}}
	if err := RunSetup(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "git clone https://x ."); err != nil {
		t.Fatal(err)
	}
	// Calls: [0] emptiness probe (Output), [1] write env to tmpfs, [2] run setup.
	if len(f.Calls) != 3 {
		t.Fatalf("want 3 ssh calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[2].Args[len(f.Calls[2].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && git clone https://x .") {
		t.Fatalf("setup not run in workspace: %q", last)
	}
}

func TestRunSetupSkipsWhenWorkspaceNonEmpty(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-nonempty\n"}}}
	if err := RunSetup(f, rawTarget(), nil, "git clone x ."); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 { // only the probe
		t.Fatalf("non-empty workspace must skip setup; calls=%+v", f.Calls)
	}
}

func TestRunSetupEmptyCommandIsNoop(t *testing.T) {
	f := &runner.Fake{}
	if err := RunSetup(f, rawTarget(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("empty setup must do nothing; calls=%+v", f.Calls)
	}
}
