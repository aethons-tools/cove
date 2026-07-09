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

func implementTask() Task {
	return Task{
		Issue:  TaskIssue{Key: "AET-1", Title: "T"},
		Repo:   TaskRepo{Name: "o/r", SourceBranch: "main", WorkBranch: "implement/AET-1"},
		Worker: TaskWorker{Class: "implement"},
		Task:   TaskSpec{Brief: "the brief"},
	}
}

func TestPrepareFreshBranch(t *testing.T) {
	dir := t.TempDir()
	g := &fakeGit{remoteHas: false}
	if err := Prepare(context.Background(), dir, implementTask(), g); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "EnsureClean") || !strings.Contains(joined, "Sync:main") ||
		!strings.Contains(joined, "NewBranch:implement/AET-1") || strings.Contains(joined, "Sync:implement/AET-1") {
		t.Fatalf("fresh-branch call sequence wrong: %v", g.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".at-work", "brief.md")); !os.IsNotExist(err) {
		t.Fatalf("prepare must not write brief.md; stat err=%v", err)
	}
}

func TestPrepareResumesExistingBranch(t *testing.T) {
	g := &fakeGit{remoteHas: true}
	if err := Prepare(context.Background(), t.TempDir(), implementTask(), g); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "Sync:implement/AET-1") || strings.Contains(joined, "NewBranch") {
		t.Fatalf("resume should Sync the work branch, not NewBranch: %v", g.calls)
	}
}

func TestPrepareRefusesBadWorkBranch(t *testing.T) {
	task := implementTask()
	task.Repo.WorkBranch = "main" // equals source-branch
	if err := Prepare(context.Background(), t.TempDir(), task, &fakeGit{}); err == nil {
		t.Fatal("Prepare should refuse work-branch == source-branch")
	}
}
