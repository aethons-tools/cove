//go:build integration

package main

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

// TestKitSetEndToEnd drives a two-kit repo (app builds on ../web, named
// web-dev) through run(...) against a real admin handler, Fetcher and git
// http-backend: tracking app imports both kits with app pinned to web-dev@v1;
// a change to web and a sync of app moves web-dev to v2 and app to v2 pinned
// @v2; a second sync changes nothing.
func TestKitSetEndToEnd(t *testing.T) {
	const token = "s3cret-kit-set-token"
	repo, first, update := gittest.Serve(t, map[string]string{
		"web/kit.yml": "kind: studio\nname: web-dev\nprompt: web one\n",
		"app/kit.yml": "kind: studio\nbase:\n  kit: ../web\nprompt: app\n",
	}, gittest.WithAuth("git", token))
	store := jam.NewMemStore()
	admin, _ := kitGitJam(t, store, repo, token)
	src := repo + "//app@main"

	code, out, errb := runKitAt(admin, "track", "app", "--git", src)
	if want := "  web-dev v1 (with app)\nimported app v1 from " + src + " (" + first[:12] + ")\ntracking " + src + "\n"; code != 0 || out != want {
		t.Fatalf("track: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}
	wantSet(t, admin, "after track", "web-dev@v1", map[string]int{"app": 1, "web-dev": 1})
	code, out, errb = runKitAt(admin, "show", "web-dev")
	if want := "# tracking: " + repo + "//web@main (last synced " + first[:12] + ")\n"; code != 0 || !strings.Contains(out, want) {
		t.Fatalf("show web-dev: code=%d stderr=%q, want %q in:\n%s", code, errb, want, out)
	}

	second := update(map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: web two\n"})
	code, out, errb = runKitAt(admin, "sync", "app")
	if want := "  web-dev v2 (with app)\nsynced app v2 (" + second[:12] + ")\n"; code != 0 || out != want {
		t.Fatalf("sync: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}
	wantSet(t, admin, "after sync", "web-dev@v2", map[string]int{"app": 2, "web-dev": 2})

	code, out, errb = runKitAt(admin, "sync", "app")
	if want := "app unchanged (" + second[:12] + ")\n"; code != 0 || out != want {
		t.Fatalf("sync again: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}
	wantSet(t, admin, "after sync again", "web-dev@v2", map[string]int{"app": 2, "web-dev": 2})
}

// wantSet checks that app's own stored base.kit is pin and that each kit has
// exactly the given number of versions.
func wantSet(t *testing.T, admin, when, pin string, versions map[string]int) {
	t.Helper()
	code, out, errb := runKitAt(admin, "show", "app", "--own")
	if code != 0 || !strings.Contains(out, "kit: "+pin+"\n") {
		t.Errorf("%s: show app --own: code=%d stderr=%q, want kit: %s in:\n%s", when, code, errb, pin, out)
	}
	for name, n := range versions {
		code, out, errb := runKitAt(admin, "versions", name)
		if rows := strings.Split(strings.TrimSpace(out), "\n"); code != 0 || len(rows) != n+1 {
			t.Errorf("%s: versions %s: code=%d stderr=%q, want a header and %d rows:\n%s", when, name, code, errb, n, out)
		}
	}
}
