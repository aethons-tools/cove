package jam

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/studio"
)

// A parent push changes the digest of a following child's image (stale) but
// not a pinned child's; a prompt-only parent push changes neither.
func TestParentChangeMarksFollowingChildStale(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store, _ := supTestKit(t, fl)
	parent := func(egress []string, prompt string) studio.StudioKit {
		return studio.StudioKit{Kind: studio.Kind, Egress: egress, Prompt: prompt}
	}
	mustEnsure(t, store, "base", parent([]string{"github.com"}, ""))
	follow := studio.StudioKit{Kind: studio.Kind, Egress: []string{"a.example"}}
	follow.Base.Kit = "base"
	mustEnsure(t, store, "follower", follow)
	pinned := studio.StudioKit{Kind: studio.Kind, Egress: []string{"b.example"}}
	pinned.Base.Kit = "base@v1"
	mustEnsure(t, store, "pinner", pinned)
	for role, kit := range map[string]string{"follow": "follower", "pin": "pinner"} {
		if err := store.PutRole("default", Role{Name: role, Kit: kit, Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: role, Project: "default", Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(step, wantFollow, wantPin string) {
		t.Helper()
		if s := summaryImage(t, store, sup, "follow"); s != wantFollow {
			t.Errorf("%s: following child = %q, want %q", step, s, wantFollow)
		}
		if s := summaryImage(t, store, sup, "pin"); s != wantPin {
			t.Errorf("%s: pinned child = %q, want %q", step, s, wantPin)
		}
	}
	check("fresh", "ok", "ok")

	mustEnsure(t, store, "base", parent([]string{"github.com", "new.example"}, ""))
	check("egress push", "stale", "ok")

	// The cache never serves the pre-push tag.
	cur, err := sup.CurrentImage("default", "follow")
	if err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("follow")
	if cur.Tag == inst.ImageTag {
		t.Errorf("current tag %q is the pre-push tag", cur.Tag)
	}
}

func TestParentPromptOnlyChangeStaysOK(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store, _ := supTestKit(t, fl)
	mustEnsure(t, store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	follow := studio.StudioKit{Kind: studio.Kind}
	follow.Base.Kit = "base"
	mustEnsure(t, store, "follower", follow)
	if err := store.PutRole("default", Role{Name: "follow", Kit: "follower", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "follow", Project: "default", Role: "follow"}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "follow"); s != "ok" {
		t.Fatalf("fresh = %q", s)
	}
	mustEnsure(t, store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}, Prompt: "changed"})
	if s := summaryImage(t, store, sup, "follow"); s != "ok" {
		t.Fatalf("after prompt-only parent push = %q, want ok", s)
	}
}
