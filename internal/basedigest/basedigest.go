// Package basedigest holds the blessed jam-studio-base manifest digests that a
// kit's resolved base must descend from — COV-34's provenance gate compares the
// base's OCI layer diff_ids against the blessed image's. The digests are embedded
// into jam (go:embed), so the gate runs fully offline at build time.
//
// The set is generated at build time from the registry, bounded by a committed
// low-watermark — see internal/blessgen and cmd/gen-blessed (COV-44). The embed
// covers a directory of up to three files:
//
//   - blessed/watermark.txt — committed: the single low-watermark digest.
//   - blessed/generated.txt — gitignored: the registry snapshot cmd/gen-blessed
//     writes before `go build`. Absent in a fresh checkout and offline builds.
//   - blessed/local.txt — gitignored: a locally built base's ref, written by
//     `just local` (scripts/local.sh); a release never has one (the goreleaser
//     before-hook refuses it). See LocalRef.
//
// Blessed prefers generated.txt and falls back to watermark.txt, so a build with
// registry access trusts every current base while an offline build (or a fresh
// clone) still compiles and trusts the watermark alone. Writing only the
// gitignored file keeps the working tree clean — goreleaser's dirty-tree check
// (and reproducible builds) stay happy.
package basedigest

import (
	"bufio"
	"embed"
	"io/fs"
	"strings"
)

//go:embed blessed
var blessedFS embed.FS

// files is what the functions below read (blessedFS; tests swap it).
var files fs.ReadFileFS = blessedFS

// Image is the published jam-studio-base repository the blessed digests name.
const Image = "ghcr.io/aethons-tools/jam-studio-base"

// Blessed returns the trusted jam-studio-base manifest digests (sha256:…),
// newest first. It reads the build-time registry snapshot (blessed/generated.txt)
// when present, else the committed low-watermark (blessed/watermark.txt). Blank
// lines and #-comments are ignored.
func Blessed() []string {
	if b, err := files.ReadFile("blessed/generated.txt"); err == nil {
		if digests := parse(string(b)); len(digests) > 0 {
			return digests
		}
	}
	b, _ := files.ReadFile("blessed/watermark.txt")
	return parse(string(b))
}

// LocalRef is the locally built jam-studio-base this binary was built against
// (blessed/local.txt: one image ref, e.g. jam-studio-base:dev-0123abcd4567),
// or "" — always "" in a release. When set it is the default base and the
// first blessed ref, so a local jam builds studios FROM the local image and
// trusts kits built FROM it; the published bases stay trusted too.
func LocalRef() string {
	b, err := files.ReadFile("blessed/local.txt")
	if err != nil {
		return ""
	}
	refs := parse(string(b))
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// DefaultRef is the base Jam's launcher hardens when a kit selects none: the
// local base (LocalRef) when this is a `just local` build, else the newest
// blessed jam-studio-base, pinned by digest.
func DefaultRef() string {
	if l := LocalRef(); l != "" {
		return l
	}
	return Image + "@" + Blessed()[0]
}

// BlessedRefs pins each blessed digest to Image — the concrete refs the
// provenance gate inspects for their layer diff_ids — after the local base,
// if any.
func BlessedRefs() []string {
	var refs []string
	if l := LocalRef(); l != "" {
		refs = append(refs, l)
	}
	for _, d := range Blessed() {
		refs = append(refs, Image+"@"+d)
	}
	return refs
}

func parse(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// VersionNote is what `version` appends on a `just local` build, so one is
// never mistaken for a release: " (local studio base <ref>)", else "".
func VersionNote() string {
	if l := LocalRef(); l != "" {
		return " (local studio base " + l + ")"
	}
	return ""
}
