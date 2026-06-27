package assemble

import (
	"io/fs"
	"testing"
)

func TestEmbedsContainKeyFiles(t *testing.T) {
	for _, p := range []string{
		"hardening/Dockerfile",
		"hardening/image-files/etc/nftables.conf",
		"hardening/image-files/etc/squid/squid.conf",
		"hardening/image-files/etc/ssh/sshd_config.d/atsbx.conf",
	} {
		if _, err := fs.Stat(hardeningFS, p); err != nil {
			t.Errorf("hardeningFS missing %s: %v", p, err)
		}
	}
	if _, err := fs.Stat(overridableFS, "overridable/image-files/home/agent/.init-agent-data/CLAUDE.md"); err != nil {
		t.Errorf("overridableFS missing CLAUDE.md: %v", err)
	}
}
