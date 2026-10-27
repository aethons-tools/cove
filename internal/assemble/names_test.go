package assemble

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// The sealed hardening layer's files carry the jam names (slice 3), and the
// sshd drop-in sorts first: sshd keeps the first TrustedUserCAKeys it reads,
// in lexical order, so the sealed one must precede any kit-supplied drop-in.
func TestHardeningLayerNames(t *testing.T) {
	var paths []string
	if err := fs.WalkDir(HardeningFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	has := func(suffix string) bool {
		return slices.ContainsFunc(paths, func(p string) bool { return strings.HasSuffix(p, suffix) })
	}
	for _, want := range []string{
		"etc/systemd/system/jam-egress.service",
		"etc/systemd/system/ssh.service.d/jam-egress.conf",
		"etc/systemd/system/squid.service.d/jam-egress.conf",
		"etc/systemd/system/docker.service.d/jam-egress.conf",
		"etc/ssh/sshd_config.d/00-jam.conf",
		"usr/local/bin/jam-git-credential.sh",
		"usr/local/lib/jam/apply-sshenv.sh",
		"usr/local/lib/jam/apply-role-egress.sh",
		"usr/local/lib/jam/apply-session-domains.sh",
		"usr/local/lib/jam/install-ssh-ca.sh",
		"usr/local/lib/jam/seed-agent-data.sh",
	} {
		if !has(want) {
			t.Errorf("hardening layer lacks %s", want)
		}
	}
	var dropins []string
	for _, p := range paths {
		if strings.Contains(p, "etc/ssh/sshd_config.d/") {
			dropins = append(dropins, p[strings.LastIndex(p, "/")+1:])
		}
		if strings.Contains(p, "cove") {
			t.Errorf("hardening path %s keeps a pre-rename name", p)
		}
	}
	if len(dropins) != 1 || !strings.HasPrefix(dropins[0], "00-") {
		t.Fatalf("sshd drop-ins = %v; want exactly one, sorting first (00-…)", dropins)
	}
}
