package studio

import (
	"strings"
	"testing"
)

func TestNotesParseAndCheck(t *testing.T) {
	sk, err := ParseStudioKit([]byte("kind: studio\nnotes:\n  - name: style.md\n    read-when: w\n    body: B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sk.Notes[0].Body != "B" {
		t.Fatalf("notes = %+v", sk.Notes)
	}
	if err := sk.CheckNotes(); err != nil {
		t.Fatal(err)
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
