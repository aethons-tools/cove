//go:build integration

package github

import (
	"context"
	"net/http"
	"os"
	"testing"
)

// TestLive opens a real PR. Run with:
//
//	GITHUB_TOKEN=… GH_REPO=owner/repo GH_BASE=main GH_HEAD=<existing-branch> \
//	go test -tags integration ./internal/dispatch/github/ -run TestLive -v
func TestLive(t *testing.T) {
	token, repo := os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_REPO")
	base, head := os.Getenv("GH_BASE"), os.Getenv("GH_HEAD")
	if token == "" || repo == "" || base == "" || head == "" {
		t.Skip("set GITHUB_TOKEN, GH_REPO, GH_BASE, GH_HEAD to run the live PR test")
	}
	url, err := New(token, http.DefaultClient).OpenPR(context.Background(), repo, base, head, "at-work smoke test", "opened by at-work TestLive")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	t.Logf("opened/found PR: %s", url)
}
