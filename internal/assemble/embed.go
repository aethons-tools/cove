package assemble

import "embed"

// hardeningFS holds the non-overridable layer (Dockerfile + image-files).
// "all:" includes dotfiles.
//
//go:embed all:hardening
var hardeningFS embed.FS

// overridableFS holds the overridable defaults layer.
//
//go:embed all:overridable
var overridableFS embed.FS
