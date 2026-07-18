package assemble

import (
	"embed"
	"io/fs"
)

// hardeningFS holds the non-overridable sealed layer (Dockerfile + image-files).
// "all:" includes dotfiles. The overridable defaults it used to sit above now
// live in cove-base-image (COV-34), so hardening COPYs only sealed files.
//
//go:embed all:hardening
var hardeningFS embed.FS

// HardeningFS returns the embedded sealed hardening layer (Dockerfile +
// image-files) as a read-only FS rooted at "hardening". internal/install hashes
// it as part of at-cove's build identity for the install currency check (COV-38);
// assembly itself copies it via copyEmbed.
func HardeningFS() fs.FS { return hardeningFS }
