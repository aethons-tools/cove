package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotesParseResolveAndCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.md"), []byte("STEPS"), 0o644); err != nil {
		t.Fatal(err)
	}
	sk, err := ParseStudioKit([]byte("kind: studio\nnotes:\n  - name: release.md\n    read-when: you are releasing\n    file: release.md\n  - name: style.md\n    read-when: w\n    body: B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sk.ResolveNoteFiles(dir); err != nil {
		t.Fatal(err)
	}
	if sk.Notes[0].Body != "STEPS" || sk.Notes[0].File != "" || sk.Notes[1].Body != "B" {
		t.Fatalf("notes = %+v", sk.Notes)
	}
	if err := sk.CheckNotes(); err != nil {
		t.Fatal(err)
	}
	j, _ := sk.ToJSON()
	if strings.Contains(string(j), "release.md\",\"file") || !strings.Contains(string(j), "STEPS") {
		t.Fatalf("stored JSON must carry bodies, not files: %s", j)
	}
	for _, bad := range []StudioKit{
		{Kind: Kind, Notes: []KitNote{{Name: "tools.md", ReadWhen: "w"}}},
		{Kind: Kind, Notes: []KitNote{{Name: "x.md"}}},
		{Kind: Kind, Notes: []KitNote{{Name: "../x.md", ReadWhen: "w"}}},
	} {
		if err := bad.CheckNotes(); err == nil {
			t.Errorf("want an error for %+v", bad.Notes)
		}
	}
	esc := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "x.md", ReadWhen: "w", File: "../../etc/passwd"}}}
	if err := esc.ResolveNoteFiles(dir); err == nil {
		t.Error("an escaping note file must be refused")
	}
	both := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "x.md", ReadWhen: "w", File: "release.md", Body: "B"}}}
	if err := both.ResolveNoteFiles(dir); err == nil {
		t.Error("body and file together must be refused")
	}
}

func TestNotesDoNotChangeBuildDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Egress: []string{"github.com"}}
	b := a
	b.Prompt, b.Notes = "P", []KitNote{{Name: "n.md", ReadWhen: "w", Body: "B"}}
	if BuildDigest(a, dh) != BuildDigest(b, dh) {
		t.Fatal("prompt/notes are raise-time inputs; the build digest must ignore them")
	}
}

// A note whose file was never read (a server-side push of raw YAML) is refused
// rather than stored with an empty body.
func TestCheckNotesRefusesUnresolvedFile(t *testing.T) {
	sk := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "x.md", ReadWhen: "w", File: "x.md"}}}
	if err := sk.CheckNotes(); err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatalf("unresolved file: %v", err)
	}
}
