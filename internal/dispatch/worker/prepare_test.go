package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func implementInput() Input {
	return Input{
		Issue: IssueInput{Key: "AET-1", Title: "T", WorkClass: "implement", Brief: "the brief"},
		Repo:  RepoInput{Name: "o/r", SourceBranch: "main", WorkBranch: "implement/AET-1"},
	}
}

func TestPrepareFreshBranch(t *testing.T) {
	dir := t.TempDir()
	g := &fakeGit{remoteHas: false}
	if err := Prepare(context.Background(), dir, implementInput(), g); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "EnsureClean") || !strings.Contains(joined, "Sync:main") ||
		!strings.Contains(joined, "NewBranch:implement/AET-1") || strings.Contains(joined, "Sync:implement/AET-1") {
		t.Fatalf("fresh-branch call sequence wrong: %v", g.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, workSubdir, "brief.md")); string(b) != "the brief" {
		t.Fatalf("brief not written: %q", b)
	}
}

func TestPrepareResumesExistingBranch(t *testing.T) {
	g := &fakeGit{remoteHas: true}
	if err := Prepare(context.Background(), t.TempDir(), implementInput(), g); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "Sync:implement/AET-1") || strings.Contains(joined, "NewBranch") {
		t.Fatalf("resume should Sync the work branch, not NewBranch: %v", g.calls)
	}
}

func TestPrepareRefusesBadWorkBranch(t *testing.T) {
	in := implementInput()
	in.Repo.WorkBranch = "main" // equals source-branch
	if err := Prepare(context.Background(), t.TempDir(), in, &fakeGit{}); err == nil {
		t.Fatal("Prepare should refuse work-branch == source-branch")
	}
}
