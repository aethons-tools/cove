// Package basedigest holds the blessed cove-base-image manifest digests that a
// kit's resolved base must descend from — COV-34's provenance gate compares the
// base's OCI layer diff_ids against the blessed image's. The digests are embedded
// into at-cove (go:embed), so the gate runs fully offline at create time.
//
// The set is generated at build time from the registry, bounded by a committed
// low-watermark — see internal/blessgen and cmd/gen-blessed (COV-44). The embed
// covers a directory of two files:
//
//   - blessed/watermark.txt — committed: the single low-watermark digest.
//   - blessed/generated.txt — gitignored: the registry snapshot cmd/gen-blessed
//     writes before `go build`. Absent in a fresh checkout and offline builds.
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
	"strings"
)

//go:embed blessed
var blessedFS embed.FS

// Image is the published cove-base-image repository the blessed digests name.
const Image = "ghcr.io/aethons-tools/cove-base-image"

// Blessed returns the trusted cove-base-image manifest digests (sha256:…),
// newest first. It reads the build-time registry snapshot (blessed/generated.txt)
// when present, else the committed low-watermark (blessed/watermark.txt). Blank
// lines and #-comments are ignored.
func Blessed() []string {
	if b, err := blessedFS.ReadFile("blessed/generated.txt"); err == nil {
		if digests := parse(string(b)); len(digests) > 0 {
			return digests
		}
	}
	b, _ := blessedFS.ReadFile("blessed/watermark.txt")
	return parse(string(b))
}

// DefaultRef is the base at-cove hardens when a kit selects none: the newest
// blessed cove-base-image, pinned by digest.
func DefaultRef() string {
	return Image + "@" + Blessed()[0]
}

// BlessedRefs pins each blessed digest to Image — the concrete refs the
// provenance gate inspects for their layer diff_ids.
func BlessedRefs() []string {
	blessed := Blessed()
	refs := make([]string, len(blessed))
	for i, d := range blessed {
		refs[i] = Image + "@" + d
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
