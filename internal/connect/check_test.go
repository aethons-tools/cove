package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunCheckTriggered(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-trigger\n"}}}
	got, err := RunCheck(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "test -e x")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("marker present => should trigger")
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret leaked onto argv: %v", c.Args)
			}
		}
	}
	// Two calls: write env, then run the check (Output).
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && if test -e x") {
		t.Fatalf("check command wrong: %q", last)
	}
}

func TestRunCheckNotTriggered(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: ""}}}
	got, err := RunCheck(f, rawTarget(), nil, "false")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("no marker => no trigger")
	}
}

func TestRunCheckConnectionError(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 255}}}}
	if _, err := RunCheck(f, rawTarget(), nil, "x"); err == nil {
		t.Fatal("ssh connection failure must error")
	}
}
