package snippet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDummyCredentialsUsesIdentityAsAccessToken(t *testing.T) {
	far := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := DummyCredentials("TOK123", far)
	if err != nil {
		t.Fatalf("DummyCredentials: %v", err)
	}
	var parsed struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.ClaudeAiOauth.AccessToken != "TOK123" {
		t.Fatalf("accessToken = %q, want the identity token", parsed.ClaudeAiOauth.AccessToken)
	}
	if parsed.ClaudeAiOauth.ExpiresAt != far.UnixMilli() {
		t.Fatalf("expiresAt = %d, want far-future ms %d", parsed.ClaudeAiOauth.ExpiresAt, far.UnixMilli())
	}
}

func TestRenderSubscriptionOmitsAPIKey(t *testing.T) {
	out := RenderSubscription("https://jam.local", "TOK123")
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
}
