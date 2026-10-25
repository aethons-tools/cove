package jam

import (
	"context"
	"strings"
	"testing"
)

func TestDraftStudioSetupGolden(t *testing.T) {
	sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
	if _, err := raiseDraftStudio(t, sup, "d1"); err != nil {
		t.Fatal(err)
	}
	d := SetupDeps{Store: store, Sup: sup, Images: &fakeImages{}}
	got, err := InstanceSetup(context.Background(), d, "d1", []string{SectionKit}, false)
	if err != nil {
		t.Fatal(err)
	}
	heading := "kit (draft @" + s.Tree[:7] + ")"
	if got.Sections[0].Name != SectionKit || got.Sections[0].Title != heading || got.Sections[0].Stale {
		t.Fatalf("section = %+v", got.Sections[0])
	}
	var b strings.Builder
	RenderSetupText(&b, got)
	text := b.String()
	for _, want := range []string{
		"\n== " + heading + " (snapshot, ",
		"  kit: my-app (draft @" + s.Tree[:7] + ") digest ",
		"  chain (root first): default@v1 → web-dev@v1 → my-app (draft)\n",
		"  image tag: cove-kit-draft:my-app-" + s.Tree[:12] + "\n",
		"my-app (draft)  draft, not published",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "my-app@v0") {
		t.Fatalf("a draft studio must not show its kit as v0:\n%s", text)
	}
	checkGolden(t, "studio-draft", got)
	role, err := RoleSetup(context.Background(), d, "default", "dev", []string{SectionKit})
	if err != nil || role.Sections[0].Title != "" {
		t.Fatalf("the role render must be unchanged: %+v %v", role.Sections[0], err)
	}
}

// TestDraftStudioKitRefsRecomputed: without a recorded snapshot the
// recomputed kit part names the draft too, never <kit>@v0.
func TestDraftStudioKitRefsRecomputed(t *testing.T) {
	inst := Instance{
		Kit:      KitRef{ID: "my-app", Version: 0, Digest: "sha256:0123456789abcdef"},
		KitChain: []KitRef{{ID: "default", Version: 1}, {ID: "my-app", Version: 0}},
		ImageTag: "cove-kit-draft:my-app-abcdef012345",
		KitDraft: &KitDraftRef{Kit: "my-app", Tree: "abcdef0123456789"},
	}
	want := "kit: my-app (draft @abcdef0) digest " + shortDigest(inst.Kit.Digest) + "\nchain (root first): default@v1 → my-app (draft)\nimage tag: cove-kit-draft:my-app-abcdef012345\n"
	if got := instanceKitRefsBody(inst); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	inst.KitDraft = nil
	if got := instanceKitRefsBody(inst); got != kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag) {
		t.Fatalf("a published studio's refs changed: %q", got)
	}
}

func TestRenderSetupTextUsesTheTitle(t *testing.T) {
	var b strings.Builder
	RenderSetupText(&b, StudioSetup{Subject: "studio s1", Sections: []SetupSection{{Name: SectionKit, Title: "kit (draft @abcdef0)", Source: SourceConfig, Body: "k\n"}}})
	if want := "# studio s1\n\n== kit (draft @abcdef0) (config)\n  k\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

// A draft studio's image facts are read by the image id it raised, never by
// its moving tag: a later build retagging the tag to another image leaves
// the studio's inspect showing what it runs.
func TestDraftStudioSetupReadsItsImageByID(t *testing.T) {
	sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if inst.KitDraftImage != "sha256:img" {
		t.Fatalf("recorded draft image = %q", inst.KitDraftImage)
	}
	tag, _ := KitDraftTag("my-app", s.Tree)
	raised := ImageFacts{ID: "sha256:img", Labels: map[string]string{"which": "raised"}, Seed: []SeedFile{{Path: "raised.md", Size: 1}}}
	moved := ImageFacts{ID: "sha256:other", Labels: map[string]string{"which": "moved"}, Seed: []SeedFile{{Path: "moved.md", Size: 1}}}
	imgs := &fakeImages{facts: map[string]ImageFacts{"sha256:img": raised, tag: moved}}
	d := SetupDeps{Store: store, Sup: sup, Images: imgs}
	got, err := InstanceSetup(context.Background(), d, "d1", []string{SectionKit, SectionSeed}, false)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	RenderSetupText(&b, got)
	text := b.String()
	for _, want := range []string{"image id: sha256:img", "which=raised", "raised.md", "image tag: " + tag} {
		if !strings.Contains(text, want) {
			t.Fatalf("render lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "moved") {
		t.Fatalf("render read the moved tag:\n%s", text)
	}
}
