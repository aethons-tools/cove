package assemble

import (
	"embed"
	"io/fs"
	"strings"
)

// hardeningFS holds the non-overridable sealed layer (Dockerfile + image-files).
// "all:" includes dotfiles. The overridable defaults it used to sit above now
// live in cove-base-image (COV-34), so hardening COPYs only sealed files.
//
//go:embed all:hardening
var hardeningFS embed.FS

// HardeningFS returns the embedded sealed hardening layer (Dockerfile +
// image-files) as a read-only FS rooted at "hardening". internal/install hashes
// it into the assembly identity (AtCoveIdentity) that keys every studio image
// tag; assembly itself copies it via copyEmbed.
func HardeningFS() fs.FS { return hardeningFS }

// SealedEgressBase is the sealed hardening layer's always-on egress list
// (/etc/squid/allowed_domains.txt) as baked into every image this binary
// assembles — the "sealed" rows of Jam's setup inspection.
func SealedEgressBase() ([]string, error) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, nil
}
