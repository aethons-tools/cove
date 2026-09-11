package snippet

import (
	"strings"
	"testing"
)

func TestRenderIncludesEndpointsNotSecrets(t *testing.T) {
	out := Render("https://harbor.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"export AT_HARBOR_IDENTITY_TOKEN=TOK123",
		"ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic",
		"ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN",
		`url."https://harbor.local.aethons.tools/git/".insteadOf`,
		`credential."https://harbor.local.aethons.tools".helper`,
		"username=x-access-token",
		"password=$AT_HARBOR_IDENTITY_TOKEN",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
	// The raw token must appear exactly once — only in AT_HARBOR_IDENTITY_TOKEN.
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1 (env-only)\n---\n%s", n, out)
	}
}

func TestRenderTrimsTrailingSlash(t *testing.T) {
	out := Render("https://harbor.local/", "T")
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://harbor.local/anthropic") {
		t.Fatalf("trailing slash not trimmed:\n%s", out)
	}
}
