package studio

import "testing"

// A kit's stored form is ToJSON, whose base.context-files key is spelled
// "contextFiles"; ParseStudioKit must read it back (every raise and every
// chain link parses the stored config).
func TestStoredContextFilesRoundTrip(t *testing.T) {
	sk, err := ParseStudioKit([]byte("kind: studio\nbase:\n  context-files:\n    dockerfile: |\n      FROM ${COVE_BASE_IMAGE}\n"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseStudioKit(stored)
	if err != nil {
		t.Fatalf("stored form does not parse: %v\n%s", err, stored)
	}
	if k, _ := back.Base.Kind(); k != BaseContextFiles || back.Base.ContextFiles["dockerfile"].File == nil {
		t.Fatalf("round trip lost context-files: %+v", back.Base)
	}
	again, _ := back.ToJSON()
	if string(again) != string(stored) {
		t.Fatalf("stored form not stable:\n%s\n%s", stored, again)
	}
}
