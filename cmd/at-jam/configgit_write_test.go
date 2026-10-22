package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// writeConfigTree writes Binary files byte for byte at their modes (past
// the umask, and over an existing file's mode).
func TestWriteConfigTreeBinaryModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "kits/k/context/run.sh"), "old", 0o600)
	blob := []byte{0xff, 0x00, 0xfe, 0x80}
	exp := jam.ConfigTreeExport{
		Files: map[string]string{"kits/k/kit.yml": "kind: studio\nbase:\n  context-dir: context\n"},
		Binary: map[string]jam.ConfigFileBlob{
			"kits/k/context/run.sh":       {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
			"kits/k/context/lib/blob.bin": {Data: blob, Mode: 0o644},
		},
	}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{"kits/k/context/run.sh": 0o755, "kits/k/context/lib/blob.bin": 0o644, "kits/k/kit.yml": 0o644} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, fi, err, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "kits/k/context/lib/blob.bin")); !bytes.Equal(b, blob) {
		t.Fatalf("blob %x", b)
	}
}

// Prune removes the files under a written kit's context dir that the
// export no longer holds — and nothing outside it.
func TestWriteConfigTreePrunesContextDir(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"kits/k/context/stale.txt", "kits/k/context/sub/old.bin", "kits/k/context/keep", "kits/k/notes.md",
		"kits/gone/context/Dockerfile", "kits/other/context/x", "README.md"} {
		mustWrite(t, filepath.Join(dir, p), "x", 0o644)
	}
	exp := jam.ConfigTreeExport{
		Files:  map[string]string{"kits/k/kit.yml": "kind: studio\n"},
		Binary: map[string]jam.ConfigFileBlob{"kits/k/context/keep": {Data: []byte("new"), Mode: 0o644}},
		Remove: []string{"kits/gone/kit.yml"},
		Prune:  []string{"kits/k/context", "kits/gone/context", "kits/none/context"},
	}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	want := map[string]string{"kits/k/kit.yml": "kind: studio\n", "kits/k/context/keep": "new", "kits/k/notes.md": "x",
		"kits/other/context/x": "x", "README.md": "x"}
	if len(got) != len(want) {
		t.Fatalf("tree %v", got)
	}
	for p, body := range want {
		if got[p] != body {
			t.Errorf("%s = %q, want %q", p, got[p], body)
		}
	}
	for _, d := range []string{"kits/k/context/sub", "kits/gone"} {
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Errorf("%s: empty dir left: %v", d, err)
		}
	}
}

// Prune does not follow a symlinked kit or context dir out of the tree.
func TestWriteConfigTreePruneStaysInTree(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(outside, "context/victim"), "x", 0o644)
	mustMkdir(t, filepath.Join(dir, "kits"))
	if err := os.Symlink(outside, filepath.Join(dir, "kits/k")); err != nil {
		t.Skip(err)
	}
	err := writeConfigTree(dir, jam.ConfigTreeExport{Prune: []string{"kits/k/context"}})
	if err == nil || !strings.Contains(err.Error(), "refusing to prune") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(filepath.Join(outside, "context/victim")); serr != nil {
		t.Fatal("pruned outside the tree")
	}
	// Nor does a write follow it out.
	err = writeConfigTree(dir, jam.ConfigTreeExport{Binary: map[string]jam.ConfigFileBlob{"kits/k/context/new": {Data: []byte("x"), Mode: 0o644}}})
	if err == nil {
		t.Fatal("wrote through a symlink out of the tree")
	}
	if _, serr := os.Stat(filepath.Join(outside, "context/new")); !os.IsNotExist(serr) {
		t.Fatal("a file landed outside the tree")
	}
}

// Prune keeps what the exported .dockerignore excludes: those files were
// never packed, so Jam's context says nothing about them.
func TestWriteConfigTreePruneKeepsDockerignored(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"kits/k/context/build.log", "kits/k/context/node_modules/x/y.js", "kits/k/context/stale.txt", "kits/k/context/keep.log"} {
		mustWrite(t, filepath.Join(dir, p), "x", 0o644)
	}
	exp := jam.ConfigTreeExport{
		Binary: map[string]jam.ConfigFileBlob{
			"kits/k/context/.dockerignore": {Data: []byte("*.log\nnode_modules\n!keep.log\n"), Mode: 0o644},
			"kits/k/context/Dockerfile":    {Data: []byte("FROM x\n"), Mode: 0o644},
		},
		Prune: []string{"kits/k/context"},
	}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	for _, p := range []string{"kits/k/context/build.log", "kits/k/context/node_modules/x/y.js"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s (ignored) was pruned", p)
		}
	}
	for _, p := range []string{"kits/k/context/stale.txt", "kits/k/context/keep.log"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s (not ignored, not exported) was kept", p)
		}
	}
}

