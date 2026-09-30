package usersecret

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/secret"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFlat_ParsesCredentials(t *testing.T) {
	p := writeTemp(t, `
global:
  gh: { command: ["gh", "auth", "token"] }
credentials:
  jam-db: { value: "pw" }
  git-pat: { global: gh }
`)
	st, err := LoadFlat(p)
	if err != nil {
		t.Fatalf("LoadFlat: %v", err)
	}
	if len(st.Credentials) != 2 {
		t.Fatalf("want 2 credentials, got %d", len(st.Credentials))
	}
	if st.Credentials["jam-db"].Value == nil || *st.Credentials["jam-db"].Value != "pw" {
		t.Fatalf("jam-db value not parsed: %+v", st.Credentials["jam-db"])
	}
}

func TestLoadFlat_MissingFileIsEmpty(t *testing.T) {
	st, err := LoadFlat(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("missing file should be empty, not error: %v", err)
	}
	if len(st.Credentials) != 0 {
		t.Fatalf("want empty store, got %d credentials", len(st.Credentials))
	}
}

func TestLoadFlat_UndefinedGlobalReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  git-pat: { global: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined global reference, got nil")
	}
}

func TestLoadFlat_UndefinedMintReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  anthropic: { mint: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined mint reference, got nil")
	}
}

func TestPlanFlat_ValueAndCommand(t *testing.T) {
	pw := "pw"
	st := Store{Credentials: map[string]Source{
		"jam-db":  {Value: &pw},
		"git-pat": {Command: []string{"gh", "auth", "token"}},
	}}
	specs, unresolved, err := st.PlanFlat([]string{"jam-db", "git-pat"}, nil)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("want none unresolved, got %v", unresolved)
	}
	byName := map[string]secret.Spec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	if !byName["jam-db"].Literal || byName["jam-db"].Value != "pw" {
		t.Fatalf("jam-db spec wrong: %+v", byName["jam-db"])
	}
	if len(byName["git-pat"].Command) != 3 {
		t.Fatalf("git-pat command wrong: %+v", byName["git-pat"])
	}
}

func TestPlanFlat_UnresolvedDemand(t *testing.T) {
	st := Store{Credentials: map[string]Source{}}
	specs, unresolved, err := st.PlanFlat([]string{"absent"}, nil)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(specs) != 0 || len(unresolved) != 1 || unresolved[0] != "absent" {
		t.Fatalf("want absent unresolved, got specs=%v unresolved=%v", specs, unresolved)
	}
}

func TestPlanFlat_MintUsesExpander(t *testing.T) {
	st := Store{
		Minters:     map[string]Minter{"m": {}},
		Credentials: map[string]Source{"anthropic": {Mint: "m"}},
	}
	expand := func(profile string, m Minter, demand string) (secret.Spec, error) {
		return secret.Spec{Name: demand, Command: []string{"at-mint", "anthropic"}}, nil
	}
	specs, _, err := st.PlanFlat([]string{"anthropic"}, expand)
	if err != nil {
		t.Fatalf("PlanFlat: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "anthropic" || specs[0].Command[0] != "at-mint" {
		t.Fatalf("mint expansion wrong: %+v", specs)
	}
}

func TestPlanFlat_MintWithoutExpanderErrors(t *testing.T) {
	st := Store{
		Minters:     map[string]Minter{"m": {}},
		Credentials: map[string]Source{"anthropic": {Mint: "m"}},
	}
	if _, _, err := st.PlanFlat([]string{"anthropic"}, nil); err == nil {
		t.Fatal("want error minting without an expander, got nil")
	}
}
