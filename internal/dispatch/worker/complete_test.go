package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkerResult(t *testing.T, dir, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, taskSubdir), 0o755)
	if err := os.WriteFile(filepath.Join(dir, taskSubdir, "worker-result.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ok WITH a proposed PR → PR opened, task-result ok carries pr-url; worker-result echoed
func TestCompleteOKOpensPR(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir,
		`{"status":{"ok":{"pull-request":{"title":"AET-1: X","message":"body"}}},"extra":"kept"}`)
	ch := &fakeCodeHost{url: "https://x/pull/1"}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{changes: true, differs: true, sha: "abc"}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "ok" || tr.Status.OK.PRURL != "https://x/pull/1" {
		t.Fatalf("ok: %+v", tr.Status)
	}
	if ch.title != "AET-1: X" { // PR title comes from the worker, not at-task
		t.Fatalf("PR title = %q; want the worker's", ch.title)
	}
	if m, _ := tr.WorkerResult.(map[string]any); m["extra"] != "kept" {
		t.Fatalf("worker-result echo dropped unknown field: %v", tr.WorkerResult)
	}
}

// ok WITHOUT a proposed PR → branch pushed, no PR, ok without pr-url
func TestCompleteOKNoPR(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir, `{"status":{"ok":{}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{changes: true, differs: true, sha: "abc"}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "ok" || tr.Status.OK.PRURL != "" {
		t.Fatalf("ok-no-pr: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("no PR should be opened when the worker proposes none")
	}
}

// ok with a proposed PR but no diff from source → no PR opened
func TestCompleteOKNoDiffNoPR(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir, `{"status":{"ok":{"pull-request":{"title":"T","message":"m"}}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{changes: false, differs: false}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "ok" || tr.Status.OK.PRURL != "" {
		t.Fatalf("ok-no-diff: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("no PR should be opened when there is no diff from source")
	}
}

// needs-input → WIP pushed, commit SHA recorded, no PR
func TestCompleteNeedsInput(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir,
		`{"status":{"needs-input":{"doing":"d","blocker":"b","need":"n","tried":"t"}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{changes: true, sha: "deadbeef"}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "needs-input" || tr.Status.NeedsInput.Commit != "deadbeef" {
		t.Fatalf("needs-input: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("must not open a PR on needs-input")
	}
}

// worker error → task-result error; no git/PR
func TestCompleteWorkerError(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir, `{"status":{"error":{"message":"cannot"}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "error" || ch.opened {
		t.Fatalf("worker-error: %+v opened=%v", tr.Status, ch.opened)
	}
	if tr.Status.Error.Message != "worker could not execute task" {
		t.Fatalf("error message = %q", tr.Status.Error.Message)
	}
	if !strings.Contains(tr.Status.Error.Detail, "cannot") {
		t.Fatalf("error detail = %q; want it to preserve the worker's message %q", tr.Status.Error.Detail, "cannot")
	}
}

// missing worker-result → error, no echo
func writeAgentLog(t *testing.T, dir, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, taskSubdir), 0o755)
	if err := os.WriteFile(filepath.Join(dir, taskSubdir, agentLogName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteMissingWorkerResult(t *testing.T) {
	dir := t.TempDir()
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{}, &fakeCodeHost{})
	if v, _ := tr.Status.ActiveTask(); v != "error" || tr.WorkerResult != nil {
		t.Fatalf("missing: %+v echo=%v", tr.Status, tr.WorkerResult)
	}
	// No worker-result → "Agent did not respond"; with no captured output the
	// detail is empty (but the message still names the real failure).
	if tr.Status.Error == nil || tr.Status.Error.Message != "Agent did not respond" {
		t.Fatalf("want message 'Agent did not respond'; got %+v", tr.Status.Error)
	}
	if tr.Status.Error.Detail != "" {
		t.Fatalf("no agent log → empty detail; got %q", tr.Status.Error.Detail)
	}
}

// When the agent left no worker-result but its captured output exists, that output
// rides along verbatim as the detail — so a silent 401 is self-explaining.
func TestCompleteMissingWorkerResultSurfacesAgentLog(t *testing.T) {
	dir := t.TempDir()
	writeAgentLog(t, dir, "Failed to authenticate. API Error: 401 Invalid authentication credentials\n")
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{}, &fakeCodeHost{})
	if tr.Status.Error == nil || tr.Status.Error.Message != "Agent did not respond" {
		t.Fatalf("want message 'Agent did not respond'; got %+v", tr.Status.Error)
	}
	if !strings.Contains(tr.Status.Error.Detail, "401 Invalid authentication credentials") {
		t.Fatalf("agent output must ride along as detail; got %q", tr.Status.Error.Detail)
	}
}

// worker-result with a status that sets zero variants → error (invalid worker result),
// but the raw worker-result IS echoed: decoding succeeded, only the tagged union is invalid.
func TestCompleteInvalidWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir, `{"status":{}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "error" {
		t.Fatalf("invalid worker result: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("no PR should be opened for an invalid worker result")
	}
	if tr.WorkerResult == nil {
		t.Fatal("raw worker-result should be echoed when the document decodes but the status union is invalid")
	}
}

// unreadable (malformed) worker-result → error, no echo: decoding fails before any
// usable worker content exists.
func TestCompleteUnreadableWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeWorkerResult(t, dir, `{not valid json`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, implementTask(), &fakeGit{}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "error" {
		t.Fatalf("unreadable worker result: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("no PR should be opened for an unreadable worker result")
	}
	if tr.WorkerResult != nil {
		t.Fatalf("echo should be nil on decode failure, got %v", tr.WorkerResult)
	}
}
