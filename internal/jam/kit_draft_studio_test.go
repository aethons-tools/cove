package jam

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/studio"
)

// draftStudioSetup is chainRaiseKit's my-app → web-dev → default, with
// my-app's draft open and its kit.yml edited (prompt "draft prompt"), and
// the supervisor's test builds over fb. It returns the draft's save.
func draftStudioSetup(t *testing.T, fb *fakeDraftBuilder) (*Supervisor, Store, *fakeLauncher, KitDraftSave) {
	t.Helper()
	sup, store, fl := chainRaiseKit(t)
	d, err := OpenKitDraft(store, "my-app", 0, "op", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	yml := "kind: studio\nbase: {kit: web-dev}\nprompt: draft prompt\n"
	s, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "kit.yml", 0, []byte(yml), DraftETag(d.Tree), "op", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup.SetKitDraftBuilds(NewKitDraftBuilds(ctx, store, OneDraftManager("local", fb), slog.New(slog.DiscardHandler)))
	return sup, store, fl, s
}

func raiseDraftStudio(t *testing.T, sup *Supervisor, id string) (Instance, error) {
	t.Helper()
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "dev", Name: id, KitDraft: "my-app"})
	return inst, err
}

func TestDraftStudioRaise(t *testing.T) {
	fb := &fakeDraftBuilder{}
	sup, store, fl, s := draftStudioSetup(t, fb)
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	tag, _ := KitDraftTag("my-app", s.Tree)
	if inst.KitDraft == nil || *inst.KitDraft != (KitDraftRef{Kit: "my-app", Tree: s.Tree}) || inst.ImageTag != tag || inst.Kit.ID != "my-app" || inst.Kit.Version != 0 {
		t.Fatalf("instance = %+v (draft %+v)", inst, inst.KitDraft)
	}
	if len(inst.KitChain) != 3 || inst.KitChain[0] != (KitRef{ID: "default", Version: 1}) || inst.KitChain[2] != (KitRef{ID: "my-app"}) {
		t.Fatalf("chain = %+v", inst.KitChain)
	}
	if fl.gotSpec.DraftImage == nil || *fl.gotSpec.DraftImage != (KitDraftImage{Tag: tag, ID: "sha256:img"}) || fl.prepareCalls != 0 {
		t.Fatalf("launcher got %+v, prepared %d", fl.gotSpec.DraftImage, fl.prepareCalls)
	}
	def := fb.defs[0]
	if def.Chain[2].Kit.Prompt != "draft prompt" || !reflect.DeepEqual(def.Harness, harnessFor(fl.gotSpec)) || def.Ref != inst.Kit {
		t.Fatalf("built %+v under %+v", def.Ref, def.Harness)
	}
	c := fl.gotSpec.Context
	if !strings.Contains(c.Core, "root prompt\n\ndraft prompt") {
		t.Fatalf("the raise context must carry the draft's prompt:\n%s", c.Core)
	}
	// The kit fact names the draft and its tree, never my-app@v0.
	label := "my-app (draft @" + s.Tree[:7] + ")"
	if !strings.Contains(c.Core, label) || strings.Contains(c.Core, "my-app@v0") || inst.KitDraft.String() != label {
		t.Fatalf("the kit fact must read %q:\n%s", label, c.Core)
	}
	again, err := sup.ContextFor("d1")
	if err != nil || again.Fingerprint != c.Fingerprint || !strings.Contains(again.Core, "draft prompt") {
		t.Fatalf("refresh = %v (fingerprint %s, want %s)", err, again.Fingerprint, c.Fingerprint)
	}
	if got := CoveSummaries(store, sup); len(got) != 1 || got[0].Image != "draft" {
		t.Fatalf("summaries = %+v", got)
	}
}

