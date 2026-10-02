package studio

import (
	"strings"
	"testing"
)

// A kit no longer carries its name: the registry (or the push) supplies it.
func TestKitWithoutNameParsesAndStoresNoName(t *testing.T) {
	sk, err := ParseStudioKit([]byte("kind: studio\negress: [a.com]\n"))
	if err != nil {
		t.Fatalf("nameless kit refused: %v", err)
	}
	j, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(j), `"name"`) {
		t.Fatalf("canonical JSON must not carry a name: %s", j)
	}
}

// Kit files and stored rows from before the change still parse; the legacy
// name is kept only to check it against the name being pushed, never stored.
func TestLegacyNameAcceptedButNotStored(t *testing.T) {
	sk, err := ParseStudioKit([]byte(`{"kind":"studio","name":"web","egress":["a.com"]}`))
	if err != nil {
		t.Fatalf("legacy stored row refused: %v", err)
	}
	if sk.LegacyName != "web" {
		t.Fatalf("LegacyName = %q", sk.LegacyName)
	}
	j, _ := sk.ToJSON()
	if strings.Contains(string(j), `"name"`) {
		t.Fatalf("legacy name leaked into canonical JSON: %s", j)
	}
}

func TestCheckName(t *testing.T) {
	plain := StudioKit{Kind: Kind}
	if err := plain.CheckName("web"); err != nil {
		t.Errorf("nameless kit pushed as web: %v", err)
	}
	if err := plain.CheckName("not ok!"); err == nil || !strings.Contains(err.Error(), "tag-safe") {
		t.Errorf("non-tag-safe name = %v", err)
	}
	if err := (StudioKit{Kind: Kind, LegacyName: "web"}).CheckName("web"); err != nil {
		t.Errorf("matching legacy name refused: %v", err)
	}
	err := StudioKit{Kind: Kind, LegacyName: "web"}.CheckName("api")
	if err == nil || !strings.Contains(err.Error(), `"web"`) || !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("mismatched legacy name = %v, want an error naming both", err)
	}
}
