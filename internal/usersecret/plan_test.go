package usersecret

import (
	"testing"

	"github.com/aethons-tools/cove/internal/secret"
)

func TestPlanPrecedenceAndSources(t *testing.T) {
	val := "lit"
	st := Store{
		Global:  map[string]Source{"g": {Command: []string{"gcmd"}}},
		Minters: map[string]Minter{"m": {GitHub: gh()}},
		Kits: map[string]map[string]Source{
			"cove": {
				"A": {Command: []string{"acmd"}},
				"B": {Global: "g"},
				"C": {Value: &val},
			},
		},
		Local: map[string]map[string]Source{
			"/p/cove": {"A": {Value: &val}}, // overrides Kits["cove"]["A"]
		},
	}
	expand := func(profile string, m Minter, name string) (secret.Spec, error) {
		return secret.Spec{Name: name, Command: []string{"at-mint", profile}}, nil
	}
	got, unresolved, err := st.Plan("cove", "/p/cove", []string{"A", "B", "C", "MISSING"}, expand)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "MISSING" {
		t.Fatalf("unresolved = %v, want [MISSING]", unresolved)
	}
	byName := map[string]secret.Spec{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if !byName["A"].Literal || byName["A"].Value != "lit" { // local override wins
		t.Fatalf("A should resolve from local literal, got %+v", byName["A"])
	}
	if byName["B"].Command[0] != "gcmd" { // global delegation
		t.Fatalf("B should resolve via global to gcmd, got %+v", byName["B"])
	}
	if !byName["C"].Literal {
		t.Fatalf("C should be a literal, got %+v", byName["C"])
	}
}

func TestPlanGlobalIsInert(t *testing.T) {
	// A demand whose name equals a global key but has no kits entry is unresolved,
	// never auto-supplied from global.
	st := Store{
		Global: map[string]Source{"shared-tracker": {Command: []string{"gh"}}},
		Kits:   map[string]map[string]Source{"cove": {}},
	}
	got, unresolved, err := st.Plan("cove", "/p", []string{"shared-tracker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(unresolved) != 1 {
		t.Fatalf("global must be inert: got=%v unresolved=%v", got, unresolved)
	}
}

func TestPlanMintNeedsExpander(t *testing.T) {
	st := Store{
		Minters: map[string]Minter{"m": {GitHub: gh()}},
		Kits:    map[string]map[string]Source{"cove": {"T": {Mint: "m"}}},
	}
	if _, _, err := st.Plan("cove", "/p", []string{"T"}, nil); err == nil {
		t.Fatal("mint: with nil expander must error")
	}
}