// A push or publish of the kit never stales or moves a draft studio.
func TestDraftStudioNeverStale(t *testing.T) {
	sup, store, _, _ := draftStudioSetup(t, &fakeDraftBuilder{})
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	pushed := studio.StudioKit{Kind: studio.Kind, Prompt: "v3"}
	pushed.Base.Kit = "web-dev"
	mustEnsure(t, store, "my-app", pushed)
	if got := CoveSummaries(store, sup)[0].Image; got != "draft" {
		t.Fatalf("image = %s after a push", got)
	}
	cur, _ := store.GetInstance("d1")
	if !reflect.DeepEqual(cur.KitDraft, inst.KitDraft) || cur.ImageTag != inst.ImageTag || cur.Kit != inst.Kit {
		t.Fatalf("the instance moved: %+v", cur)
	}
	if b, err := sup.ContextFor("d1"); err != nil || strings.Contains(b.Core, "v3") || !strings.Contains(b.Core, "draft prompt") {
		t.Fatalf("a refresh must keep the draft's prompt: %v\n%s", err, b.Core)
	}
}

func TestDraftStudioReusesItsBuild(t *testing.T) {
	fb := &fakeDraftBuilder{}
	fb.setPresent("sha256:img", true)
	sup, _, _, _ := draftStudioSetup(t, fb)
	for _, id := range []string{"d1", "d2"} {
		if _, err := raiseDraftStudio(t, sup, id); err != nil {
			t.Fatal(err)
		}
	}
	if fb.builds() != 1 {
		t.Fatalf("builds = %d, want the second raise to reuse the first build", fb.builds())
	}
	fb.setPresent("sha256:img", false) // the image was removed
	if _, err := raiseDraftStudio(t, sup, "d3"); err != nil {
		t.Fatal(err)
	}
	if fb.builds() != 2 {
		t.Fatalf("builds = %d, want a rebuild once the image is gone", fb.builds())
	}
}

// A test build under the default harness is not the studio's image: the
// role's harness differs, so the raise builds its own.
func TestDraftStudioDoesNotReuseAnotherHarness(t *testing.T) {
	fb := &fakeDraftBuilder{}
	fb.setPresent("sha256:img", true)
	sup, store, _, s := draftStudioSetup(t, fb)
	pinned := modelspec.Default("anthropic")
	pinned.Name, pinned.Version = "pinned", "2.1.100"
	if err := store.PutModelSpec(pinned); err != nil {
		t.Fatal(err)
	}
	role, _ := store.GetRole("default", "dev")
	role.ModelSpec = "pinned"
	if err := store.PutRole("default", role); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.KitDraftBuilds().Start("my-app", s.ETag, "op", ""); err != nil {
		t.Fatal(err)
	}
	waitDraftBuild(t, sup.KitDraftBuilds(), "my-app")
	if _, err := raiseDraftStudio(t, sup, "d1"); err != nil {
		t.Fatal(err)
	}
	if fb.builds() != 2 || reflect.DeepEqual(fb.defs[0].Harness, fb.defs[1].Harness) {
		t.Fatalf("builds = %d, want the raise's own under the role's harness", fb.builds())
	}
}

// waitDraftBuild waits for kit's latest build to end.
func waitDraftBuild(t *testing.T, b *KitDraftBuilds, kit string) {
	t.Helper()
	b.mu.Lock()
	j := b.jobs[draftJobKey(kit, "local")]
	b.mu.Unlock()
	<-j.done
}

