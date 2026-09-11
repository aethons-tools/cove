package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
)

func TestEnrollCommandPrintsSnippet(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--admin-url", ts.URL, "--id", "spider-18", "--project", "ACME",
		"--role", "guest", "--destinations", "anthropic,git", "--repos", "acme/*",
		"--base-url", "https://harbor.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "AT_HARBOR_IDENTITY_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
	if len(store.ListIdentities()) != 1 {
		t.Fatal("identity was not created via the admin API")
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
