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

// Blessed returns the trusted cove-base-image manifest digests (sha256:…),
// newest first. Blank lines and #-comments in the file are ignored.
func Blessed() []string {
	return parse(raw)
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
