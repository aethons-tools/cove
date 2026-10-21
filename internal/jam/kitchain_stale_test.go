package jam

import (
	"context"
	"strings"
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

func chainRole(t *testing.T, store Store, role, kitName string) {
	t.Helper()
	if err := store.PutRole("default", Role{Name: role, Kit: kitName, Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
}

func TestParentRemovedSurfacesErrorNotCachedTag(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store, _ := supTestKit(t, fl)
	mustEnsure(t, store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	child := studio.StudioKit{Kind: studio.Kind}
	child.Base.Kit = "base"
	mustEnsure(t, store, "follower", child)
	chainRole(t, store, "follow", "follower")
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "follow", Project: "default", Role: "follow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.CurrentImage("default", "follow"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveKit("base"); err != nil {
		t.Fatal(err)
	}
	cur, err := sup.CurrentImage("default", "follow")
	if err == nil || !strings.Contains(err.Error(), "kit base (parent of follower) is missing") || cur.Tag != "" {
		t.Fatalf("CurrentImage = %+v, %v; want the missing-parent error and no tag", cur, err)
	}
	if s := summaryImage(t, store, sup, "follow"); s != "unknown" {
		t.Fatalf("status = %q, want unknown", s)
	}
}

func TestChainedCurrentImageIsCached(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store, _ := supTestKit(t, fl)
	mustEnsure(t, store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	child := studio.StudioKit{Kind: studio.Kind}
	child.Base.Kit = "base"
	mustEnsure(t, store, "follower", child)
	chainRole(t, store, "follow", "follower")
	for range 3 {
		if _, err := sup.CurrentImage("default", "follow"); err != nil {
			t.Fatal(err)
		}
	}
	if n := sup.kitRefs.misses; n != 1 {
		t.Fatalf("misses = %d over 3 polls, want 1", n)
	}
}

func TestGrandparentChangeMarksGrandchildStale(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store, _ := supTestKit(t, fl)
	mustEnsure(t, store, "root", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	mid := studio.StudioKit{Kind: studio.Kind}
	mid.Base.Kit = "root"
	mustEnsure(t, store, "mid", mid)
	leaf := studio.StudioKit{Kind: studio.Kind, Egress: []string{"a.example"}}
	leaf.Base.Kit = "mid"
	mustEnsure(t, store, "leaf", leaf)
	chainRole(t, store, "follow", "leaf")
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "follow", Project: "default", Role: "follow"}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "follow"); s != "ok" {
		t.Fatalf("fresh = %q", s)
	}
	mustEnsure(t, store, "root", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com", "new.example"}})
	if s := summaryImage(t, store, sup, "follow"); s != "stale" {
		t.Fatalf("after grandparent push = %q, want stale", s)
	}
}
