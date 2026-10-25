// Package gitsource imports studio kits from git repositories. A checkout is
// untrusted input: nothing a kit names may resolve outside it.
package gitsource

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitFile is the kit's definition file at the root of its directory.
const KitFile = kittree.KitFile

// Load reads the kit directory dir (slash, relative to root; "." for the
// root) of the checkout at root into a pushable kit: its files
// (kittree.ReadDir: .dockerignore applied; symlinks, hard links, irregular
// files and unsafe names refused; the tree limits enforced as it reads), then
// the one ingest pipeline (studio.ParseFiles: no packed-context field,
// kit.yml parsed with its note files read from the directory), and the
// push-time authoring checks. The name hint is the kit's name: field, else
// the base of dir, else "" (dir is the root: the caller names it).
func Load(root, dir string) (studio.StudioKit, []kittree.File, string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return studio.StudioKit{}, nil, "", errors.New("kit root cannot be opened")
	}
	defer r.Close()
	files, err := kittree.ReadDir(r, dir)
	if err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	_, _, sk, err := studio.ParseFiles(files)
	if err != nil {
		if _, isTree := errors.AsType[*studio.TreeError](err); isTree {
			err = inDir(dir, err)
		}
		return studio.StudioKit{}, nil, "", err
	}
	if err := sk.CheckPrompt(); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	if err := sk.CheckNotes(); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	return sk, files, nameHint(sk, dir), nil
}

// inDir locates a refusal of dir's files as a tree at dir.
func inDir(dir string, err error) error {
	if d := path.Clean(dir); d != "." {
		return fmt.Errorf("%s: %w", d, err)
	}
	return err
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
// with a fixed message, and a kit.yml that cannot be read is ErrNoKitFile.
// Load's ReadDir then refuses any symlink inside dir. Other failures are Load's.
func LoadDir(root, dir string) (studio.StudioKit, []kittree.File, string, error) {
	dir = path.Clean(dir)
	if !filepath.IsLocal(filepath.FromSlash(dir)) {
		return studio.StudioKit{}, nil, "", ErrOutsideCheckout
	}
	if err := checkDir(dir); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	if _, err := studio.ReadFileInRoot(root, filepath.Join(filepath.FromSlash(dir), KitFile)); err != nil {
		return studio.StudioKit{}, nil, "", ErrNoKitFile
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
