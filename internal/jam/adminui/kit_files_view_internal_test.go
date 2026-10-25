package adminui

import (
	"strings"
	"testing"
)

// hiddenKitYML never lets a packed context's value through: it replaces
// every context value, and falls back to the fixed placeholder alone when it
// cannot be sure.
func TestHiddenKitYML(t *testing.T) {
	const blob = "H4sIAAAAblob"
	for name, tc := range map[string]struct {
		text      string
		whole     bool   // the result is hiddenContext alone
		keep, not string // the result keeps keep and drops not
	}{
		"no context":     {text: "kind: studio\nbogus: 1\n", keep: "kind: studio\nbogus: 1\n"},
		"base.context":   {text: "kind: studio\nbogus: 1\nbase:\n  context: " + blob + "\n", keep: "bogus: 1", not: blob},
		"merge key":      {text: "x: &c {context: " + blob + "}\nkind: studio\nbase:\n  <<: *c\n", keep: "kind: studio", not: blob},
		"aliased scalar": {text: "x: &b " + blob + "\nbase: {context: *b}\n", whole: true},
		"empty value":    {text: "base: {context: ''}\nbogus: 1\n", whole: true},
		"invalid yaml":   {text: "base: {context: " + blob + "\n  : [", whole: true},
		"invalid, clean": {text: "kind: [studio\n", keep: "kind: [studio\n"},
	} {
		got := hiddenKitYML(tc.text)
		switch {
		case tc.whole && got != hiddenContext:
			t.Errorf("%s: %q, want the placeholder alone", name, got)
		case strings.Contains(got, blob):
			t.Errorf("%s: shows the blob: %q", name, got)
		case tc.keep != "" && !strings.Contains(got, tc.keep):
			t.Errorf("%s: %q lost %q", name, got, tc.keep)
		case !tc.whole && tc.not != "" && !strings.Contains(got, hiddenContext):
			t.Errorf("%s: %q has no placeholder", name, got)
		}
	}
}
