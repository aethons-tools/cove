// Package gitsource imports studio kits from git repositories. A checkout is
// untrusted input: nothing a kit names may resolve outside it.
package gitsource

import (
	"errors"
	"path"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/studio"
)

// KitFile is the one file Jam reads from a source's directory.
const KitFile = "kit.yml"

// Load reads <dir>/kit.yml from the checkout at root and resolves it into a
// pushable kit: it parses and validates the kit, packs its context-dir and
// inlines its note files — all refused unless they resolve inside root — and
// applies the push-time authoring checks. dir is slash-separated and relative
// to root ("." for the root). The name hint is the kit's name: field, else
// the base of dir, else "" (dir is the root: the caller names it).
func Load(root, dir string) (studio.StudioKit, string, error) {
	data, err := studio.ReadFileInRoot(root, filepath.Join(filepath.FromSlash(dir), KitFile))
	if err != nil {
		return studio.StudioKit{}, "", err
	}
	sk, err := studio.ParseStudioKit(data)
	if err != nil {
		return studio.StudioKit{}, "", err
	}
	if err := sk.ResolveInRoot(root, filepath.FromSlash(dir)); err != nil {
		return studio.StudioKit{}, "", err
	}
	if err := sk.CheckPrompt(); err != nil {
		return studio.StudioKit{}, "", err
	}
	if err := sk.CheckNotes(); err != nil {
		return studio.StudioKit{}, "", err
	}
	return sk, nameHint(sk, dir), nil
}

// ErrOutsideCheckout is LoadDir's refusal of a dir that leaves the checkout.
var ErrOutsideCheckout = errors.New("dir leaves the checkout")

// ErrNoKitFile is LoadDir's refusal of a dir whose kit.yml cannot be read:
// missing, resolving outside the checkout (alike, so no oracle) or unreadable.
var ErrNoKitFile = errors.New("no kit.yml")

// LoadDir is Load for a dir computed from a path parent reference rather
// than taken from a validated Source: a dir that is not local after
// path.Clean (absolute, or climbing out with "..") is ErrOutsideCheckout, and
// a dir a Source could not hold (space, quote, control character) is refused
// with a fixed message, and a kit.yml that cannot be read is ErrNoKitFile. Load's jail then guards
// symlinks inside dir. Other failures are Load's.
func LoadDir(root, dir string) (studio.StudioKit, string, error) {
	dir = path.Clean(dir)
	if !filepath.IsLocal(filepath.FromSlash(dir)) {
		return studio.StudioKit{}, "", ErrOutsideCheckout
	}
	if err := checkDir(dir); err != nil {
		return studio.StudioKit{}, "", err
	}
	if _, err := studio.ReadFileInRoot(root, filepath.Join(filepath.FromSlash(dir), KitFile)); err != nil {
		return studio.StudioKit{}, "", ErrNoKitFile
	}
	return Load(root, dir)
}

func nameHint(sk studio.StudioKit, dir string) string {
	if sk.LegacyName != "" {
		return sk.LegacyName
	}
	if b := path.Base(path.Clean(dir)); b != "." && b != "/" {
		return b
	}
	return ""
}
