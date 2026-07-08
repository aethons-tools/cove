package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeOutcome(t *testing.T, dir, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, workSubdir), 0o755)
	if err := os.WriteFile(filepath.Join(dir, workSubdir, "outcome.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteOKOpensPR(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"OK","pr-message":"body"}`)
	g := &fakeGit{changes: true, differs: true, sha: "abc"}
	ch := &fakeCodeHost{url: "https://x/pull/1"}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusOK || out.Work.PRURL != "https://x/pull/1" {
		t.Fatalf("out = %+v; want OK + pr-url", out)
	}
	if !ch.opened {
		t.Fatal("PR was not opened")
	}
}

func TestCompleteOKNoChangesIsError(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"OK"}`)
	g := &fakeGit{changes: false, differs: false} // nothing to PR
	ch := &fakeCodeHost{}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusError {
		t.Fatalf("out = %+v; want ERROR (no changes)", out)
	}
	if ch.opened {
		t.Fatal("must not open a PR with no changes")
	}
}

func TestCompleteNeedsInputPushesNoPR(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"NEEDS_INPUT","needs-input":{"blocker":"b","need":"n"}}`)
	g := &fakeGit{changes: true, sha: "def"}
	ch := &fakeCodeHost{}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusNeedsInput || out.Work.SafeState == "" {
		t.Fatalf("out = %+v; want NEEDS_INPUT + safe-state", out)
	}
	if ch.opened {
		t.Fatal("must not open a PR on needs_input")
	}
}

func TestCompleteMissingOutcomeIsError(t *testing.T) {
	out := Complete(context.Background(), t.TempDir(), implementInput(), &fakeGit{}, &fakeCodeHost{})
	if out.Status != StatusError {
		t.Fatalf("out = %+v; want ERROR for missing outcome", out)
	}
}
