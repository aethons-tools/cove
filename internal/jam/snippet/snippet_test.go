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

func TestEnv(t *testing.T) {
	e := Env("https://harbor.local/", "TOK")
	if e["ANTHROPIC_BASE_URL"] != "https://harbor.local/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", e["ANTHROPIC_BASE_URL"])
	}
	if e["ANTHROPIC_API_KEY"] != "TOK" || e["AT_HARBOR_IDENTITY_TOKEN"] != "TOK" {
		t.Fatalf("token env = %+v", e)
	}
}

func TestGitConfigHasNoToken(t *testing.T) {
	g := GitConfig("https://harbor.local")
	for _, want := range []string{
		`url."https://harbor.local/git/".insteadOf https://github.com/`,
		`credential."https://harbor.local".helper`,
		"password=$AT_HARBOR_IDENTITY_TOKEN",
	} {
		if !strings.Contains(g, want) {
			t.Fatalf("GitConfig missing %q\n---\n%s", want, g)
		}
	}
	// GitConfig takes no token — it is token-free by construction; the helper
	// reads the value from the environment at run time.
}
