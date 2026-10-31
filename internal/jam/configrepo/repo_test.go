package configrepo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

// served is a pushable repo with files on main, and a Repo following main.
func served(t *testing.T, files map[string]string, opts ...gittest.Option) (*Repo, string, func(map[string]string) string) {
	t.Helper()
	url, _, update := gittest.Serve(t, files, append([]gittest.Option{gittest.WithPush(), gittest.WithExec("bin/run")}, opts...)...)
	return &Repo{URL: url, Branch: "main", Path: filepath.Join(t.TempDir(), "config-repo")}, url, update
}

var ctx = context.Background()

func TestBranchesAndFiles(t *testing.T) {
	r, url, _ := served(t, map[string]string{"users/bob.yml": "kind: user\n", "bin/run": "#!/bin/sh\n"})
	bs, err := r.Branches(ctx)
	if err != nil || len(bs) != 1 || bs[0].Name != "main" || bs[0].Head != gittest.Head(t, url, "main") {
		t.Fatalf("branches = %+v %v", bs, err)
	}
	head, files, err := r.Files(ctx, "main")
	if err != nil || head != bs[0].Head {
		t.Fatalf("files head %s %v", head, err)
	}
	if f := files["users/bob.yml"]; string(f.Data) != "kind: user\n" || f.Mode != filemode.Regular {
		t.Fatalf("bob = %+v", f)
	}
	if f := files["bin/run"]; f.Mode != filemode.Executable {
		t.Fatalf("run mode = %v", f.Mode)
	}
	if _, _, err := r.Files(ctx, "nope"); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("unknown branch = %v", err)
	}
	// Another Repo on the same storage reuses it.
	again := &Repo{URL: url, Branch: "main", Path: r.Path}
	if _, _, err := again.Files(ctx, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestBadAuthIsRedacted(t *testing.T) {
	const token = "s3cret-tok"
	r, url, _ := served(t, map[string]string{"a": "b"}, gittest.WithAuth("git", "right"))
	r.Creds = gitsource.Creds{{Prefix: url, Name: "c", Value: token}}
	_, err := r.Branches(ctx)
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v", err)
	}
}
