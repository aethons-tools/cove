package snippet

import (
	"strings"
	"testing"
)

func TestRenderIncludesEndpointsNotSecrets(t *testing.T) {
	out := Render("https://jam.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"export JAM_IDENTITY_TOKEN=TOK123",
		"ANTHROPIC_BASE_URL=\"https://jam.local.aethons.tools/anthropic\"",
		"ANTHROPIC_API_KEY=\"${JAM_IDENTITY_TOKEN}\"",
		`url."https://jam.local.aethons.tools/git/".insteadOf`,
		`credential."https://jam.local.aethons.tools".helper`,
		"username=x-access-token",
		"password=${JAM_IDENTITY_TOKEN}",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
	// The raw token must appear exactly once — only in JAM_IDENTITY_TOKEN.
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1 (env-only)\n---\n%s", n, out)
	}
}

func TestRenderTrimsTrailingSlash(t *testing.T) {
	out := Render("https://jam.local/", "T")
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=\"https://jam.local/anthropic\"") {
		t.Fatalf("trailing slash not trimmed:\n%s", out)
	}
}

func TestEnv(t *testing.T) {
	e := Env("https://jam.local/", "TOK")
	if e["ANTHROPIC_BASE_URL"] != "https://jam.local/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", e["ANTHROPIC_BASE_URL"])
	}
	// The token lives in this one in-memory map, never argv or disk.
	if e["ANTHROPIC_API_KEY"] != "TOK" || e["JAM_IDENTITY_TOKEN"] != "TOK" || len(e) != 3 {
		t.Fatalf("token env = %+v", e)
	}
}

func TestGitConfigHasNoToken(t *testing.T) {
	g := GitConfig("https://jam.local")
	for _, want := range []string{
		`url."https://jam.local/git/".insteadOf 'https://github.com/'`,
		`credential."https://jam.local".helper`,
		// the helper reads the token from the environment
		"password=${JAM_IDENTITY_TOKEN}",
	} {
		if !strings.Contains(g, want) {
			t.Fatalf("GitConfig missing %q\n---\n%s", want, g)
		}
	}
	// GitConfig takes no token — it is token-free by construction; the helper
	// reads the value from the environment at run time.
}

func TestIdentityTokenVars(t *testing.T) {
	if got := IdentityTokenVars(); len(got) != 1 || got[0] != "JAM_IDENTITY_TOKEN" {
		t.Fatalf("IdentityTokenVars = %v", got)
	}
}
