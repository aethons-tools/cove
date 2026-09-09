// Package atswitchboard embeds the linux at-switchboard binaries into at-cove, so
// the at-cove that orchestrates a dispatch injects the *exact* matching
// at-switchboard into the hardening layer it builds — version lockstep, no
// coordination, no pin to drift (mirrors internal/attask, COV-36). Both linux
// arches are embedded regardless of at-cove's own host, since at-cove may build a
// sandbox for either VM arch.
package atswitchboard

import (
	"embed"
	"fmt"
	"io/fs"
)

// binFS holds the linux at-switchboard binaries. Only bin/README is tracked; the
// binaries (bin/at-switchboard-linux-{amd64,arm64}) are gitignored build
// artifacts, staged by scripts/build.sh and the release workflow *before*
// at-cove is built so this embed picks them up. A fresh checkout embeds just the
// placeholder, so the package still compiles — Binary then errors actionably at
// runtime.
//
//go:embed bin
var binFS embed.FS

// Binary returns the embedded linux at-switchboard binary for goarch ("amd64" or
// "arm64"). It errors if the binary was not staged (a plain `go build` without
// the pre-step) rather than shipping a broken sandbox.
func Binary(goarch string) ([]byte, error) {
	return lookup(binFS, goarch)
}

// BinFS returns the embedded at-switchboard binaries as a read-only FS (rooted at
// "bin"). internal/install hashes it as part of at-cove's build identity for the
// install currency check (COV-38), so an at-switchboard rebuild invalidates
// installs.
func BinFS() fs.FS { return binFS }

func lookup(fsys fs.FS, goarch string) ([]byte, error) {
	name := "bin/at-switchboard-linux-" + goarch
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("embedded at-switchboard for linux/%s not staged — run scripts/build.sh (or use a release build): %w", goarch, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("embedded at-switchboard for linux/%s is empty", goarch)
	}
	return b, nil
}
