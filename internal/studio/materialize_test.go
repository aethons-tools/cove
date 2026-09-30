package studio

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func fileNode(s string) ContextNode { return ContextNode{File: &s} }

// zipB64 builds a zip from name→content entries and base64-encodes it.
func zipB64(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestMaterializeContextFilesWritesTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "base")
	b := Base{ContextFiles: ContextTree{
		"dockerfile": fileNode("FROM ${COVE_BASE_IMAGE}\n"),
		"a.txt":      fileNode("A"),
		"sub":        {Dir: ContextTree{"b.txt": fileNode("B")}},
	}}
	if err := b.MaterializeInto(dir); err != nil {
		t.Fatalf("MaterializeInto: %v", err)
	}
	for path, want := range map[string]string{
		"Dockerfile": "FROM ${COVE_BASE_IMAGE}\n", // top-level `dockerfile` → Dockerfile
		"a.txt":      "A",
		"sub/b.txt":  "B",
	} {
		if got := readStr(t, filepath.Join(dir, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestMaterializeZipHappy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "base")
	b := Base{Context: zipB64(t, map[string]string{
		"Dockerfile":  "FROM ${COVE_BASE_IMAGE}\n",
		"app/main.go": "package main\n",
	})}
	if err := b.MaterializeInto(dir); err != nil {
		t.Fatalf("MaterializeInto: %v", err)
	}
	if got := readStr(t, filepath.Join(dir, "app/main.go")); got != "package main\n" {
		t.Fatalf("nested file = %q", got)
	}
}

func TestMaterializeZipRejectsZipSlip(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "base")
	b := Base{Context: zipB64(t, map[string]string{
		"Dockerfile": "FROM x\n",
		"../evil":    "pwned",
	})}
	if err := b.MaterializeInto(dir); err == nil {
		t.Fatal("zip-slip entry must be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
		t.Fatal("zip-slip escaped the build dir")
	}
}

func TestMaterializeZipRejectsAbsolutePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "base")
	b := Base{Context: zipB64(t, map[string]string{
		"Dockerfile": "FROM x\n",
		"/etc/evil":  "pwned",
	})}
	if err := b.MaterializeInto(dir); err == nil {
		t.Fatal("absolute-path entry must be rejected")
	}
}

func TestMaterializeZipRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	df, _ := zw.Create("Dockerfile")
	df.Write([]byte("FROM x\n"))
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(fs.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("/etc/passwd"))
	zw.Close()
	b := Base{Context: base64.StdEncoding.EncodeToString(buf.Bytes())}
	if err := b.MaterializeInto(filepath.Join(t.TempDir(), "base")); err == nil {
		t.Fatal("symlink entry must be rejected")
	}
}

func TestMaterializeZipRejectsTooManyEntries(t *testing.T) {
	defer func(o int) { maxZipEntries = o }(maxZipEntries)
	maxZipEntries = 2
	b := Base{Context: zipB64(t, map[string]string{
		"Dockerfile": "FROM x\n", "a": "1", "b": "2",
	})}
	if err := b.MaterializeInto(filepath.Join(t.TempDir(), "base")); err == nil {
		t.Fatal("entry count over the cap must be rejected")
	}
}

func TestMaterializeZipRejectsDecompressionBomb(t *testing.T) {
	defer func(o int) { maxDecompressedZip = o }(maxDecompressedZip)
	maxDecompressedZip = 16 // bytes
	b := Base{Context: zipB64(t, map[string]string{
		"Dockerfile": "FROM x\n",
		"big":        string(make([]byte, 1000)), // well over the 16-byte cap
	})}
	if err := b.MaterializeInto(filepath.Join(t.TempDir(), "base")); err == nil {
		t.Fatal("decompressed size over the cap must be rejected")
	}
}

func TestMaterializeZipRequiresDockerfile(t *testing.T) {
	b := Base{Context: zipB64(t, map[string]string{"readme.txt": "hi"})}
	if err := b.MaterializeInto(filepath.Join(t.TempDir(), "base")); err == nil {
		t.Fatal("a zip without a root Dockerfile must be rejected")
	}
}

func TestValidateZipCheapEncodedCap(t *testing.T) {
	defer func(o int) { maxEncodedZip = o }(maxEncodedZip)
	maxEncodedZip = 8 // bytes of base64
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{Context: zipB64(t, map[string]string{"Dockerfile": "FROM x\n"})}}
	if err := sk.Validate(); err == nil {
		t.Fatal("an over-cap encoded zip must be rejected at validate")
	}
}

func TestBuildDigestDiffersByBaseForm(t *testing.T) {
	img := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{Image: "r"}})
	files := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{ContextFiles: ContextTree{"dockerfile": fileNode("FROM x")}}})
	zipd := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{Context: zipB64(t, map[string]string{"Dockerfile": "FROM x\n"})}})
	if img == files || img == zipd || files == zipd {
		t.Fatalf("digests must differ per base form: image=%s files=%s zip=%s", img, files, zipd)
	}
}

func TestValidateContextFilesRejectsDockerfileClash(t *testing.T) {
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{ContextFiles: ContextTree{
		"dockerfile": fileNode("FROM x"),
		"Dockerfile": fileNode("FROM y"),
	}}}
	if err := sk.Validate(); err == nil {
		t.Fatal("a top-level Dockerfile key colliding with reserved dockerfile must be rejected")
	}
}

func readStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
