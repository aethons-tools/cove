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
// allow/deny rules are rendered (TestGoldenClaudePolicyArgv), and a non-bypass
// mode always also allows the messaging tools.

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
	// one with no spec delivered — same argv, no extra env — except that, since
	// COV-242, its plugins are enabled through the per-run --settings file
	// (inserted after --strict-mcp-config) rather than the sealed managed
	// settings. COV-245 moved claude-default's preferences into that same
	// file (its content, not the argv: TestClaudeDefaultSettingsFile). A spec
	// with no plugins and no settings passes none.
	def := modelspec.Default("anthropic")
	noMode := modelspec.Default("anthropic")
	noMode.Name = "no-mode"
	noMode.Policy = modelspec.Policy{}
	noPlugins := modelspec.Default("anthropic")
	noPlugins.Name = "no-plugins"
	noPlugins.Claude.Plugins, noPlugins.Claude.Settings = nil, nil
	prefsOnly := modelspec.Default("anthropic")
	prefsOnly.Name = "prefs-only"
	prefsOnly.Claude.Plugins = nil
	for _, spec := range []*modelspec.Spec{nil, &def, &noMode, &noPlugins, &prefsOnly} {
		variant := "none"
		if spec != nil {
			variant = fmt.Sprintf("%s(mode=%q)", spec.Name, spec.Policy.Mode)
		}
		for _, c := range cases {
			want := c.want
			if spec != nil && (len(spec.Claude.Plugins) > 0 || len(spec.Claude.Settings) > 0) {
				i := slices.Index(want, "--strict-mcp-config") + 1
				want = slices.Concat(want[:i], []string{"--settings", "/dev/shm/cove-agent-settings.json"}, want[i:])
			}
			t.Run(fmt.Sprintf("%s/spec=%s", c.name, variant), func(t *testing.T) {
				bin, args, env := goldenCommand(Episode{Continued: c.continued, ContextCore: c.core, Spec: spec})
				if bin != "claude" {
					t.Errorf("bin = %q, want claude", bin)
				}
				if !slices.Equal(args, want) {
					t.Errorf("argv:\n got %q\nwant %q", args, want)
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
	// Every non-bypass mode always allows the guaranteed messaging server's
	// tools, ahead of the spec's own rules.
	always := []string{"--allowedTools=mcp__messaging"}
	cases := []struct {
		name   string
		policy modelspec.Policy
		want   []string // between head and tail
	}{
		{"default+allow+deny", modelspec.Policy{Mode: "default", Allow: []string{"Read", "Bash(git *)"}, Deny: []string{"WebFetch", "Bash(rm -rf *)"}},
			slices.Concat([]string{"--permission-mode=default"}, always, []string{
				"--allowedTools=Read", "--allowedTools=Bash(git *)",
				"--disallowedTools=WebFetch", "--disallowedTools=Bash(rm -rf *)"})},
		{"acceptEdits", modelspec.Policy{Mode: "acceptEdits"}, slices.Concat([]string{"--permission-mode=acceptEdits"}, always)},
		// bypassPermissions keeps today's flag; a deny list still applies under it.
		{"bypass+deny", modelspec.Policy{Mode: "bypassPermissions", Deny: []string{"Bash"}},
			[]string{"--dangerously-skip-permissions", "--disallowedTools=Bash"}},
		// No mode = today's bypass; the rules still apply.
		{"rules only", modelspec.Policy{Allow: []string{"Edit"}},
			[]string{"--dangerously-skip-permissions", "--allowedTools=Edit"}},
		// A rule that looks like a flag stays one argv element, never a flag.
		{"flag-like rule", modelspec.Policy{Mode: "dontAsk", Deny: []string{"--dangerously-skip-permissions"}},
			slices.Concat([]string{"--permission-mode=dontAsk"}, always, []string{"--disallowedTools=--dangerously-skip-permissions"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := modelspec.Default("anthropic")
			spec.Policy = c.policy
			spec.Claude.Plugins, spec.Claude.Settings = nil, nil // policy flags only (these add --settings)
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
