package adminui

import (
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// draftTree nests sorted entries by directory: a row for each directory the
// first time a file under it appears, indented by depth.
func TestDraftTreeNests(t *testing.T) {
	entries := []jam.DraftEntry{
		{Path: "a.txt", Mode: "0644", Size: 1},
		{Path: "a/b.txt", Mode: "0644", Size: 2},
		{Path: "a/b/c.sh", Mode: "0755", Size: 3},
		{Path: "a/z", Mode: "0644", Size: 4},
		{Path: "kit.yml", Mode: "0644", Size: 5},
	}
	rows := draftTree("app", entries, map[string]string{"a/b/c.sh": "added"})
	var got []string
	for _, r := range rows {
		l := r.Pad + r.Name
		if r.Dir {
			l += "/"
		} else {
			l += " " + r.Mode + " " + r.Change
		}
		got = append(got, l)
	}
	want := []string{"a.txt 0644 ", "a/", "  b.txt 0644 ", "  b/", "    c.sh 0755 added", "  z 0644 ", "kit.yml 0644 "}
	if !slices.Equal(got, want) {
		t.Fatalf("tree =\n%q\nwant\n%q", got, want)
	}
	if rows[4].Href != "/app/admin/kits/app/draft?file=a%2Fb%2Fc.sh" || rows[4].Path != "a/b/c.sh" || rows[1].Href != "" {
		t.Fatalf("rows = %+v", rows)
	}
}