func TestDraftStudioRefusals(t *testing.T) {
	fb := &fakeDraftBuilder{err: errors.New("RUN make: exit 2\nmore detail")}
	sup, store, _, s := draftStudioSetup(t, fb)
	_, err := raiseDraftStudio(t, sup, "d1")
	if err == nil || !strings.Contains(err.Error(), "draft my-app: the test build on local failed: RUN make: exit 2") ||
		strings.Contains(err.Error(), "more detail") || WriteStatus(err, 500) != 400 {
		t.Fatalf("failed build: %v", err)
	}
	if _, ok := store.GetInstance("d1"); ok {
		t.Fatal("a failed raise recorded an instance")
	}
	for _, a := range store.ListActors() {
		if a.ID == "d1" {
			t.Fatal("a failed raise left its identity")
		}
	}
	// The latest build of the tree failed: the next raise retries it.
	fb.mu.Lock()
	fb.err = nil
	fb.mu.Unlock()
	if _, err := raiseDraftStudio(t, sup, "d1"); err != nil || fb.builds() != 2 {
		t.Fatalf("retry = %v after %d builds", err, fb.builds())
	}
	ctx := context.Background()
	_, _, _, err = sup.Raise(ctx, RaiseSpec{ActorID: "d2", Project: "default", Role: "dev", Name: "d2", KitDraft: "web-dev"})
	if err == nil || !strings.Contains(err.Error(), "kit web-dev has no draft") || WriteStatus(err, 500) != 404 {
		t.Fatalf("no draft: %v", err)
	}
	_, _, _, err = sup.Raise(ctx, RaiseSpec{ActorID: "d3", Project: "default", Role: "dev", Name: "d3", KitDraft: "a\x1b[31m"})
	if err == nil || !strings.Contains(err.Error(), unsafeKitNameText) || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("unsafe name: %q", err)
	}
	if _, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "kit.yml", 0, []byte("kind: nope\n"), s.ETag, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err = raiseDraftStudio(t, sup, "d4")
	if err == nil || !strings.Contains(err.Error(), "draft my-app has problems: ") {
		t.Fatalf("problems: %v", err)
	}
	bare, bareStore, _ := chainRaiseKit(t)
	if _, err := OpenKitDraft(bareStore, "my-app", 0, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err = raiseDraftStudio(t, bare, "d5")
	if err == nil || !strings.Contains(err.Error(), kitDraftBuildsOff) || WriteStatus(err, 500) != 503 {
		t.Fatalf("no builds wired: %v", err)
	}
}

