// Package covemasterbin embeds the linux cove-master binaries into at-cove, so
// the at-cove that builds a cove image installs the *exact* matching cove-master
// into the hardening layer — version lockstep, no coordination, no pin to drift
// (mirrors internal/atswitchboard, COV-135, and internal/attask, COV-36). Both
// linux arches are embedded regardless of at-cove's own host, since at-cove may
// build a sandbox for either VM arch.
package covemasterbin

import (
	"embed"
	"fmt"
	"io/fs"
)

// binFS holds the linux cove-master binaries. Only bin/README + bin/.gitignore
// are tracked; the binaries (bin/cove-master-linux-{amd64,arm64}) are gitignored
// build artifacts, staged by scripts/stage-attask.sh before at-cove is built so
// this embed picks them up. A fresh checkout embeds just the placeholder, so the
// package still compiles — Binary then errors actionably at runtime.
//
//go:embed bin
var binFS embed.FS

// Binary returns the embedded linux cove-master binary for goarch ("amd64" or
// "arm64"). It errors if the binary was not staged (a plain `go build` without
// the pre-step) rather than shipping a broken sandbox.
func Binary(goarch string) ([]byte, error) {
	return lookup(binFS, goarch)
}

// BinFS returns the embedded cove-master binaries as a read-only FS (rooted at
// "bin"). internal/install hashes it as part of at-cove's build identity for the
// install currency check (COV-38), so a cove-master rebuild invalidates installs.
func BinFS() fs.FS { return binFS }

func lookup(fsys fs.FS, goarch string) ([]byte, error) {
	name := "bin/cove-master-linux-" + goarch
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("embedded cove-master for linux/%s not staged — run scripts/stage-attask.sh (or use a release build): %w", goarch, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("embedded cove-master for linux/%s is empty", goarch)
	}
	return b, nil
}
