package studio

import "testing"

func TestBuildDigestIgnoresPrompt(t *testing.T) {
	a := StudioKit{Kind: Kind, Name: "web", Base: Base{Ref: "r"}, Egress: []string{"b.com", "a.com"}, Prompt: "one"}
	b := a
	b.Prompt = "two (edited)" // raise-time field — must not change the image key
	if BuildDigest(a) != BuildDigest(b) {
		t.Fatal("prompt edit must not change the build-digest")
	}
	c := a
	c.Egress = []string{"a.com", "b.com"} // reordered — same set
	if BuildDigest(a) != BuildDigest(c) {
		t.Fatal("egress order must not change the build-digest")
	}
	d := a
	d.BuildArgs = map[string]string{"X": "1"} // build-affecting change
	if BuildDigest(a) == BuildDigest(d) {
		t.Fatal("a build-arg change must change the build-digest")
	}
}
