package install

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/attask"
)

// CurrencyInputs are the build-affecting inputs a run command recomputes to
// decide whether an install is still current (§5). Each field is an independent
// digest so the combined CurrencyHash changes iff at least one input changed.
type CurrencyInputs struct {
	KitSourceTree       string // hash of config.yml + the kit's image/ tree (KitSourceTree)
	AtCoveBuildIdentity string // hash of at-cove's embedded sealed layer + at-task binaries (AtCoveIdentity)
	BaseRef             string // the configured base string, or the blessed default ref
}

// CurrencyHash combines the build-affecting inputs into the manifest's currency
// hash: sha256( kitSourceTree ‖ atCoveBuildIdentity ‖ baseRef ). Pure — no I/O.
func CurrencyHash(in CurrencyInputs) string {
	h := sha256.New()
	writeField(h, []byte(in.KitSourceTree))
	writeField(h, []byte(in.AtCoveBuildIdentity))
	writeField(h, []byte(in.BaseRef))
	return hex.EncodeToString(h.Sum(nil))
}

// writeField feeds a length-prefixed field into h, so a sequence of fields hashes
// unambiguously — no boundary shift between adjacent fields can collide (a naive
// concatenation would let "a"+"bc" hash the same as "ab"+"c").
func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// HashTree returns a deterministic digest over every regular file reachable in
// fsys: the path-sorted (path, content) pairs. Two trees with identical files
// hash equal regardless of walk order; any edited, added, removed, or renamed
// file changes the digest. Directories and symlinks contribute nothing beyond
// the files under them.
func HashTree(fsys fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", err
		}
		writeField(h, []byte(p))
		writeField(h, b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// KitSourceTree hashes the kit inputs that feed the build (§5): config.yml plus
// everything under the kit's image/ dir (Dockerfile + image-files). It hashes
// source only — it never assembles .build — so a run command's currency check
// cannot race a concurrent build. A kit with no image/ dir hashes over config.yml
// alone. It errors if config.yml is absent.
func KitSourceTree(kitDir string) (string, error) {
	h := sha256.New()

	cfg, err := os.ReadFile(filepath.Join(kitDir, "config.yml"))
	if err != nil {
		return "", err
	}
	writeField(h, []byte("config.yml"))
	writeField(h, cfg)

	imgDir := filepath.Join(kitDir, "image")
	switch info, err := os.Stat(imgDir); {
	case err == nil && info.IsDir():
		tree, err := HashTree(os.DirFS(imgDir))
		if err != nil {
			return "", err
		}
		writeField(h, []byte("image/"))
		writeField(h, []byte(tree))
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AtCoveIdentity hashes at-cove's embedded build contributions (§5): the sealed
// hardening layer and the embedded at-task binaries. An at-cove upgrade that
// changes either flips the digest, invalidating every install. install (S2) and
// the run commands both call this, so they agree on the identity by construction.
func AtCoveIdentity() (string, error) {
	h := sha256.New()

	hard, err := HashTree(assemble.HardeningFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("hardening"))
	writeField(h, []byte(hard))

	at, err := HashTree(attask.BinFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("attask"))
	writeField(h, []byte(at))

	return hex.EncodeToString(h.Sum(nil)), nil
}
