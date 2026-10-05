package agentrun

import (
	"slices"
	"testing"
)

// Golden: claude's argv and stdin bytes as they were before the Harness seam
// (COV-235). These literals must never change in a pure refactor.

const goldenMCP = "/etc/claude-code/mcp.json"
const goldenCore = "/agent-data/context/CORE.md"

func TestGoldenClaudeArgv(t *testing.T) {
	cases := []struct {
		name      string
		continued bool
		core      string
		want      []string
	}{
		{"first", false, "", []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--dangerously-skip-permissions", "--mcp-config", goldenMCP, "--strict-mcp-config"}},
		{"continued", true, "", []string{"-p", "--continue", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--dangerously-skip-permissions", "--mcp-config", goldenMCP, "--strict-mcp-config"}},
		{"context", false, goldenCore, []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--dangerously-skip-permissions", "--mcp-config", goldenMCP, "--strict-mcp-config",
			"--append-system-prompt-file", goldenCore, "--system-prompt-snapshot", "off"}},
		{"continued+context", true, goldenCore, []string{"-p", "--continue", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--dangerously-skip-permissions", "--mcp-config", goldenMCP, "--strict-mcp-config",
			"--append-system-prompt-file", goldenCore, "--system-prompt-snapshot", "off"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, args := goldenCommand(c.continued, c.core)
			if bin != "claude" {
				t.Errorf("bin = %q, want claude", bin)
			}
			if !slices.Equal(args, c.want) {
				t.Errorf("argv:\n got %q\nwant %q", args, c.want)
			}
		})
	}
}

func TestGoldenClaudeStdin(t *testing.T) {
	got := string(goldenEncode("line1\nline2 \"q\" <&>"))
	// json.Marshal HTML-escapes <, & and > — part of today's bytes.
	want := `{"type":"user","message":{"role":"user","content":"line1\nline2 \"q\" \u003c\u0026\u003e"}}` + "\n"
	if got != want {
		t.Fatalf("stdin bytes:\n got %q\nwant %q", got, want)
	}
}

// goldenCommand / goldenEncode bind the golden data to the code under test.
func goldenCommand(continued bool, core string) (string, []string) {
	w := New(Config{}, nil)
	w.contextCore = core
	return "claude", w.claudeArgs(continued)
}

func goldenEncode(text string) []byte { return userMessage(text) }
