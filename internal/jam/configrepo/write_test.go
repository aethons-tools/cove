package configrepo

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

var alice = Signature{Name: "alice", Email: "alice@example.com"}

func TestCreateBranch(t *testing.T) {
	r, url, _ := served(t, map[string]string{"a.yml": "a\n"})
	h, err := r.CreateBranch(ctx, "jam/alice")
	if err != nil || h != gittest.Head(t, url, "main") || gittest.Head(t, url, "jam/alice") != h {
		t.Fatalf("create = %s %v", h, err)
	}
	if _, err := r.CreateBranch(ctx, "jam/alice"); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("again = %v", err)
	}
	if _, err := r.CreateBranch(ctx, "main"); !errors.Is(err, ErrFollowedBranch) {
		t.Fatalf("main = %v", err)
	}
	if _, err := r.CreateBranch(ctx, "bad..name"); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestCommit(t *testing.T) {
	r, url, _ := served(t, map[string]string{"a.yml": "a\n", "old.yml": "old\n"})
	h0, _ := r.CreateBranch(ctx, "jam/alice")
	h1, err := r.Commit(ctx, "jam/alice", h0, Changes{
		Put:    map[string]File{"projects/app/roles/ic.yml": {Data: []byte("kind: role\n"), Mode: filemode.Regular}, "kits/web/run.sh": {Data: []byte("#!/bin/sh\n"), Mode: filemode.Executable}},
		Delete: []string{"old.yml", "never.yml"},
	}, alice, "role app/ic: added")
	if err != nil || h1 == h0 || gittest.Head(t, url, "jam/alice") != h1 {
		t.Fatalf("commit = %s %v", h1, err)
	}
	if got, ok := gittest.Show(t, url, "jam/alice", "projects/app/roles/ic.yml"); !ok || got != "kind: role\n" {
		t.Fatalf("ic = %q %v", got, ok)
	}
	if _, ok := gittest.Show(t, url, "jam/alice", "old.yml"); ok {
		t.Fatal("old.yml still there")
	}
	if got, ok := gittest.Show(t, url, "jam/alice", "a.yml"); !ok || got != "a\n" {
		t.Fatal("a.yml lost")
	}
	if mode := gittest.Git(t, url, "ls-tree", "jam/alice", "kits/web/run.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("mode %q", mode)
	}
	if log := gittest.Git(t, url, "log", "-1", "--format=%an <%ae>|%cn|%s", "jam/alice"); strings.TrimSpace(log) != "alice <alice@example.com>|Jam|role app/ic: added" {
		t.Fatalf("log %q", log)
	}
	if gittest.Head(t, url, "main") != h0 {
		t.Fatal("main moved")
	}
	// no-op: the same content again makes no commit.
	same, err := r.Commit(ctx, "jam/alice", h1, Changes{Put: map[string]File{"a.yml": {Data: []byte("a\n"), Mode: filemode.Regular}}}, alice, "nothing")
	if err != nil || same != h1 || gittest.Head(t, url, "jam/alice") != h1 {
		t.Fatalf("no-op = %s %v", same, err)
	}
	// stale head: an old wantHead is refused.
	if _, err := r.Commit(ctx, "jam/alice", h0, Changes{Delete: []string{"a.yml"}}, alice, "x"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale = %v", err)
	}
	if _, err := r.Commit(ctx, "main", h0, Changes{Delete: []string{"a.yml"}}, alice, "x"); !errors.Is(err, ErrFollowedBranch) {
		t.Fatalf("main = %v", err)
	}
	if _, err := r.Commit(ctx, "nope", h0, Changes{}, alice, "x"); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("unknown = %v", err)
	}
}
