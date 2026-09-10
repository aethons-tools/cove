package harbor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollStoresHashedIdentity(t *testing.T) {
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	tok, err := Enroll(store, "spider-18", "ACME", "guest", []string{"anthropic", "git"}, []string{"acme/*"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	id, ok := store.Lookup(HashToken(tok))
	if !ok || id.ID != "spider-18" || id.TokenHash == tok {
		t.Fatalf("stored identity = %+v, ok=%v (hash must not equal raw token)", id, ok)
	}
}

func TestRenderEnrollSnippetIncludesEndpointsNotSecrets(t *testing.T) {
	out := RenderEnrollSnippet("https://harbor.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic",
		"ANTHROPIC_AUTH_TOKEN=TOK123",
		`url."https://harbor.local.aethons.tools/git/".insteadOf`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
}
