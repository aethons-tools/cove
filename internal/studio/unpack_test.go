package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// richCtxDir is a context dir with a binary (non-UTF-8) file, an executable
// script, a nested dir and a .dockerignore that drops one file.
func richCtxDir(t *testing.T) string {
	t.Helper()
	dir := writeCtxDir(t, map[string]string{
		"Dockerfile":        "FROM ${COVE_BASE_IMAGE}\nCOPY . /opt\n",
		".dockerignore":     "*.log\n",
		"lib/deep/conf.txt": "nested\n",
		"build.log":         "ignored\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "lib/blob.bin"), []byte{0xff, 0xfe, 0x00, 0x80, 0xc3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	return dir
}

// ContextDirFiles unpacks a packed context into the files (and modes) of a
// context-dir; writing them back out and packing again reproduces the stored
// context byte for byte.
func TestContextDirFilesRoundTrip(t *testing.T) {
	b64, err := PackContextDir(richCtxDir(t))
	if err != nil {
		t.Fatal(err)
	}
	files, err := ContextDirFiles(b64)
	if err != nil {
		t.Fatalf("ContextDirFiles: %v", err)
	}
	got := map[string]ContextFile{}
	for _, f := range files {
		got[f.Path] = f
	}
	if len(got) != 5 {
		t.Fatalf("files %v", got)
	}
	if _, ok := got["build.log"]; ok {
		t.Fatal("an ignored file was never packed, so it cannot come back")
	}
	if f := got["run.sh"]; f.Mode != 0o755 {
		t.Fatalf("run.sh mode %o", f.Mode)
	}
	if f := got["lib/blob.bin"]; !bytes.Equal(f.Data, []byte{0xff, 0xfe, 0x00, 0x80, 0xc3}) || f.Mode != 0o644 {
		t.Fatalf("blob %+v", f)
	}
	out := t.TempDir()
	for _, f := range files {
		p := filepath.Join(out, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, f.Mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.Mode); err != nil {
			t.Fatal(err)
		}
	}
	again, err := PackContextDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if again != b64 {
		t.Fatal("the unpacked files do not pack back to the stored context")
	}
}

// A context no context-dir can reproduce (a hand-made tar: directory
// entries, a mode git cannot hold, an ignored file, unsorted names, a
// "./" name) is ErrContextNotReproducible; an unsafe one is refused by
// the scan.
func TestContextDirFilesRefuses(t *testing.T) {
	b64 := func(entries []tarGzEntry) string { return base64.StdEncoding.EncodeToString(tarGzBytes(t, entries)) }
	canonical, err := PackContextDir(writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n", "a": "a\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ContextDirFiles(canonical); err != nil {
		t.Fatalf("canonical: %v", err)
	}
	notRepro := map[string]string{
		"unsorted":     b64([]tarGzEntry{{name: "a", body: "a\n"}, {name: "Dockerfile", body: "FROM x\n"}}),
		"group-write":  b64([]tarGzEntry{{name: "Dockerfile", body: "FROM x\n", mode: 0o664}}),
		"ignored-file": b64([]tarGzEntry{{name: ".dockerignore", body: "x\n"}, {name: "Dockerfile", body: "FROM x\n"}, {name: "x", body: "x"}}),
		"dot-slash":    b64([]tarGzEntry{{name: "./Dockerfile", body: "FROM x\n"}}),
		"dir-entry":    dirEntryContext(t),
	}
	for name, ctx := range notRepro {
		if _, err := ContextDirFiles(ctx); !errors.Is(err, ErrContextNotReproducible) {
			t.Errorf("%s: %v, want ErrContextNotReproducible", name, err)
		}
	}
	unsafe := map[string]string{
		"dotdot":  b64([]tarGzEntry{{name: "Dockerfile", body: "FROM x\n"}, {name: "../x", body: "x"}}),
		"no-root": b64([]tarGzEntry{{name: "a", body: "a"}}),
		"base64":  "not base64!",
	}
	for name, ctx := range unsafe {
		if _, err := ContextDirFiles(ctx); err == nil || errors.Is(err, ErrContextNotReproducible) {
			t.Errorf("%s: %v, want a scan refusal", name, err)
		}
	}
}

func dirEntryContext(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, h := range []*tar.Header{{Name: "Dockerfile", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}, {Name: "d/", Mode: 0o755, Typeflag: tar.TypeDir}} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte("x\n"))
		}
	}
	_ = tw.Close()
	_ = gw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
