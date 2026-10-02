package launcher

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/install"
)

// tagHalf is how many hex chars of each digest the image tag keeps: docker caps
// a tag at 128 chars, and 2×32 (+ "-") fits with 128 bits of each digest.
const tagHalf = 32

// asmDigest is the launcher's assembly fingerprint: everything a studio-kit
// build bakes in that is NOT in the kit's own BuildDigest — at-jam's embedded
// payload (the sealed hardening layer + at-task/at-switchboard/cove-master,
// hashed by install.AtCoveIdentity, the same identity the repo-kit currency check
// uses), the blessed default base (an omitted base builds FROM it; a context base
// descends from it), the Jam host (baked into the infra egress list) and the
// launcher's public key (baked into authorized_keys). Length-prefixed fields.
func asmDigest(identity, defaultRef, jamHost string, pub []byte) string {
	h := sha256.New()
	for _, f := range [][]byte{[]byte(identity), []byte(defaultRef), []byte(jamHost), pub} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// currentAsmDigest computes asmDigest for this binary and cfg. An identity error
// (an unreadable embed — never expected) degrades to an empty identity: the tag
// still varies with the other inputs, and the error is logged by the caller.
func currentAsmDigest(cfg Config) (string, error) {
	id, err := install.AtCoveIdentity()
	return asmDigest(id, basedigest.DefaultRef(), cfg.JamHost, cfg.PublicKey), err
}

// tagFor names a studio-kit image by its kit build-digest AND the launcher's
// assembly fingerprint, so a change to either rebuilds.
func tagFor(kitDigest, asm string) string {
	return "cove-kit:" + trim(kitDigest) + "-" + trim(asm)
}

func trim(d string) string {
	if len(d) > tagHalf {
		return d[:tagHalf]
	}
	return d
}

// imageTag is the tag this launcher builds, inventories and runs a kit under.
func (l *Launcher) imageTag(r KitRef) string { return tagFor(r.Digest, l.asm) }

// shortAsm is the fingerprint prefix logged when a kit is prepared, so an
// operator can see that a rebuild came from a Jam-side change.
func shortAsm(a string) string { return a[:min(12, len(a))] }
