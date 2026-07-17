// Package basedigest holds the blessed cove-base-image manifest digests that a
// kit's resolved base must descend from — COV-34's provenance gate compares the
// base's OCI layer diff_ids against the blessed image's. The list is committed
// in-repo and embedded into at-cove, so builds are reproducible and need no
// registry access to know what is trusted; the cove-base-image publish appends a
// new digest to the file on each republish (COV-36).
package basedigest

import (
	"bufio"
	_ "embed"
	"strings"
)

//go:embed blessed-digests.txt
var raw string

// Image is the published cove-base-image repository the blessed digests name.
const Image = "ghcr.io/aethons-tools/cove-base-image"

// Blessed returns the trusted cove-base-image manifest digests (sha256:…),
// newest first. Blank lines and #-comments in the file are ignored.
func Blessed() []string {
	return parse(raw)
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
