package assemble

import "embed"

// hardeningFS holds the non-overridable sealed layer (Dockerfile + image-files).
// "all:" includes dotfiles. The overridable defaults it used to sit above now
// live in cove-base-image (COV-34), so hardening COPYs only sealed files.
//
//go:embed all:hardening
var hardeningFS embed.FS
