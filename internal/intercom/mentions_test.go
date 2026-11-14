package intercom

import (
	"errors"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// A mentions part names the participants a message mentions: at most one,
// with a list of ids.
func TestValidatePartsMentions(t *testing.T) {
	ids := []ident.ID{"ses_01j9q3aaaaaaaaaaaaaaaaaaaa", "usr_01j9q3bbbbbbbbbbbbbbbbbbbb"}
	p := MentionsPart(ids)
	if err := validateParts([]Part{p}); err != nil {
		t.Fatalf("one mentions part: %v", err)
	}
	if err := validateParts([]Part{p, p}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two mentions parts: %v, want ErrInvalid", err)
	}
	if err := validateParts([]Part{{Type: MentionsPartType, Data: []byte(`"x"`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed mentions part: %v, want ErrInvalid", err)
	}
	if got := Mentions(Squawk{Parts: []Part{p}}); !slices.Equal(got, ids) {
		t.Fatalf("Mentions = %v, want %v", got, ids)
	}
	if got := Mentions(Squawk{Body: "x"}); got != nil {
		t.Fatalf("no part: Mentions = %v", got)
	}
}
