package harbor

import (
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

func TestSecretResolverRunsConfiguredCommand(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "REAL-PAT\n"}}}
	r := NewSecretResolver(f, map[string]secret.Spec{
		"git-pat": {Command: []string{"at-mint", "github"}},
	})
	got, err := r.Resolve("git-pat")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "REAL-PAT" { // secret.Resolve trims the trailing newline
		t.Fatalf("Resolve = %q", got)
	}
}

func TestSecretResolverUnknownName(t *testing.T) {
	r := NewSecretResolver(&runner.Fake{}, nil)
	if _, err := r.Resolve("nope"); err == nil {
		t.Fatal("expected error for unknown credential")
	}
}
