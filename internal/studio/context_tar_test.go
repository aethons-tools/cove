package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
)

func fileNode(s string) ContextNode { return ContextNode{File: &s} }

// ContextTar on an inline tree yields a readable tar.gz with the reserved
// `dockerfile` at the root and nested files under their paths.
func TestContextTarFromFilesTree(t *testing.T) {
	b := Base{ContextFiles: ContextTree{
		"dockerfile": fileNode("FROM ${COVE_BASE_IMAGE}\n"),
		"a.txt":      fileNode("A"),
		"sub":        {Dir: ContextTree{"b.txt": fileNode("B")}},
	}}
	raw, err := b.ContextTar()
	if err != nil {
		t.Fatalf("ContextTar: %v", err)
	}
	files := untarGz(t, raw)
	for path, want := range map[string]string{
		"Dockerfile": "FROM ${COVE_BASE_IMAGE}\n", // reserved `dockerfile` → root Dockerfile
		"a.txt":      "A",
		"sub/b.txt":  "B",
	} {
		if files[path].body != want {
			t.Errorf("%s = %q, want %q", path, files[path].body, want)
		}
	}
	if _, ok := files["dockerfile"]; ok {
		t.Error("reserved `dockerfile` must be renamed to Dockerfile, not packed verbatim")
	}
}

// ContextTar on a stored base64 context decodes the bytes verbatim.
func TestContextTarFromStoredContext(t *testing.T) {
	b64 := tarB64(t, map[string]string{"Dockerfile": "FROM x\n"})
	raw, err := (Base{Context: b64}).ContextTar()
	if err != nil {
		t.Fatalf("ContextTar: %v", err)
	}
	if files := untarGz(t, raw); files["Dockerfile"].body != "FROM x\n" {
		t.Fatalf("decoded Dockerfile = %q", files["Dockerfile"].body)
	}
}

// Image/default forms carry no context, so ContextTar errors.
func TestContextTarErrorsForImage(t *testing.T) {
	if _, err := (Base{Image: "r"}).ContextTar(); err == nil {
		t.Fatal("ContextTar must error for an image base")
	}
}

func TestScanContextTarAcceptsGood(t *testing.T) {
	raw, _ := (Base{ContextFiles: ContextTree{
		"dockerfile": fileNode("FROM x\n"),
		"app.go":     fileNode("package main\n"),
	}}).ContextTar()
	if err := ScanContextTar(raw); err != nil {
		t.Fatalf("a good tar must pass: %v", err)
	}
}

func TestScanContextTarRequiresDockerfile(t *testing.T) {
	raw := tarGzBytes(t, []tarGzEntry{{name: "readme.txt", body: "hi"}})
	if err := ScanContextTar(raw); err == nil {
		t.Fatal("a tar without a root Dockerfile must be rejected")
	}
}

func TestScanContextTarRejectsTooManyEntries(t *testing.T) {
	defer func(o int) { maxZipEntries = o }(maxZipEntries)
	maxZipEntries = 2
	raw := tarGzBytes(t, []tarGzEntry{
		{name: "Dockerfile", body: "FROM x\n"}, {name: "a", body: "1"}, {name: "b", body: "2"},
	})
	if err := ScanContextTar(raw); err == nil {
		t.Fatal("entry count over the cap must be rejected")
	}
}

func TestScanContextTarRejectsBomb(t *testing.T) {
	defer func(o int) { maxDecompressedZip = o }(maxDecompressedZip)
	maxDecompressedZip = 16
	raw := tarGzBytes(t, []tarGzEntry{
		{name: "Dockerfile", body: "FROM x\n"}, {name: "big", body: string(make([]byte, 1000))},
	})
	if err := ScanContextTar(raw); err == nil {
		t.Fatal("cumulative uncompressed over the cap must be rejected")
	}
}

