package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
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

// standing list survives a failing studio listing: the declarations still print
// (phase/image "-") with a warning on stderr, exit 0. An image status missing
// from the server's reply (an older Jam) prints as unknown in both lists.
func TestListsImageFallbacks(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{TTL: time.Hour},
		Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	bot := jam.StandingActorID("acme", "reviewer", "bot")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	covesReply := "" // "" = fail GET /admin/coves
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/coves" {
			if covesReply == "" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, covesReply)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer ts.Close()
	getenv := func(string) string { return "" }
	run2 := func(args ...string) (string, string) {
		t.Helper()
		var out, errb bytes.Buffer
		if code := run(append(args, "--admin-url", ts.URL), getenv, &out, &errb); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, errb.String())
		}
		return out.String(), errb.String()
	}

	out, errOut := run2("standing", "list", "--project", "acme", "--role", "reviewer")
	if out != "bot\tid="+bot+"\tphase=-\timage=-\n" || !strings.Contains(errOut, "warning") {
		t.Fatalf("standing list with failing coves: out=%q stderr=%q", out, errOut)
	}

	covesReply = `[{"id":"` + bot + `","project":"acme","role":"reviewer","phase":"live","connector":"ok"}]`
	if out, _ := run2("studio", "list"); !strings.Contains(out, "\timage=unknown\n") {
		t.Fatalf("studio list without image field:\n%s", out)
	}
	if out, _ := run2("standing", "list", "--project", "acme", "--role", "reviewer"); out != "bot\tid="+bot+"\tphase=live\timage=unknown\n" {
		t.Fatalf("standing list without image field: %q", out)
	}
}
