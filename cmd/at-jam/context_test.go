package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

func TestParseContextFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.md"), []byte("STEPS"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "core: C\nleaves:\n  - name: release.md\n    read-when: releasing\n    file: release.md\n  - name: inline.md\n    read-when: w\n    body: B\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove}\n"
	b, err := parseContextFile([]byte(src), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.Core != "C" || len(b.Leaves) != 2 || b.Leaves[0].Body != "STEPS" || b.Leaves[0].ReadWhen != "releasing" || b.Leaves[1].Body != "B" || len(b.Resources) != 1 {
		t.Fatalf("parsed = %+v", b)
	}
	for _, bad := range []string{
		"core: C\nleaves:\n  - name: x.md\n    read-when: w\n    body: B\n    file: f.md\n", // both
		"core: C\nleaves:\n  - name: x.md\n    read-when: w\n    file: ../../etc/passwd\n",  // escapes dir
		"core: C\nnope: 1\n", // unknown key
	} {
		if _, err := parseContextFile([]byte(bad), dir); err == nil {
			t.Errorf("want an error for:\n%s", bad)
		}
	}
}

func TestContextScopeFlags(t *testing.T) {
	for args, want := range map[string]adminclient.ContextScope{
		"--role acme/dev": {Project: "acme", Role: "dev"},
		"--role dev":      {Project: jam.DefaultProject, Role: "dev"},
		"--project acme":  {Project: "acme"},
		"--jam":           {Jam: true},
	} {
		got, err := contextScope(strings.Fields(args))
		if err != nil || got != want {
			t.Errorf("%s → %+v, %v; want %+v", args, got, err, want)
		}
	}
	for _, bad := range []string{"", "--jam --project acme", "--role a/b --project c"} {
		if _, err := contextScope(strings.Fields(bad)); err == nil {
			t.Errorf("%q: want exactly one scope", bad)
		}
	}
}
