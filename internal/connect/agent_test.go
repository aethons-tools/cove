package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunAgentInjectsAndRunsHeadless(t *testing.T) {
	f := &runner.Fake{}
	if err := RunAgent(f, rawTarget(), map[string]string{"ANTHROPIC_API_KEY": "sk-secret"}, "do the task"); err != nil {
		t.Fatal(err)
	}
	// Two ssh calls: write env to tmpfs, then run the agent.
	if len(f.Calls) != 2 {
		t.Fatalf("want 2 ssh calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, "sk-secret") {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && exec claude -p 'do the task'") {
		t.Fatalf("headless agent command wrong: %q", last)
	}
	// No interactive auth probe is run.
	if strings.Contains(last, "claude auth") {
		t.Fatalf("agent run must not touch auth: %q", last)
	}
}

func TestRunAgentShellQuotesPrompt(t *testing.T) {
	f := &runner.Fake{}
	if err := RunAgent(f, rawTarget(), nil, "a'b"); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	// shellQuote renders the embedded ' as '\'', so a'b becomes 'a'\''b' — one
	// argument to the remote shell.
	if !strings.Contains(last, `claude -p 'a'\''b'`) {
		t.Fatalf("prompt not shell-quoted as a single argument: %q", last)
	}
}
