package snippet

import (
	"strings"
	"testing"
)

func TestRenderIncludesEndpointsNotSecrets(t *testing.T) {
	out := Render("https://jam.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"export AT_JAM_IDENTITY_TOKEN=TOK123",
		// Deprecated alias for older images, exported FROM the new variable so the
		// raw token is still written once.
		`export AT_HARBOR_IDENTITY_TOKEN="$AT_JAM_IDENTITY_TOKEN"`,
		"ANTHROPIC_BASE_URL=https://jam.local.aethons.tools/anthropic",
		"ANTHROPIC_API_KEY=$AT_JAM_IDENTITY_TOKEN",
		`url."https://jam.local.aethons.tools/git/".insteadOf`,
		`credential."https://jam.local.aethons.tools".helper`,
		"username=x-access-token",
		"password=${AT_JAM_IDENTITY_TOKEN:-$AT_HARBOR_IDENTITY_TOKEN}",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
	// The raw token must appear exactly once — only in AT_JAM_IDENTITY_TOKEN.
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1 (env-only)\n---\n%s", n, out)
	}
}

func TestRenderTrimsTrailingSlash(t *testing.T) {
	out := Render("https://jam.local/", "T")
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://jam.local/anthropic") {
		t.Fatalf("trailing slash not trimmed:\n%s", out)
	}
}

func TestEnv(t *testing.T) {
	e := Env("https://jam.local/", "TOK")
	if e["ANTHROPIC_BASE_URL"] != "https://jam.local/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", e["ANTHROPIC_BASE_URL"])
	}
	// Writers set both the new and the deprecated name (older images read the
	// old one); both live in this one in-memory map, never argv or disk.
	if e["ANTHROPIC_API_KEY"] != "TOK" || e["AT_JAM_IDENTITY_TOKEN"] != "TOK" || e["AT_HARBOR_IDENTITY_TOKEN"] != "TOK" {
		t.Fatalf("token env = %+v", e)
	}
}

func TestGitConfigHasNoToken(t *testing.T) {
	g := GitConfig("https://jam.local")
	for _, want := range []string{
		`url."https://jam.local/git/".insteadOf https://github.com/`,
		`credential."https://jam.local".helper`,
		// the helper reads the new name, falling back to the deprecated one
		"password=${AT_JAM_IDENTITY_TOKEN:-$AT_HARBOR_IDENTITY_TOKEN}",
	} {
		if !strings.Contains(g, want) {
			t.Fatalf("GitConfig missing %q\n---\n%s", want, g)
		}
	}
	// GitConfig takes no token — it is token-free by construction; the helper
	// reads the value from the environment at run time.
}
