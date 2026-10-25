package adminui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// maskKitYML masks only base's packed-context fields, in a plain text (no
// anchors, aliases or merge keys), never another key named context, keeps an unmasked text exactly as
// stored, and falls back to the fixed placeholder alone when it cannot be
// sure.
// A merge fan-out (each level merging the previous one eight times, sixteen
// levels deep) is refused at once, not expanded.
func TestMaskKitYMLMergeFanOutIsFast(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 {context: H4sIAAAAblob}\n")
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "a%d: &a%d {<<: [", i, i)
		for j := 0; j < 8; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*a%d", i-1)
		}
		b.WriteString("]}\n")
	}
	b.WriteString("base: {<<: *a16}\n")
	start := time.Now()
	got, how := maskKitYML(b.String())
	if how != maskWhole || strings.Contains(got, "H4sI") {
		t.Fatalf("fan-out: %d %q", how, got)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("fan-out took %v", d)
	}
}

func TestMaskKitYML(t *testing.T) {
	const blob = "H4sIAAAAblob"
	for name, tc := range map[string]struct {
		text string
		want maskKind
		keep string // the result keeps keep
	}{
		"no context, as stored":   {text: "# note\nkind:   studio\nbogus: 1\n", want: maskNone, keep: "# note\nkind:   studio\nbogus: 1\n"},
		"build-args context kept": {text: "kind: studio\nbogus: 1\nbuild-args: {context: x}\n", want: maskNone, keep: "build-args: {context: x}"},
		"mcp header kept":         {text: "bogus: 1\nmcp-servers: {m: {headers: {context: y}}}\n", want: maskNone, keep: "context: y"},
		"base.context":            {text: "kind: studio\nbogus: 1\nbuild-args: {context: x}\nbase:\n  context: " + blob + "\n", want: maskPartial, keep: "context: x"},
		"base.context-files":      {text: "bogus: 1\nbase:\n  context-files: {dockerfile: " + blob + "}\n", want: maskPartial, keep: "bogus: 1"},
		"base.contextFiles":       {text: "bogus: 1\nbase:\n  contextFiles: {dockerfile: " + blob + "}\n", want: maskPartial, keep: "bogus: 1"},
		"base.context-dir":        {text: "bogus: 1\nbase: {context-dir: " + blob + "}\n", want: maskPartial, keep: "bogus: 1"},
		"merge key into base":     {text: "x: &c {context: " + blob + "}\nkind: studio\nbase:\n  <<: *c\n", want: maskWhole},
		"merge list into base":    {text: "x: &c {context: " + blob + "}\nbase:\n  <<: [*c]\n", want: maskWhole},
		"aliased base":            {text: "x: &b {context: " + blob + "}\nbase: *b\n", want: maskWhole},
		"top-level merge":         {text: "<<: {base: {context: " + blob + "}}\nbogus: 1\n", want: maskWhole},
		"anchored context-files":  {text: "x: &t {dockerfile: " + blob + "}\nbase: {context-files: *t}\n", want: maskWhole},
		"anchored list":           {text: "x: &t [" + blob + "]\nbase: {context-files: *t}\n", want: maskWhole},
		"alias key":               {text: "k: &k context\nbase: {*k : " + blob + "}\n", want: maskWhole},
		"sequence root":           {text: "- base: {context: " + blob + "}\n", want: maskWhole},
		"sequence root, clean":    {text: "- kind: studio\n", want: maskNone, keep: "- kind: studio\n"},
		"anchor, no context":      {text: "x: &a 1\ny: *a\n", want: maskNone, keep: "x: &a 1\ny: *a\n"},
		"non-scalar key":          {text: "? [a]\n: 1\nbase: {context: " + blob + "}\n", want: maskWhole},
		"aliased scalar":          {text: "x: &b " + blob + "\nbase: {context: *b}\n", want: maskWhole},
		"invalid yaml":            {text: "base: {context: " + blob + "\n  : [", want: maskWhole},
		"invalid, clean":          {text: "kind: [studio\n", want: maskNone, keep: "kind: [studio\n"},
		"multi-document":          {text: "kind: studio\nbogus: 1\n---\nbase: {context: " + blob + "}\n", want: maskWhole},
		"multi-document, clean":   {text: "kind: studio\n---\nbogus: 1\n", want: maskNone, keep: "---"},
	} {
		got, how := maskKitYML(tc.text)
		switch {
		case how != tc.want:
			t.Errorf("%s: kind %d, want %d (%q)", name, how, tc.want, got)
		case strings.Contains(got, blob):
			t.Errorf("%s: shows the blob: %q", name, got)
		case how == maskWhole && got != hiddenContext:
			t.Errorf("%s: %q, want the placeholder alone", name, got)
		case how == maskNone && got != tc.text:
			t.Errorf("%s: %q, want the text as stored", name, got)
		case how == maskPartial && !strings.Contains(got, hiddenContext):
			t.Errorf("%s: %q has no placeholder", name, got)
		case tc.keep != "" && !strings.Contains(got, tc.keep):
			t.Errorf("%s: %q lost %q", name, got, tc.keep)
		}
	}
}
