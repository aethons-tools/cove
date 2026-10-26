package ticketkey

import (
	"errors"
	"testing"
)

func TestParseGitHub(t *testing.T) {
	for _, c := range []struct {
		key  string
		repo string
		n    int
		ok   bool
	}{
		{"acme/board#12", "acme/board", 12, true},
		{"Acme-Co/board.go#1", "acme-co/board.go", 1, true}, // canonical: lowercased (F16)
		{"a_b/c-d#999999", "a_b/c-d", 999999, true},
		{"acme/board#007", "acme/board", 7, true},         // canonical: no leading zeros (F16)
		{"#12", "", 0, false},                             // at-cove's bare form is not a Jam key
		{"AET-42", "", 0, false},                          // a Linear key
		{"acme/board#", "", 0, false},                     // no number
		{"acme/board#0", "", 0, false},                    // issue numbers start at 1
		{"acme/board#000", "", 0, false},                  // still zero
		{"acme/board#x1", "", 0, false},                   // not a number
		{"acme#12", "", 0, false},                         // no repo
		{"acme/board/x#12", "", 0, false},                 // a path, not owner/repo
		{"acme/..#12", "", 0, false},                      // would walk the URL path
		{"../board#12", "", 0, false},                     // would walk the URL path
		{"acme/board#12 ", "", 0, false},                  // trailing space
		{"acme/bo ard#12", "", 0, false},                  // space
		{"acme/board#99999999999999999999", "", 0, false}, // overflows int
	} {
		repo, n, err := ParseGitHub(c.key)
		if c.ok != (err == nil) || repo != c.repo || n != c.n {
			t.Errorf("ParseGitHub(%q) = %q, %d, %v; want %q, %d, ok=%v", c.key, repo, n, err, c.repo, c.n, c.ok)
		}
		if err != nil && !errors.Is(err, ErrGitHub) {
			t.Errorf("ParseGitHub(%q) error %v is not ErrGitHub", c.key, err)
		}
		if IsGitHub(c.key) != c.ok {
			t.Errorf("IsGitHub(%q) = %v", c.key, !c.ok)
		}
	}
	if ErrGitHub.Error() != "github ticket key must be owner/repo#<n>" {
		t.Fatalf("ErrGitHub text = %q", ErrGitHub.Error())
	}
}

func TestGitHubBuildsAKeyThatParses(t *testing.T) {
	k := GitHub("acme/board", 7)
	if k != "acme/board#7" {
		t.Fatalf("GitHub = %q", k)
	}
	if repo, n, err := ParseGitHub(k); err != nil || repo != "acme/board" || n != 7 {
		t.Fatalf("round trip = %q %d %v", repo, n, err)
	}
	if k := GitHub("Acme/Board", 7); k != "acme/board#7" {
		t.Fatalf("GitHub with mixed case = %q; want canonical acme/board#7", k)
	}
}

// F16: every spelling of one issue maps to one canonical key (and so one
// ticket channel).
func TestCanonicalMapsVariantsToOneKey(t *testing.T) {
	for _, k := range []string{
		"acme/board#7", "acme/board#007", "Acme/Board#7", "ACME/BOARD#0007",
	} {
		got, err := Canonical(k)
		if err != nil || got != "acme/board#7" {
			t.Errorf("Canonical(%q) = %q, %v; want acme/board#7", k, got, err)
		}
	}
	for _, k := range []string{"AET-42", "#7", "acme/board#0", "acme/..#7"} {
		if got, err := Canonical(k); err == nil || !errors.Is(err, ErrGitHub) || got != "" {
			t.Errorf("Canonical(%q) = %q, %v; want ErrGitHub", k, got, err)
		}
	}
}

func TestValidRepo(t *testing.T) {
	for repo, ok := range map[string]bool{
		"acme/board": true, "a.b/c_d-e": true, "Acme/Board": true,
		"": false, "acme": false, "acme/": false, "/board": false, "acme/board/x": false,
		"acme/..": false, "./board": false, "acme/...": false, "acme/bo ard": false,
	} {
		if err := ValidRepo(repo); (err == nil) != ok {
			t.Errorf("ValidRepo(%q) = %v; want ok=%v", repo, err, ok)
		} else if err != nil && !errors.Is(err, ErrRepo) {
			t.Errorf("ValidRepo(%q) error %v is not ErrRepo", repo, err)
		}
	}
	if ErrRepo.Error() != "github repo must be owner/name" {
		t.Fatalf("ErrRepo text = %q", ErrRepo.Error())
	}
}