// The cap is enforced on the ACTUAL stream, not the header Size: a header that
// lies about a small Size still trips the cap once the bytes are read.
func TestScanContextTarCapsByStreamNotHeader(t *testing.T) {
	defer func(o int) { maxDecompressedZip = o }(maxDecompressedZip)
	maxDecompressedZip = 16
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	body := make([]byte, 1000)
	// Honest Dockerfile, then an oversize file whose header under-reports Size.
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM x\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "big", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gw.Close()
	if err := ScanContextTar(buf.Bytes()); err == nil {
		t.Fatal("stream larger than the cap must be rejected")
	}
}

func TestScanContextTarRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM x\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	_ = tw.Close()
	_ = gw.Close()
	if err := ScanContextTar(buf.Bytes()); err == nil {
		t.Fatal("a symlink entry must be rejected")
	}
}

func TestScanContextTarRejectsHardlink(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM x\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "hard", Linkname: "Dockerfile", Typeflag: tar.TypeLink})
	_ = tw.Close()
	_ = gw.Close()
	if err := ScanContextTar(buf.Bytes()); err == nil {
		t.Fatal("a hard-link entry must be rejected")
	}
}

func TestScanContextTarRejectsDir(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM x\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "adir/", Mode: 0o755, Typeflag: tar.TypeDir})
	_ = tw.Close()
	_ = gw.Close()
	if err := ScanContextTar(buf.Bytes()); err == nil {
		t.Fatal("a non-regular (directory) entry must be rejected (fail-closed)")
	}
}

func TestScanContextTarRejectsAbsolutePath(t *testing.T) {
	raw := tarGzBytes(t, []tarGzEntry{
		{name: "Dockerfile", body: "FROM x\n"}, {name: "/etc/evil", body: "x"},
	})
	if err := ScanContextTar(raw); err == nil {
		t.Fatal("an absolute-path entry must be rejected")
	}
}

func TestScanContextTarRejectsDotDot(t *testing.T) {
	raw := tarGzBytes(t, []tarGzEntry{
		{name: "Dockerfile", body: "FROM x\n"}, {name: "../evil", body: "x"},
	})
	if err := ScanContextTar(raw); err == nil {
		t.Fatal("a ..-escaping entry must be rejected")
	}
}

func TestScanContextTarRejectsNonGzip(t *testing.T) {
	if err := ScanContextTar([]byte("not a gzip")); err == nil {
		t.Fatal("non-gzip input must be rejected")
	}
}

func TestValidateContextCheapEncodedCap(t *testing.T) {
	defer func(o int) { maxEncodedZip = o }(maxEncodedZip)
	maxEncodedZip = 8 // bytes of base64
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{Context: tarB64(t, map[string]string{"Dockerfile": "FROM x\n"})}}
	if err := sk.Validate(); err == nil {
		t.Fatal("an over-cap encoded context must be rejected at validate")
	}
}

func TestValidateContextCheapRejectsNonTar(t *testing.T) {
	// Valid base64 and valid gzip, but the payload is not a tar.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("definitely not a tar stream"))
	_ = gw.Close()
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{Context: base64.StdEncoding.EncodeToString(buf.Bytes())}}
	if err := sk.Validate(); err == nil {
		t.Fatal("a non-tar gzip payload must be rejected at validate")
	}
}

func TestBuildDigestDiffersByBaseForm(t *testing.T) {
	img := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{Image: "r"}})
	files := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{ContextFiles: ContextTree{"dockerfile": fileNode("FROM x")}}})
	ctx := BuildDigest(StudioKit{Kind: Kind, Name: "w", Base: Base{Context: tarB64(t, map[string]string{"Dockerfile": "FROM x\n"})}})
	if img == files || img == ctx || files == ctx {
		t.Fatalf("digests must differ per base form: image=%s files=%s context=%s", img, files, ctx)
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

// A context-files tar is deterministic: ContextTar twice yields identical bytes.
func TestContextTarDeterministic(t *testing.T) {
	b := Base{ContextFiles: ContextTree{
		"dockerfile": fileNode("FROM x\n"),
		"z.txt":      fileNode("Z"),
		"a.txt":      fileNode("A"),
	}}
	one, _ := b.ContextTar()
	two, _ := b.ContextTar()
	if !bytes.Equal(one, two) {
		t.Fatal("ContextTar must be byte-deterministic")
	}
}
