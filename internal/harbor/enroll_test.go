package harbor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollStoresHashedIdentity(t *testing.T) {
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	if err := store.PutRole(DefaultProject, Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, TTL: time.Hour}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	tok, err := Enroll(store, "spider-18", "", "guest", nil, time.Now())
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	a, ok := store.Lookup(HashToken(tok))
	if !ok || a.ID != "spider-18" || a.TokenHash == tok {
		t.Fatalf("stored actor = %+v, ok=%v (hash must not equal raw token)", a, ok)
	}
	if len(a.Grants) != 1 || a.Grants[0].Project != DefaultProject || a.Grants[0].Role != "guest" {
		t.Fatalf("actor grants = %+v, want one (default,guest) grant", a.Grants)
	}
}

func TestEnrollRequiresExistingRole(t *testing.T) {
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	if _, err := Enroll(store, "id2", "", "missing", nil, time.Now()); err == nil {
		t.Fatal("expected denial when the role does not exist (fail closed)")
	}
}

func TestRenderEnrollSnippetIncludesEndpointsNotSecrets(t *testing.T) {
	out := RenderEnrollSnippet("https://harbor.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"export AT_HARBOR_IDENTITY_TOKEN=TOK123",
		"ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic",
		"ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN",
		`url."https://harbor.local.aethons.tools/git/".insteadOf`,
		// git must have a working, headless credential (username + env-only password),
		// so `git clone` through harbor doesn't prompt.
		`credential."https://harbor.local.aethons.tools".helper`,
		"username=x-access-token",
		"password=$AT_HARBOR_IDENTITY_TOKEN",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
	// The raw token must appear exactly once — only in AT_HARBOR_IDENTITY_TOKEN.
	// Everything else references the env var, so the token never lands in gitconfig.
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1 (env-only)\n---\n%s", n, out)
	}
}
