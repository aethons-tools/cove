package studio

import (
	"encoding/base64"
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

// packed decodes PackContextDir's base64 result into a name→tfile map.
func packed(t *testing.T, b64 string) map[string]tfile {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return untarGz(t, raw)
}

// PackContextDir packs a readable tar.gz with the Dockerfile at the root and
// nested files under their paths.
func TestPackContextDirRoundTrip(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":  "FROM ${COVE_BASE_IMAGE}\n",
		"app/main.go": "package main\n",
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	files := packed(t, b64)
	if files["Dockerfile"].body != "FROM ${COVE_BASE_IMAGE}\n" {
		t.Fatalf("Dockerfile = %q", files["Dockerfile"].body)
	}
	if files["app/main.go"].body != "package main\n" {
		t.Fatalf("nested file = %q", files["app/main.go"].body)
	}
}

// PackContextDir preserves the unix exec bit (the exit-126 regression): a 0755
// script stays 0755 in the packed tar, so docker restores +x on extraction.
func TestPackContextDirPreservesModes(t *testing.T) {
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n"})
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	files := packed(t, b64)
	if got := files["run.sh"].mode; got != 0o755 {
		t.Fatalf("run.sh mode = %o, want 0755 (exec bit must survive)", got)
	}
	if got := files["Dockerfile"].mode; got != 0o644 {
		t.Fatalf("Dockerfile mode = %o, want 0644", got)
	}
}

// PackContextDir is byte-deterministic: identical trees pack to identical base64.
func TestPackContextDirDeterministic(t *testing.T) {
	files := map[string]string{"Dockerfile": "FROM x\n", "z.txt": "Z", "a/b.txt": "B"}
	one, err := PackContextDir(writeCtxDir(t, files))
	if err != nil {
		t.Fatal(err)
	}
	two, err := PackContextDir(writeCtxDir(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("PackContextDir must be byte-deterministic across equal trees")
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
	if got := packed(t, b64)["a.txt"].body; got != "A" {
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

// extracted decodes PackContextDir's result into a name→tfile map; has reports
// whether a path is present in it (the tar/stream analogue of the old on-disk
// extraction these .dockerignore parity cases checked).
func extracted(t *testing.T, b64 string) map[string]tfile { return packed(t, b64) }

func has(files map[string]tfile, rel string) bool { _, ok := files[rel]; return ok }

func TestPackContextDirHonorsDockerignore(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":    "FROM x\n",
		".dockerignore": "*.log\nsecret.txt\n",
		"app.go":        "keep",
		"secret.txt":    "nope",
		"debug.log":     "nope", // root-level → matched by *.log
		"logs/a.log":    "keep", // subdir → NOT matched by root-anchored *.log (docker parity)
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	out := extracted(t, b64)
	if !has(out, "Dockerfile") || !has(out, "app.go") {
		t.Fatal("kept files missing")
	}
	if has(out, "secret.txt") {
		t.Fatal("secret.txt must be excluded by .dockerignore")
	}
	if has(out, "debug.log") {
		t.Fatal("root debug.log must be excluded by *.log")
	}
	if !has(out, "logs/a.log") {
		t.Fatal("logs/a.log must be KEPT — *.log is root-anchored (docker parity)")
	}
}

func TestPackContextDirDockerignoreNegation(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":    "FROM x\n",
		".dockerignore": "*.log\n!keep.log\n",
		"drop.log":      "d",
		"keep.log":      "k",
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	out := extracted(t, b64)
	if has(out, "drop.log") {
		t.Fatal("drop.log must be excluded")
	}
	if !has(out, "keep.log") {
		t.Fatal("!keep.log negation must re-include (moby parity)")
	}
}

func TestPackContextDirDockerignoreKeepsBuildFiles(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":    "FROM x\n",
		".dockerignore": "*\n", // ignore everything…
		"other.txt":     "x",
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	out := extracted(t, b64)
	if !has(out, "Dockerfile") {
		t.Fatal("root Dockerfile must be kept even when .dockerignore matches *")
	}
	if has(out, "other.txt") {
		t.Fatal("other.txt must be excluded by *")
	}
}

// An ignored directory is pruned, so its contents don't count against the caps —
// a tree that would blow the entry cap packs fine once .dockerignore excludes it.
func TestPackContextDirDockerignorePrunesUnderCap(t *testing.T) {
	defer func(o int) { maxZipEntries = o }(maxZipEntries)
	maxZipEntries = 2 // room for Dockerfile + .dockerignore only
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":     "FROM x\n",
		".dockerignore":  "node_modules\n",
		"node_modules/a": "1",
		"node_modules/b": "2",
		"node_modules/c": "3",
	})
	if _, err := PackContextDir(src); err != nil {
		t.Fatalf("ignored dir must be pruned (not counted against caps): %v", err)
	}
}

// `*` + `!sub/file` must re-include the negated file even though its parent dir
// matches `*` — the packer must NOT unconditionally prune an ignored dir when
// exclusions exist (moby/docker-build parity). Regression for the review find.
func TestPackContextDirDockerignoreNegationUnderDir(t *testing.T) {
	src := writeCtxDir(t, map[string]string{
		"Dockerfile":    "FROM x\n",
		".dockerignore": "*\n!src/main.go\n",
		"src/main.go":   "keep",
		"src/other.go":  "drop",
		"top.txt":       "drop",
	})
	b64, err := PackContextDir(src)
	if err != nil {
		t.Fatalf("PackContextDir: %v", err)
	}
	out := extracted(t, b64)
	if !has(out, "src/main.go") {
		t.Fatal("!src/main.go must be re-included even though its parent matches *")
	}
	if has(out, "src/other.go") || has(out, "top.txt") {
		t.Fatal("* must exclude everything not re-included")
	}
	if !has(out, "Dockerfile") {
		t.Fatal("root Dockerfile must be kept")
	}
}

func TestPackContextDirDockerignoreSymlinkHandling(t *testing.T) {
	src := writeCtxDir(t, map[string]string{"Dockerfile": "FROM x\n", ".dockerignore": "badlink\n"})
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "badlink")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	// An IGNORED symlink is skipped, not an error.
	if _, err := PackContextDir(src); err != nil {
		t.Fatalf("an ignored symlink must be skipped, not error: %v", err)
	}
	// A NON-ignored symlink still errors (rejection applies to included entries).
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "livelink")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := PackContextDir(src); err == nil {
		t.Fatal("a non-ignored symlink must still be rejected")
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
