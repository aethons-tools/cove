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
		"export HARBOR_IDENTITY_TOKEN=TOK123",
		"ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic",
		"ANTHROPIC_API_KEY=$HARBOR_IDENTITY_TOKEN",
		`url."https://harbor.local.aethons.tools/git/".insteadOf`,
		// git must have a working, headless credential (username + env-only password),
		// so `git clone` through harbor doesn't prompt.
		`credential."https://harbor.local.aethons.tools".helper`,
		"username=x-access-token",
		"password=$HARBOR_IDENTITY_TOKEN",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
	// The raw token must appear exactly once — only in HARBOR_IDENTITY_TOKEN.
	// Everything else references the env var, so the token never lands in gitconfig.
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1 (env-only)\n---\n%s", n, out)
	}
}
