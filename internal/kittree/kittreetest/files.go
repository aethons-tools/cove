// Package kittreetest builds kit trees for tests: the one shared
// path → content file builder every package's kit-tree tests use.
package kittreetest

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree"
)

// Files is the files of path → content pairs kv; a "*" suffix on a path makes
// that file 0755, every other file is 0644. A trailing odd element is ignored.
func Files(kv ...string) []kittree.File {
	var out []kittree.File
	for i := 0; i+1 < len(kv); i += 2 {
		p, mode := kv[i], 0o644
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		out = append(out, kittree.File{Path: p, Mode: fs.FileMode(mode), Data: []byte(kv[i+1])})
	}
	return out
}

// Tree builds Files(kv...) into a tree and its blobs, failing t on a refusal.
func Tree(t testing.TB, kv ...string) (kittree.Tree, map[string][]byte) {
	t.Helper()
	tr, blobs, err := kittree.Build(Files(kv...))
	if err != nil {
		t.Fatal(err)
	}
	return tr, blobs
}
