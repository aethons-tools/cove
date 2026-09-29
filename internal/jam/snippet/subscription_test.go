package snippet

import (
	"strings"
	"testing"
)

func TestRenderSubscriptionSetsAuthTokenNotAPIKey(t *testing.T) {
	out := RenderSubscription("https://jam.local", "TOK123")
	// Identity rides on ANTHROPIC_AUTH_TOKEN (static bearer), referencing the
	// identity var so the raw token is written once.
	if !strings.Contains(out, "export ANTHROPIC_AUTH_TOKEN=$AT_JAM_IDENTITY_TOKEN") {
		t.Fatalf("subscription render must set ANTHROPIC_AUTH_TOKEN from the identity var:\n%s", out)
	}
	// NOT the API-key slot — that would force x-api-key mode.
	if strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Fatalf("subscription render must NOT set ANTHROPIC_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://jam.local/anthropic") {
		t.Fatalf("missing base URL:\n%s", out)
	}
	if !strings.Contains(out, "export AT_JAM_IDENTITY_TOKEN=TOK123") {
		t.Fatalf("missing identity token export:\n%s", out)
	}
	// git routing still present in subscription mode.
	if !strings.Contains(out, "git config --global") {
		t.Fatalf("missing git config:\n%s", out)
	}
	// The raw token is written exactly once (only in AT_JAM_IDENTITY_TOKEN).
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times, want exactly 1:\n%s", n, out)
	}
}
