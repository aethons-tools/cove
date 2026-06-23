package kit

import "testing"

// env builds a lookup func from a map. Shared by all kit package tests.
func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestSubstituteBraced(t *testing.T) {
	got := Substitute("hi ${NAME}!", env(map[string]string{"NAME": "bob"}))
	if got != "hi bob!" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteBare(t *testing.T) {
	got := Substitute("path=$HOME/x", env(map[string]string{"HOME": "/h"}))
	if got != "path=/h/x" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteUnsetBecomesEmpty(t *testing.T) {
	got := Substitute("[${MISSING}]", env(nil))
	if got != "[]" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteLoneDollar(t *testing.T) {
	got := Substitute("cost is $ 5 and $-", env(nil))
	if got != "cost is $ 5 and $-" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteInvalidBraceLeftLiteral(t *testing.T) {
	got := Substitute("${1bad}", env(map[string]string{"1bad": "x"}))
	if got != "${1bad}" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteBareStopsAtNonIdent(t *testing.T) {
	got := Substitute("$FOO-bar", env(map[string]string{"FOO": "f"}))
	if got != "f-bar" {
		t.Fatalf("got %q", got)
	}
}
