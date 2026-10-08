package jam

import (
	"slices"
	"strings"
	"testing"
)

func TestParseDestinations(t *testing.T) {
	d, c, err := ParseDestinations(" git=git-pat-cove , anthropic ")
	if err != nil || !slices.Equal(d, []string{"git", "anthropic"}) || c["git"] != "git-pat-cove" || len(c) != 1 {
		t.Fatalf("got %v %v %v", d, c, err)
	}
	if _, c, _ := ParseDestinations("git,anthropic"); c != nil {
		t.Fatalf("no mappings → nil map, got %v", c)
	}
	for _, bad := range []string{"git=", "=pat", "git=a,git=b", "git,git"} {
		if _, _, err := ParseDestinations(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestFormatDestinationsRoundTrip(t *testing.T) {
	in := "git=git-pat-cove,anthropic"
	d, c, _ := ParseDestinations(in)
	if got := FormatDestinations(d, c); got != in {
		t.Fatalf("FormatDestinations = %q", got)
	}
}

func TestValidateCredentials(t *testing.T) {
	known := func(n string) bool { return n == "git-pat-cove" }
	ok := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	if err := ValidateCredentials(ok, known); err != nil {
		t.Fatal(err)
	}
	notAllowed := Scope{Destinations: []string{"anthropic"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	unknown := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "typo"}}
	for _, s := range []Scope{notAllowed, unknown} {
		if ValidateCredentials(s, known) == nil {
			t.Errorf("%+v: want error", s)
		}
	}
}

func TestValidateCredentialsErrorOmitsCredentialName(t *testing.T) {
	err := ValidateCredentials(Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "SECRET-TYPO"}}, func(string) bool { return false })
	if err == nil || strings.Contains(err.Error(), "SECRET-TYPO") {
		t.Fatalf("err = %v; must not echo the credential name", err)
	}
}
