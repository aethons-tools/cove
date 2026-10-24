package jam

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// The UI's text is the text render's, minus the indent: layout kept, every
// control, bidi override and invalid byte made visible.
func TestSetupDisplayText(t *testing.T) {
	got := SetupDisplayText("a\tb\nc\u202ed\x1b[31m\xff")
	if want := "a\tb\nc\\u202ed\\x1b[31m\\xff"; got != want {
		t.Fatalf("SetupDisplayText = %q, want %q", got, want)
	}
}

// A context body splits back into its head and its files, in the table's
// order; a file may hold a line that looks like a marker.
func TestSplitContextBody(t *testing.T) {
	b := sessionctx.Bundle{
		Core:     "# Core\n\n--- not a marker\n",
		Files:    map[string]string{"INDEX.md": "index\n", "role/dev.md": "role leaf"},
		Warnings: []string{"w1"},
	}
	body := contextBody(b)
	head, files, ok := SplitContextBody(body)
	if !ok {
		t.Fatalf("not split:\n%s", body)
	}
	if !strings.HasPrefix(head, "FILE") || !strings.HasSuffix(head, "warning: w1\n") || strings.Contains(head, "--- ") {
		t.Errorf("head = %q", head)
	}
	want := []ContextBodyFile{
		{Path: sessionctx.Dir + "/CORE.md", Body: "# Core\n\n--- not a marker\n"},
		{Path: sessionctx.Dir + "/INDEX.md", Body: "index\n"},
		{Path: sessionctx.Dir + "/role/dev.md", Body: "role leaf\n"},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %+v\nwant  %+v", files, want)
	}
}

// A file holding a later file's exact marker line stays that file's.
func TestSplitContextBodyHostileMarker(t *testing.T) {
	core := "core\n\n--- " + sessionctx.Dir + "/INDEX.md\nforged\n"
	body := contextBody(sessionctx.Bundle{Core: core, Files: map[string]string{"INDEX.md": "index\n"}})
	_, files, ok := SplitContextBody(body)
	if !ok || len(files) != 2 || files[0].Body != core || files[1].Body != "index\n" {
		t.Fatalf("ok=%v files=%+v", ok, files)
	}
}

// A truncated snapshot keeps the files it still holds; the last carries the
// truncation marker.
func TestSplitContextBodyTruncated(t *testing.T) {
	body := contextBody(sessionctx.Bundle{Core: "core\n", Files: map[string]string{"INDEX.md": "index\n", "role/dev.md": "leaf\n"}})
	cut := strings.Index(body, "\n--- "+sessionctx.Dir+"/role/dev.md")
	_, files, ok := SplitContextBody(body[:cut] + "\n… truncated: 99 bytes in all (cap 64)\n")
	if !ok || len(files) != 2 || !strings.Contains(files[1].Body, "truncated") || !strings.HasPrefix(files[1].Body, "index\n") {
		t.Fatalf("ok=%v files=%+v", ok, files)
	}
}

func TestSplitContextBodyNotAContextBody(t *testing.T) {
	for _, body := range []string{"", "policy: kit default\n", "FILE  BYTES\n"} {
		if _, _, ok := SplitContextBody(body); ok {
			t.Errorf("SplitContextBody(%q) ok", body)
		}
	}
}
