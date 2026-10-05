package agentrun

import (
	"fmt"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// Golden: claude's argv and stdin bytes as they were before the Harness seam
// (COV-235). These literals must never change in a pure refactor.
// COV-240 deliberately changed ONE thing: --mcp-config now names the harness's
// generated per-run file (messaging + kit servers) instead of the baked
// /etc/claude-code/mcp.json. COV-238 pins that the default model-spec changes
// nothing here. COV-239 takes the permission policy from the spec: no spec,
// a spec without policy.mode, and claude-default (bypassPermissions) all keep
// --dangerously-skip-permissions byte-identically; any other mode and the
// allow/deny rules are rendered (TestGoldenClaudePolicyArgv).

const goldenMCP = "/dev/shm/cove-agent-mcp.json"
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
	// COV-238: a cove under the seeded claude-default spec launches exactly as
	// one with no spec delivered — same argv, no extra env.
	def := modelspec.Default("anthropic")
	noMode := modelspec.Default("anthropic")
	noMode.Policy = modelspec.Policy{}
	for _, spec := range []*modelspec.Spec{nil, &def, &noMode} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s/spec=%v", c.name, spec != nil), func(t *testing.T) {
				bin, args, env := goldenCommand(Episode{Continued: c.continued, ContextCore: c.core, Spec: spec})
				if bin != "claude" {
					t.Errorf("bin = %q, want claude", bin)
				}
				if !slices.Equal(args, c.want) {
					t.Errorf("argv:\n got %q\nwant %q", args, c.want)
				}
				if len(env) != 0 {
					t.Errorf("extra env = %v, want none", env)
				}
			})
		}
	}
}

func TestGoldenClaudePolicyArgv(t *testing.T) {
	head := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	tail := []string{"--mcp-config", goldenMCP, "--strict-mcp-config"}
	cases := []struct {
		name   string
		policy modelspec.Policy
		want   []string // between head and tail
	}{
		{"default+allow+deny", modelspec.Policy{Mode: "default", Allow: []string{"Read", "Bash(git *)"}, Deny: []string{"WebFetch", "Bash(rm -rf *)"}},
			[]string{"--permission-mode", "default",
				"--allowedTools=Read", "--allowedTools=Bash(git *)",
				"--disallowedTools=WebFetch", "--disallowedTools=Bash(rm -rf *)"}},
		{"plan", modelspec.Policy{Mode: "plan"}, []string{"--permission-mode", "plan"}},
		// bypassPermissions keeps today's flag; a deny list still applies under it.
		{"bypass+deny", modelspec.Policy{Mode: "bypassPermissions", Deny: []string{"Bash"}},
			[]string{"--dangerously-skip-permissions", "--disallowedTools=Bash"}},
		// No mode = today's bypass; the rules still apply.
		{"rules only", modelspec.Policy{Allow: []string{"Edit"}},
			[]string{"--dangerously-skip-permissions", "--allowedTools=Edit"}},
		// A rule that looks like a flag stays one argv element, never a flag.
		{"flag-like rule", modelspec.Policy{Mode: "dontAsk", Deny: []string{"--dangerously-skip-permissions"}},
			[]string{"--permission-mode", "dontAsk", "--disallowedTools=--dangerously-skip-permissions"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := modelspec.Default("anthropic")
			spec.Policy = c.policy
			_, args, _ := goldenCommand(Episode{Spec: &spec})
			want := slices.Concat(head, c.want, tail)
			if !slices.Equal(args, want) {
				t.Errorf("argv:\n got %q\nwant %q", args, want)
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

// goldenCommand / goldenEncode bind the golden data to the code under test:
// the default harness New installs.
func goldenCommand(ep Episode) (string, []string, map[string]string) {
	return New(Config{}, nil).cfg.Harness.Command(ep)
}

func goldenEncode(text string) []byte { return New(Config{}, nil).cfg.Harness.EncodeInput(text) }
