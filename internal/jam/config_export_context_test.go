package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/studio"
)

var ctxBlob = []byte{0xff, 0xfe, 0x00, 0x80, 0xc3, '\n'}

// ctxKitContext packs a context with a binary (non-UTF-8) file, an
// executable script, a nested dir and a .dockerignore.
func ctxKitContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM ${COVE_BASE_IMAGE}\nCOPY . /opt\n", 0o644)
	mustWrite(t, filepath.Join(dir, ".dockerignore"), "*.log\n", 0o644)
	mustWrite(t, filepath.Join(dir, "lib/deep/conf.txt"), "nested\n", 0o644)
	mustWrite(t, filepath.Join(dir, "lib/blob.bin"), string(ctxBlob), 0o644)
	mustWrite(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho hi\n", 0o755)
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	b64, err := studio.PackContextDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b64
}

func ctxKitStore(t *testing.T) (*MemStore, string) {
	t.Helper()
	st := NewMemStore()
	b64 := ctxKitContext(t)
	if _, _, err := PushStudioKit(st, "builder", "kind: studio\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushStudioKit(st, "plain", "kind: studio\nprompt: hi\n"); err != nil {
		t.Fatal(err)
	}
	return st, b64
}

// writeExport writes exp under dir as config write does: Files at 0644,
// Binary at their modes (past the umask).
func writeExport(t *testing.T, dir string, exp ConfigTreeExport) {
	t.Helper()
	for p, body := range exp.Files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(p)), body, 0o644)
	}
	for p, b := range exp.Binary {
		full := filepath.Join(dir, filepath.FromSlash(p))
		mustWrite(t, full, string(b.Data), os.FileMode(b.Mode))
		if err := os.Chmod(full, os.FileMode(b.Mode)); err != nil {
			t.Fatal(err)
		}
	}
}

func currentKitConfig(t *testing.T, st *MemStore, name string) string {
	t.Helper()
	k, ok := st.GetKit(name)
	if !ok {
		t.Fatalf("no kit %s", name)
	}
	return k.Versions[k.Current]
}

// An inline kit with a packed context exports as kits/<n>/kit.yml naming
// base.context-dir: context, plus the context's files (modes kept) under
// kits/<n>/context/; other kits are unchanged.
func TestExportConfigTreeRendersContextAsFiles(t *testing.T) {
	st, _ := ctxKitStore(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	ky := exp.Files["kits/builder/kit.yml"]
	if !strings.Contains(ky, "context-dir: context\n") || strings.Contains(ky, "  context: ") {
		t.Fatalf("kit.yml:\n%s", ky)
	}
	want := map[string]uint32{
		"kits/builder/context/Dockerfile": 0o644, "kits/builder/context/.dockerignore": 0o644,
		"kits/builder/context/lib/deep/conf.txt": 0o644, "kits/builder/context/lib/blob.bin": 0o644,
		"kits/builder/context/run.sh": 0o755,
	}
	if len(exp.Binary) != len(want) {
		t.Fatalf("binary %v", slices.Sorted(maps.Keys(exp.Binary)))
	}
	for p, mode := range want {
		if b, ok := exp.Binary[p]; !ok || b.Mode != mode {
			t.Errorf("%s: %+v", p, b)
		}
		if _, dup := exp.Files[p]; dup {
			t.Errorf("%s is in Files too", p)
		}
	}
	if !bytes.Equal(exp.Binary["kits/builder/context/lib/blob.bin"].Data, ctxBlob) {
		t.Fatal("binary content lost")
	}
	if exp.Files["kits/plain/kit.yml"] != "kind: studio\nprompt: hi\n" {
		t.Fatalf("plain kit.yml:\n%s", exp.Files["kits/plain/kit.yml"])
	}
	if !slices.Contains(exp.Prune, "kits/builder/context") || !slices.Contains(exp.Prune, "kits/plain/context") {
		t.Fatalf("prune %v", exp.Prune)
	}
}

// A context no context-dir can reproduce byte for byte stays inline.
func TestExportConfigTreeKeepsIrreproducibleContextInline(t *testing.T) {
	st := NewMemStore()
	// Directory entries: PackContextDir never writes them.
	raw := tarGzWithDir(t)
	b64 := base64.StdEncoding.EncodeToString(raw)
	if _, _, err := PushStudioKit(st, "hand", "kind: studio\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if ky := exp.Files["kits/hand/kit.yml"]; !strings.Contains(ky, "context: "+b64) || len(exp.Binary) != 0 {
		t.Fatalf("kit.yml:\n%s\nbinary %d", ky, len(exp.Binary))
	}
	if !slices.Contains(exp.Prune, "kits/hand/context") {
		t.Fatalf("prune %v", exp.Prune)
	}
}

// The round trip: the written tree, through the --dir upload, applies to a
// fresh Jam with the byte-identical stored kit, and re-plans unchanged — on
// the replica and on the source itself.
func TestConfigRoundTripContextDir(t *testing.T) {
	src, _ := ctxKitStore(t)
	exp, err := ExportConfigTree(src.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	written := t.TempDir()
	writeExport(t, written, exp)
	tarball, err := configtar.Pack(written)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := configtar.Unpack(bytes.NewReader(tarball), dir); err != nil {
		t.Fatal(err)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	want := currentKitConfig(t, src, "builder")
	for _, st := range []*MemStore{NewMemStore(), src} {
		if res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local"); err != nil || !res.Applied {
			t.Fatalf("apply: %+v %v", res, err)
		}
		if got := currentKitConfig(t, st, "builder"); got != want {
			t.Fatalf("stored kit differs:\n%s\n---\n%s", got, want)
		}
		again, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
		if err != nil || again.Plan.Counts()[ConfigUnchanged] != len(again.Plan.Ops) {
			t.Fatalf("re-plan is not unchanged: %v %+v", err, again.Plan.Ops)
		}
		if k, _ := st.GetKit("builder"); k.Current != 1 {
			t.Fatalf("a new kit version: %d", k.Current)
		}
	}
}

// A managed kit that is gone from Jam, or now tracks its own source, has its
// context dir pruned too.
func TestExportConfigTreePrunesGoneKitContext(t *testing.T) {
	st, _ := ctxKitStore(t)
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "builder"}, {Kind: ConfigKindKit, Name: "gone"}}
	exp, err := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(exp.Prune, []string{"kits/builder/context", "kits/gone/context"}) {
		t.Fatalf("prune %v", exp.Prune)
	}
}

// tarGzWithDir is a hand-made context: a Dockerfile and a directory entry.
func tarGzWithDir(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	must(t, tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg}))
	if _, err := tw.Write([]byte("FROM x\n")); err != nil {
		t.Fatal(err)
	}
	must(t, tw.WriteHeader(&tar.Header{Name: "d/", Mode: 0o755, Typeflag: tar.TypeDir}))
	must(t, tw.Close())
	must(t, gw.Close())
	return buf.Bytes()
}
