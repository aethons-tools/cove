package assemble

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbedsContainKeyFiles(t *testing.T) {
	for _, p := range []string{
		"hardening/Dockerfile",
		"hardening/image-files/etc/nftables.conf",
		"hardening/image-files/etc/squid/squid.conf",
		"hardening/image-files/etc/ssh/sshd_config.d/atsbx.conf",
		"hardening/image-files/etc/claude-code/managed-settings.json",
	} {
		if _, err := fs.Stat(hardeningFS, p); err != nil {
			t.Errorf("hardeningFS missing %s: %v", p, err)
		}
	}
	if _, err := fs.Stat(overridableFS, "overridable/image-files/home/agent/.init-agent-data/CLAUDE.md"); err != nil {
		t.Errorf("overridableFS missing CLAUDE.md: %v", err)
	}
}

// TestManagedSettingsForceOAuth guards the policy that the non-overridable
// managed settings require subscription/OAuth login (forceLoginMethod=claudeai),
// which is what makes remote control work and blocks API-key fallback. Only
// managed settings enforce this, so the key must live in the hardening layer.
func TestManagedSettingsForceOAuth(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/claude-code/managed-settings.json")
	if err != nil {
		t.Fatalf("managed-settings.json not embedded: %v", err)
	}
	if !strings.Contains(string(b), `"forceLoginMethod": "claudeai"`) {
		t.Errorf("managed-settings.json must set forceLoginMethod=claudeai; got:\n%s", b)
	}
}

// TestAllowlistPermitsClaudeAI guards that the egress allowlist permits the
// claude.ai OAuth login endpoint (not covered by .claude.com — different TLD).
func TestAllowlistPermitsClaudeAI(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		t.Fatalf("allowed_domains.txt not embedded: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "claude.ai" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("allowed_domains.txt must permit claude.ai for OAuth login; got:\n%s", b)
	}
}
