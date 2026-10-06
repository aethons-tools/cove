package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// taggingLauncher names images like the real launcher (kit digest + a Jam-side
// fingerprint); changing *asm stales every raised image.
type taggingLauncher struct {
	aliveLauncher
	asm *string
}

func (l taggingLauncher) ImageTag(ref jam.KitRef) string {
	return "cove-kit:" + ref.Digest + "-" + *l.asm
}

// `studio list` prints each cove's image status, and `standing list` each
// declared session's running studio (phase + image), "-" when none runs.
func TestListsShowImageStatus(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if _, err := jam.EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Kit: "web", Scope: jam.Scope{TTL: time.Hour},
		Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "bot", Prompt: "p"}, {Name: "idle", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	asm := "a1"
	sup := jam.NewSupervisor(store, taggingLauncher{asm: &asm}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot := jam.StandingActorID("acme", "reviewer", "bot")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: bot, Project: "acme", Role: "reviewer", Name: "bot", SessionKind: "standing"}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	list := func(args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := run(append(args, "--admin-url", ts.URL), getenv, &out, &errb); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, errb.String())
		}
		return out.String()
	}

	if out := list("studio", "list"); !strings.Contains(out, "\timage=ok") {
		t.Fatalf("studio list:\n%s", out)
	}
	standing := list("standing", "list", "--project", "acme", "--role", "reviewer")
	want := "bot\tid=" + bot + "\tphase=live\timage=ok\n" +
		"idle\tid=" + jam.StandingActorID("acme", "reviewer", "idle") + "\tphase=-\timage=-\n"
	if standing != want {
		t.Fatalf("standing list = %q, want %q", standing, want)
	}

	asm = "a2"
	if out := list("studio", "list"); !strings.Contains(out, "\timage=stale") {
		t.Fatalf("studio list after rebuild:\n%s", out)
	}
	if out := list("standing", "list", "--project", "acme", "--role", "reviewer"); !strings.Contains(out, "\tphase=live\timage=stale\n") {
		t.Fatalf("standing list after rebuild:\n%s", out)
	}
}
