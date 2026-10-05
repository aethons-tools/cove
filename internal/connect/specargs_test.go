package connect

import (
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/runner"
)

func TestSpecArgs(t *testing.T) {
	if SpecArgs(nil) != nil {
		t.Fatal("no model-spec: no extra claude argv (the interactive launch is unchanged)")
	}
	// claude-default's runtime parts change nothing interactively: bypass is
	// the image's interactive default and its settings are the image baseline.
	d := modelspec.Default("")
	d.Claude.Settings = nil
	if got := SpecArgs(&d); got != nil {
		t.Fatalf("claude-default without settings: want no argv, got %q", got)
	}
	s := &modelspec.Spec{
		Model:  modelspec.Choice{ID: "claude-opus-4-8", Effort: "high"},
		Policy: modelspec.Policy{Mode: "acceptEdits", Allow: []string{"Bash(go test:*)"}, Deny: []string{"WebFetch"}},
		Claude: &modelspec.Claude{Provider: "anthropic", Settings: map[string]any{"theme": "light"}},
	}
	want := []string{
		"--model", "claude-opus-4-8", "--effort", "high",
		"--permission-mode=acceptEdits", "--allowedTools=Bash(go test:*)", "--disallowedTools=WebFetch",
		"--settings", `{"theme":"light"}`,
	}
	if got := SpecArgs(s); !slices.Equal(got, want) {
		t.Fatalf("SpecArgs = %q\nwant      %q", got, want)
	}
	// bypassPermissions keeps the image's interactive default (no mode flag),
	// but deny rules still apply.
	s.Policy.Mode = modelspec.ModeBypassPermissions
	if got := SpecArgs(s); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--permission-mode") }) {
		t.Fatalf("bypassPermissions must not add a mode flag: %q", got)
	}
}

// The spec's argv is shell-quoted onto both the resumed and the fresh claude
// launch; a program replacement (--raw) ignores it.
func TestStdinScriptCarriesSpecArgs(t *testing.T) {
	f := &runner.Fake{}
	args := []string{"--model", "claude-opus-4-8", "--settings", `{"a":"it's"}`}
	if err := (StdinScript{R: f, Name: "k", Resume: true, Args: args}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	flags := ` -n 'k cove' '--model' 'claude-opus-4-8' '--settings' '{"a":"it'\''s"}'`
	if !strings.Contains(remote, "exec claude --continue"+flags+";") || !strings.Contains(remote, "done; exec claude"+flags+"; }") {
		t.Fatalf("both launches must carry the spec argv: %q", remote)
	}
	f = &runner.Fake{}
	if err := (StdinScript{R: f, Cmd: "bash", Args: args}).Launch(rawTarget(), map[string]string{"X": "y"}); err != nil {
		t.Fatal(err)
	}
	if remote := f.Calls[1].Args[len(f.Calls[1].Args)-1]; strings.Contains(remote, "--model") {
		t.Fatalf("--raw must ignore the spec argv: %q", remote)
	}
}
