package jam

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestValidateEntityName(t *testing.T) {
	for _, ok := range []string{"alice", "Alice.B", "linear-acme", "a_b", strings.Repeat("x", 64)} {
		if err := ValidateEntityName(ok); err != nil {
			t.Errorf("ValidateEntityName(%q) = %v, want nil", ok, err)
		}
	}
	bad := []string{
		"", strings.Repeat("x", 65), "a b", "a\tb", "a:b", "a,b", "a*", "a?", "a[b]", `a\b`, "a/b",
		string(ident.New(ident.User)), // a name must not look like an id
	}
	for _, name := range bad {
		if err := ValidateEntityName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateEntityName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestEntryLabel(t *testing.T) {
	if got := (Entry{Name: "alice", Status: StatusLive}).Label(); got != "alice" {
		t.Fatalf("live label = %q", got)
	}
	if got := (Entry{Name: "alice", Status: StatusRemoved}).Label(); got != "alice (removed)" {
		t.Fatalf("removed label = %q", got)
	}
}
