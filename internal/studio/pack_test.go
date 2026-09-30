package studio

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCtxDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// PackContextDir then MaterializeInto round-trips to the same tree — the packer
// and the server extractor agree.
func TestPackContextDirRoundTrip(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":  "FROM ${COVE_BASE_IMAGE}\n",
		"app/main.go": "package main\n",
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	out := filepath.Join(t.TempDir(), "base")
	if err := (Base{Context: b64}).MaterializeInto(out); err != nil {
		t.Fatalf("MaterializeInto (extract): %v", err)
	}
	if got := readStr(t, filepath.Join(out, "Dockerfile")); got != "FROM ${COVE_BASE_IMAGE}\n" {
		t.Fatalf("Dockerfile = %q", got)
	}
	if got := readStr(t, filepath.Join(out, "app/main.go")); got != "package main\n" {
		t.Fatalf("nested file = %q", got)
	}
}

func TestPackContextDirRequiresDockerfile(t *testing.T) {
	src := writeCtxDir(t, map[string]string{"readme.txt": "hi"})
	if _, err := PackContextDir(src); err == nil {
		t.Fatal("a context dir without a root Dockerfile must be rejected")
	}
}

func TestPackContextDirRejectsSymlink(t *testing.T) {
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n"})
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	if _, err := PackContextDir(src); err == nil {
		t.Fatal("a symlink in the context dir must be rejected")
	}
}

func TestPackContextDirEntryCap(t *testing.T) {
	defer func(o int) { maxZipEntries = o }(maxZipEntries)
	maxZipEntries = 1
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n", "a": "1"})
	if _, err := PackContextDir(src); err == nil {
		t.Fatal("entry count over the cap must be rejected")
	}
}

// A symlinked context root must be followed (WalkDir alone would not descend,
// yielding an empty zip). Regression for the review's Important finding.
func TestPackContextDirFollowsSymlinkedRoot(t *testing.T) {
	real := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n", "a.txt": "A"})
	link := filepath.Join(t.TempDir(), "ctxlink")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	b64, err := PackContextDir(link)
	if err != nil {
		t.Fatalf("PackContextDir(symlinked root): %v", err)
	}
	out := filepath.Join(t.TempDir(), "base")
	if err := (Base{Context: b64}).MaterializeInto(out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := readStr(t, filepath.Join(out, "a.txt")); got != "A" {
		t.Fatalf("symlinked-root context was not packed: a.txt=%q", got)
	}
}

func TestPackContextDirSizeCap(t *testing.T) {
	defer func(o int) { maxDecompressedZip = o }(maxDecompressedZip)
	maxDecompressedZip = 16
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n", "big": string(make([]byte, 1000))})
	if _, err := PackContextDir(src); err == nil {
		t.Fatal("uncompressed size over the cap must be rejected")
	}
}

func TestResolveContextDirPacks(t *testing.T) {
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n"})
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{ContextDir: src}}
	if err := sk.ResolveContextDir(""); err != nil {
		t.Fatalf("ResolveContextDir: %v", err)
	}
	if sk.Base.ContextDir != "" {
		t.Fatal("context-dir must be cleared after resolve")
	}
	if sk.Base.Context == "" {
		t.Fatal("context (zip) must be set after resolve")
	}
	if _, err := sk.ToJSON(); err != nil {
		t.Fatalf("resolved kit must serialize: %v", err)
	}
}

func TestToJSONRejectsUnresolvedContextDir(t *testing.T) {
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{ContextDir: "/some/dir"}}
	if _, err := sk.ToJSON(); err == nil {
		t.Fatal("ToJSON must reject an unresolved context-dir (server can't read the operator's disk)")
	}
}

func TestValidateContextDirExactlyOne(t *testing.T) {
	sk := StudioKit{Kind: Kind, Name: "web", Base: Base{Image: "r", ContextDir: "/d"}}
	if err := sk.Validate(); err == nil {
		t.Fatal("image + context-dir must be rejected (exactly-one)")
	}
}
