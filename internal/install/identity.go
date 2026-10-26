// Package install computes the assembly identity: the digest of the sealed
// hardening layer, harness payload and embedded cove-master that keys every
// studio image tag (AtCoveIdentity).
package install

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io/fs"
	"sort"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/covemasterbin"
	"github.com/aethons-tools/cove/internal/harnessinstall"
)

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

// harnessBaseline is the harness layer's rendered baseline preferences (a
// var so a test can swap it).
var harnessBaseline = harnessinstall.BaselineSettings

// coveMasterFS is the embedded cove-master binaries (a var so a test can swap
// them).
var coveMasterFS = covemasterbin.BinFS

// AtCoveIdentity hashes the binary's embedded build contributions: the sealed
// hardening layer (and assemble.Revision), the harness layer's payload (its
// scripts and the managed settings' sandbox policy) and its rendered baseline
// preferences — NOT any install's version/plugins, which are per-image (the
// studio build-digest) — and the embedded cove-master binaries. An upgrade that
// changes any of these flips the digest, so every studio image tag changes
// (internal/jam/launcher asm.go keys the tag on it).
func AtCoveIdentity() (string, error) {
	h := sha256.New()

	hard, err := HashTree(assemble.HardeningFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("hardening"))
	writeField(h, []byte(hard))
	writeField(h, []byte("assemble-revision"))
	writeField(h, []byte(assemble.Revision))

	hp, err := HashTree(harnessinstall.PayloadFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("harness"))
	writeField(h, []byte(hp))
	writeField(h, []byte("harness-baseline"))
	writeField(h, harnessBaseline())

	cm, err := HashTree(coveMasterFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("covemaster"))
	writeField(h, []byte(cm))

	return hex.EncodeToString(h.Sum(nil)), nil
}
