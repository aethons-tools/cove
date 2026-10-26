package modelspec

import "testing"

// Every sealed-owned or security-relevant variable must stay protected, and an
// ordinary provider variable must not be.
func TestProtectedEnvKeyCoversEveryEntry(t *testing.T) {
	for _, k := range []string{
		"http_proxy", "https_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"CLAUDE_CONFIG_DIR", "GOOGLE_APPLICATION_CREDENTIALS", "PATH",
	} {
		if !ProtectedEnvKey(k) {
			t.Errorf("ProtectedEnvKey(%q) = false, want true", k)
		}
	}
	if len(protectedEnvKeys) != 9 {
		t.Errorf("protected list has %d entries; update this test with the new ones", len(protectedEnvKeys))
	}
	if ProtectedEnvKey("ANTHROPIC_BASE_URL") {
		t.Error("ANTHROPIC_BASE_URL must not be protected")
	}
}
