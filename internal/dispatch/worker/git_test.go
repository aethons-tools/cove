package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run execs a command in dir (or cwd if ""), failing the test on error.
func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// newRemote makes a bare repo seeded with one commit on `main`, returns its path.
func newRemote(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	run(t, "", "git", "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	run(t, "", "git", "clone", bare, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi\n"), 0o644)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "push", "origin", "main")
	return bare
}

func TestEnsureCleanAndSyncAndNewBranch(t *testing.T) {
	remote := newRemote(t)
	g, err := NewShellGit("")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "wt")
	ctx := context.Background()

	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatalf("EnsureClean(clone): %v", err)
	}
	if err := g.Sync(ctx, dir, "main"); err != nil {
		t.Fatalf("Sync main: %v", err)
	}
	has, err := g.RemoteHasBranch(ctx, dir, "implement/AET-1")
	if err != nil || has {
		t.Fatalf("RemoteHasBranch new = %v,%v; want false,nil", has, err)
	}
	if err := g.NewBranch(ctx, dir, "implement/AET-1", "main"); err != nil {
		t.Fatalf("NewBranch: %v", err)
	}
	// EnsureClean on the existing clean checkout is fine
	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatalf("EnsureClean(existing clean): %v", err)
	}
	// a dirty checkout is refused
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644)
	if err := g.EnsureClean(ctx, remote, dir); err == nil {
		t.Fatal("EnsureClean should refuse a dirty checkout")
	}
}

func TestSyncResumesExistingRemoteBranch(t *testing.T) {
	remote := newRemote(t)
	// push a work branch to the remote (a prior NEEDS_INPUT round)
	seed := filepath.Join(t.TempDir(), "s")
	run(t, "", "git", "clone", remote, seed)
	run(t, seed, "git", "checkout", "-b", "implement/AET-1")
	os.WriteFile(filepath.Join(seed, "wip.txt"), []byte("wip"), 0o644)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-m", "wip")
	run(t, seed, "git", "push", "origin", "implement/AET-1")

	g, _ := NewShellGit("")
	dir := filepath.Join(t.TempDir(), "wt")
	ctx := context.Background()
	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatal(err)
	}
	has, err := g.RemoteHasBranch(ctx, dir, "implement/AET-1")
	if err != nil || !has {
		t.Fatalf("RemoteHasBranch = %v,%v; want true", has, err)
	}
	if err := g.Sync(ctx, dir, "implement/AET-1"); err != nil {
		t.Fatalf("Sync(resume): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wip.txt")); err != nil {
		t.Fatalf("resume did not restore WIP: %v", err)
	}
}
