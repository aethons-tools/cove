// Package baseimage holds the pure logic of COV-34's provenance gate: given the
// OCI rootfs layer diff_ids of a resolved base image and of the blessed
// cove-base-image(s), decide whether the base descends from a blessed one.
//
// An image built `FROM X` carries X's diff_ids as the exact prefix of its own,
// so "descends from" is a prefix test — unforgeable, since matching the prefix
// means those bottom layers *are* the blessed image, byte-for-byte. The
// registry/docker execution that fetches the diff_ids lives in the backend; this
// package is deliberately I/O-free so it can be exhaustively unit-tested.
package baseimage

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseLayers decodes the output of
// `docker inspect --format '{{json .RootFS.Layers}}'` — a JSON array of diff_id
// strings — into an ordered slice.
func ParseLayers(inspectJSON string) ([]string, error) {
	var layers []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(inspectJSON)), &layers); err != nil {
		return nil, fmt.Errorf("parse image layers %q: %w", strings.TrimSpace(inspectJSON), err)
	}
	return layers, nil
}

// DescendsFrom reports whether an image with layers `child` was built from an
// image whose layers are `parent` — i.e. parent is a non-empty exact prefix of
// child. An empty parent or child never matches: an empty parent would match
// everything (defeating the gate), and an empty child has no provenance to
// trust.
func DescendsFrom(child, parent []string) bool {
	if len(parent) == 0 || len(child) < len(parent) {
		return false
	}
	for i := range parent {
		if child[i] != parent[i] {
			return false
		}
	}
	return true
}

// Verify reports whether `child` descends from ANY of the blessed layer sets —
// the rolling set of blessed cove-base-image publishes, so a base built on an
// earlier-but-still-blessed publish is accepted.
func Verify(child []string, blessed [][]string) bool {
	for _, b := range blessed {
		if DescendsFrom(child, b) {
			return true
		}
	}
	return false
}
