package modelspec

import (
	"slices"
	"testing"
)

func TestClaudeModelArgs(t *testing.T) {
	if ClaudeModelArgs(nil) != nil || ClaudeModelArgs(&Spec{}) != nil {
		t.Fatal("no model: no flags")
	}
	got := ClaudeModelArgs(&Spec{Model: Choice{ID: "m", Effort: "high"}})
	if !slices.Equal(got, []string{"--model", "m", "--effort", "high"}) {
		t.Fatalf("got %q", got)
	}
}

func TestClaudePolicyArgs(t *testing.T) {
	always := []string{"Edit(x)"}
	for _, tc := range []struct {
		spec     *Spec
		explicit bool
		want     []string
	}{
		{nil, true, []string{"--dangerously-skip-permissions"}},
		{nil, false, nil},
		{&Spec{Policy: Policy{Mode: ModeBypassPermissions, Deny: []string{"WebFetch"}}}, true, []string{"--dangerously-skip-permissions", "--disallowedTools=WebFetch"}},
		{&Spec{Policy: Policy{Mode: ModeBypassPermissions, Deny: []string{"WebFetch"}}}, false, []string{"--disallowedTools=WebFetch"}},
		{&Spec{Policy: Policy{Mode: "acceptEdits", Allow: []string{"Bash(go:*)"}}}, true, []string{"--permission-mode=acceptEdits", "--allowedTools=Edit(x)", "--allowedTools=Bash(go:*)"}},
	} {
		if got := ClaudePolicyArgs(tc.spec, tc.explicit, always); !slices.Equal(got, tc.want) {
			t.Errorf("%+v explicit=%v: got %q want %q", tc.spec, tc.explicit, got, tc.want)
		}
	}
}

func TestClaudeSettingsJSON(t *testing.T) {
	if _, ok := ClaudeSettingsJSON(&Spec{Claude: &Claude{}}); ok {
		t.Fatal("empty settings: nothing")
	}
	if got, ok := ClaudeSettingsJSON(&Spec{Claude: &Claude{Settings: map[string]any{"theme": "light"}}}); !ok || got != `{"theme":"light"}` {
		t.Fatalf("got %q", got)
	}
}
