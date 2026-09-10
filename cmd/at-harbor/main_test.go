package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollCommandPrintsSnippet(t *testing.T) {
	store := filepath.Join(t.TempDir(), "ids.json")
	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--store", store, "--id", "spider-18", "--project", "ACME",
		"--role", "guest", "--destinations", "anthropic,git", "--repos", "acme/*",
		"--base-url", "https://harbor.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_AUTH_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