// Every path the export names is checked before anything is written.
func TestWriteConfigTreeRefusesUnsafePaths(t *testing.T) {
	for name, exp := range map[string]jam.ConfigTreeExport{
		"binary-abs":    {Binary: map[string]jam.ConfigFileBlob{"/etc/x": {Mode: 0o644}}},
		"binary-dotdot": {Binary: map[string]jam.ConfigFileBlob{"../x": {Mode: 0o644}}},
		"binary-mode":   {Binary: map[string]jam.ConfigFileBlob{"kits/k/context/x": {Mode: 0o4755}}},
		"prune-dotdot":  {Prune: []string{"../kits/k/context"}},
		"prune-other":   {Prune: []string{"users"}},
		"prune-deep":    {Prune: []string{"kits/k/sub/context"}},
		"both":          {Binary: map[string]jam.ConfigFileBlob{"marker": {Mode: 0o644}}},
	} {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "users/bob.yml"), "x", 0o644)
		exp.Files = map[string]string{"marker": "m"}
		err := writeConfigTree(dir, exp)
		if err == nil || !strings.HasPrefix(err.Error(), "refusing to") {
			t.Errorf("%s: %v", name, err)
		}
		if _, serr := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(serr) {
			t.Errorf("%s: wrote before refusing", name)
		}
	}
}

// ctxContext packs a context with a binary file, an executable script, a
// nested dir and a .dockerignore.
func ctxContext(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	mustWrite(t, filepath.Join(d, "Dockerfile"), "FROM ${COVE_BASE_IMAGE}\n", 0o644)
	mustWrite(t, filepath.Join(d, ".dockerignore"), "*.log\n", 0o644)
	mustWrite(t, filepath.Join(d, "lib/deep/blob.bin"), "\xff\xfe\x00\x80", 0o644)
	mustWrite(t, filepath.Join(d, "run.sh"), "#!/bin/sh\n", 0o755)
	cfgMust(t, os.Chmod(filepath.Join(d, "run.sh"), 0o755))
	b64, err := studio.PackContextDir(d)
	cfgMust(t, err)
	return b64
}

// config write renders a context kit as kit.yml + context/ files; apply
// --dir replicates it byte for byte with nothing left to apply; a kit that
// drops its context has its context/ files removed by the next write.
func TestConfigWriteContextDirRoundTrip(t *testing.T) {
	srcURL, src := configServer(t)
	b64 := ctxContext(t)
	if _, _, err := jam.PushStudioKit(src, "builder", "kind: studio\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	repo := gitDir(t)
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	tree := readTree(t, repo)
	if ky := tree["kits/builder/kit.yml"]; !strings.Contains(ky, "context-dir: context") || strings.Contains(ky, b64) {
		t.Fatalf("kit.yml:\n%s", ky)
	}
	if tree["kits/builder/context/lib/deep/blob.bin"] != "\xff\xfe\x00\x80" {
		t.Fatalf("tree %v", tree)
	}
	if fi, err := os.Stat(filepath.Join(repo, "kits/builder/context/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh %v %v", fi, err)
	}
	dstURL, dst := configServer(t)
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", dstURL); code != 0 {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	sk, _ := src.GetKit("builder")
	dk, _ := dst.GetKit("builder")
	if dk.Versions[dk.Current] != sk.Versions[sk.Current] {
		t.Fatal("the replica's stored kit differs")
	}
	if code, out, _ := runJam("config", "apply", "--dir", repo, "--admin-url", dstURL); code != 0 || !strings.Contains(out, "nothing to apply") {
		t.Fatalf("second apply: %d\n%s", code, out)
	}
	if _, _, err := jam.PushStudioKit(src, "builder", "kind: studio\nprompt: plain now\n"); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("rewrite: %d %s %s", code, out, errs)
	}
	for p := range readTree(t, repo) {
		if strings.HasPrefix(p, "kits/builder/context/") {
			t.Fatalf("%s left after the kit dropped its context", p)
		}
	}
}

// config write names, on stderr, the kits whose context stayed inline.
func TestConfigWriteNotesInlineContexts(t *testing.T) {
	url, st := configServer(t)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	cfgMust(t, tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg}))
	_, _ = tw.Write([]byte("FROM x\n"))
	cfgMust(t, tw.WriteHeader(&tar.Header{Name: "d/", Mode: 0o755, Typeflag: tar.TypeDir}))
	cfgMust(t, tw.Close())
	cfgMust(t, gw.Close())
	if _, _, err := jam.PushStudioKit(st, "hand", "kind: studio\nbase:\n  context: "+base64.StdEncoding.EncodeToString(buf.Bytes())+"\n"); err != nil {
		t.Fatal(err)
	}
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "kits/hand/context/mine.txt"), "checked in", 0o644)
	code, _, errs := runJam("config", "write", repo, "--all", "--admin-url", url)
	if code != 0 || !strings.Contains(errs, "kit hand: context kept inline in kits/hand/kit.yml") {
		t.Fatalf("%d %s", code, errs)
	}
	if readTree(t, repo)["kits/hand/context/mine.txt"] != "checked in" {
		t.Fatal("an inline kit's context dir was pruned")
	}
}
