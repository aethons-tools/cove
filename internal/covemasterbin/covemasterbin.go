// Package covemasterbin embeds the linux cove-master binaries into at-jam, so
// the launcher that builds a studio image installs the *exact* matching
// cove-master into the hardening layer — version lockstep, no coordination, no
// pin to drift. Both linux arches are embedded regardless of the host, since a
// studio may run on either VM arch.
package covemasterbin

import (
	"embed"
	"fmt"
	"io/fs"
)

// binFS holds the linux cove-master binaries. Only bin/README + bin/.gitignore
// are tracked; the binaries (bin/cove-master-linux-{amd64,arm64}) are gitignored
// build artifacts, staged by scripts/stage-covemaster.sh before at-jam is built so
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
// "bin"). internal/install hashes it as part of the assembly identity (AtCoveIdentity),
// so a cove-master rebuild retags every studio image.
func BinFS() fs.FS { return binFS }

func lookup(fsys fs.FS, goarch string) ([]byte, error) {
	name := "bin/cove-master-linux-" + goarch
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("embedded cove-master for linux/%s not staged — run scripts/stage-covemaster.sh (or use a release build): %w", goarch, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("embedded cove-master for linux/%s is empty", goarch)
	}
	return b, nil
}