// A test build of an older tree, started on the supervisor's registry, is
// running: a raise of the current tree does not wait on it, it is refused.
// The build and the raise share the supervisor's one registry.
func TestDraftStudioRefusesWhileAnotherTreeBuilds(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	sup, store, _, s := draftStudioSetup(t, fb)
	t.Cleanup(func() { close(fb.release) })
	if _, err := sup.KitDraftBuilds().Start("my-app", s.ETag, "op", ""); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-fb.started
	if _, err := PutKitDraftFile(store, slog.New(slog.DiscardHandler), "my-app", "notes.txt", 0, []byte("x"), s.ETag, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	_, err := raiseDraftStudio(t, sup, "d1")
	if err == nil || !strings.Contains(err.Error(), "a draft build of my-app is already running on local") || WriteStatus(err, 500) != 409 {
		t.Fatalf("err = %v", err)
	}
	if fb.builds() != 1 {
		t.Fatalf("builds = %d", fb.builds())
	}
}

// Review Focus 4: discarding or publishing the draft leaves a running draft
// studio, its image status, and its tree as they were.
func TestDraftStudioKeepsItsTreeLive(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	t.Run("discard", func(t *testing.T) {
		sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
		inst, err := raiseDraftStudio(t, sup, "d1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DiscardKitDraft(store, log, "my-app", s.ETag); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.KitTree(s.Tree); !ok {
			t.Fatal("GC took a running draft studio's tree")
		}
		if sk, ok := instanceKit(store, inst); !ok || !strings.Contains(sk.Prompt, "draft prompt") {
			t.Fatalf("instanceKit = %+v %v", sk, ok)
		}
		if b, err := sup.ContextFor("d1"); err != nil || !strings.Contains(b.Core, "draft prompt") {
			t.Fatalf("context = %v", err)
		}
		if got := CoveSummaries(store, sup)[0].Image; got != "draft" {
			t.Fatalf("image = %s", got)
		}
		if err := sup.Teardown(context.Background(), "d1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GCKitObjects(); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.KitTree(s.Tree); ok {
			t.Fatal("a torn-down draft studio's tree outlived it")
		}
	})
	t.Run("publish", func(t *testing.T) {
		sup, store, _, s := draftStudioSetup(t, &fakeDraftBuilder{})
		inst, err := raiseDraftStudio(t, sup, "d1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PublishKitDraft(store, log, "my-app", false, s.ETag); err != nil {
			t.Fatal(err)
		}
		cur, _ := store.GetInstance("d1")
		if !reflect.DeepEqual(cur.KitDraft, inst.KitDraft) || CoveSummaries(store, sup)[0].Image != "draft" {
			t.Fatalf("a publish moved the draft studio: %+v", cur)
		}
	})
}

// Review Focus 4: the draft is discarded (and GC'd) while the raise's own
// build runs; the raise still records the studio and re-stores its tree.
func TestDraftStudioTreeReStoredAfterARacingDiscard(t *testing.T) {
	fb := &fakeDraftBuilder{}
	sup, store, _, s := draftStudioSetup(t, fb)
	fb.during = func() {
		if _, err := DiscardKitDraft(store, slog.New(slog.DiscardHandler), "my-app", s.ETag); err != nil {
			t.Error(err)
		}
		if _, ok := store.KitTree(s.Tree); ok {
			t.Error("the discard did not collect the tree: the test proves nothing")
		}
	}
	inst, err := raiseDraftStudio(t, sup, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.KitTree(s.Tree); !ok {
		t.Fatal("the studio's tree was not re-stored")
	}
	if sk, ok := instanceKit(store, inst); !ok || !strings.Contains(sk.Prompt, "draft prompt") {
		t.Fatalf("instanceKit = %+v %v", sk, ok)
	}
	if gc, err := store.GCKitObjects(); err != nil || gc.Trees != 0 {
		t.Fatalf("gc = %+v %v (the instance keeps its tree)", gc, err)
	}
}

// A manual raise reserves its label before it waits for the draft's build:
// a second raise with the same label meanwhile is refused as taken, and the
// label is free again once the first raise ends.
func TestDraftStudioRaiseReservesItsLabel(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{})}
	sup, store, _, _ := draftStudioSetup(t, fb)
	release := sync.OnceFunc(func() { close(fb.release) })
	t.Cleanup(release)
	spec := RaiseSpec{Project: "default", Role: "dev", KitDraft: "my-app"}
	done := make(chan error, 1)
	go func() {
		_, _, _, _, err := RaiseManual(context.Background(), store, sup, nil, "try", spec)
		done <- err
	}()
	<-fb.started
	// Bounded: an unreserved second raise would join the build's wait.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, _, err := RaiseManual(ctx, store, sup, nil, "try", spec)
	if err == nil || err.Error() != `a live session is already labelled "try"` || WriteStatus(err, 500) != http.StatusConflict {
		t.Fatalf("second raise while the first builds = %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := RaiseManual(context.Background(), store, sup, nil, "try", spec); err == nil || WriteStatus(err, 500) != http.StatusConflict {
		t.Fatalf("raise over the live session = %v", err)
	}
	// A failed raise releases its reservation.
	if _, _, _, _, err := RaiseManual(context.Background(), store, sup, nil, "other", RaiseSpec{Project: "default", Role: "nope"}); err == nil {
		t.Fatal("raise of an unknown role succeeded")
	}
	if sup.labelPending("other") {
		t.Fatal("a failed raise kept its label reserved")
	}
}

// A draft studio runs on the manager whose build it uses: Ensure picks m1,
// so the raise is placed on m1 alone, its timeout kept.
func TestDraftStudioPlacedOnBuilder(t *testing.T) {
	p := newPlacingFake("local", "m1")
	sup, store, _ := supTestKit(t, p)
	mustEnsure(t, store, "my-app", studio.StudioKit{Kind: studio.Kind, Prompt: "leaf"})
	if err := store.PutRole("default", Role{Name: "dev", Kit: "my-app", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKitDraft(store, "my-app", 0, "op", draftNow); err != nil {
		t.Fatal(err)
	}
	fm := newFakeManagedDraft("local", "m1")
	fm.setAway("local", true) // local cannot build drafts now: Ensure picks m1
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup.SetKitDraftBuilds(NewKitDraftBuilds(ctx, store, fm, slog.New(slog.DiscardHandler)))
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "d1", Project: "default", Role: "dev", Name: "d1", KitDraft: "my-app",
		Placement: Placement{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	got := p.by["m1"].gotSpec.Placement
	if inst.Manager != "m1" || !slices.Equal(got.Managers, []string{"m1"}) || got.Timeout != 5*time.Second || len(p.by["local"].raised) != 0 {
		t.Fatalf("manager %q, placement %+v, local raised %v", inst.Manager, got, p.by["local"].raised)
	}
	if fm.by["m1"].builds() != 1 || inst.ImageTag == "" {
		t.Fatalf("m1 builds = %d, image %q", fm.by["m1"].builds(), inst.ImageTag)
	}
}
