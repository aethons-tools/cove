package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadStringIsValue(t *testing.T) {
	s, err := Load(write(t, "GITHUB_TOKEN: ghp_abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := s["GITHUB_TOKEN"]
	if !ok || e.Value != "ghp_abc" || len(e.Command) != 0 {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestLoadNumericScalarIsStringValue(t *testing.T) {
	s, err := Load(write(t, "PIN: 1234\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s["PIN"].Value != "1234" {
		t.Fatalf("entry = %+v", s["PIN"])
	}
}

func TestLoadArrayIsCommand(t *testing.T) {
	s, err := Load(write(t, `TOK: ["op", "read", "x"]`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := s["TOK"]
	if e.Value != "" || len(e.Command) != 3 || e.Command[0] != "op" || e.Command[2] != "x" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("store = %+v", s)
	}
}

func TestLoadInvalidYAMLErrors(t *testing.T) {
	if _, err := Load(write(t, "a: [unterminated\n")); err == nil {
		t.Fatal("expected error on invalid YAML")
	}
}

func TestLoadMappingValueErrors(t *testing.T) {
	_, err := Load(write(t, "GITHUB_TOKEN:\n  nested: bad\n"))
	if err == nil {
		t.Fatal("expected error on a mapping value")
	}
}

func TestLoadScalarArrayElementCoerces(t *testing.T) {
	// yaml.v3 coerces a scalar element into its string form, mirroring how a
	// scalar value coerces (e.g. PIN: 1234 -> "1234"). Accepted, not an error.
	s, err := Load(write(t, `TOK: ["op", 5]`+"\n"))
	if err != nil {
		t.Fatalf("scalar array elements should coerce, not error: %v", err)
	}
	if len(s["TOK"].Command) != 2 || s["TOK"].Command[1] != "5" {
		t.Fatalf("entry = %+v", s["TOK"])
	}
}

func TestLoadNestedArrayElementErrors(t *testing.T) {
	// A non-scalar element (a nested list) cannot be a string -> error.
	_, err := Load(write(t, "TOK:\n  - op\n  - [1, 2]\n"))
	if err == nil {
		t.Fatal("expected error on a non-scalar (nested) array element")
	}
}
