package kittree

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTree creates files (path → content; a path ending in "*" is 0755) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func paths(files []File) string {
	var ps []string
	for _, f := range files {
		ps = append(ps, f.Path+":"+f.Mode.String())
	}
	return strings.Join(ps, " ")
}

func TestReadDirReadsFilesWithModes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "kind: studio\n", "kits/app/bin/run.sh*": "echo", "kits/app/data": "x", "kits/other/kit.yml": "no"})
	files, err := ReadDir(openRoot(t, root), "kits/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "bin/run.sh:-rwxr-xr-x data:-rw-r--r-- kit.yml:-rw-r--r--" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadDirAppliesDockerignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"kit.yml": "kind: studio\n", "Dockerfile": "FROM x\n", ".dockerignore": "*.log\nnode_modules\nkit.yml\nDockerfile\n.dockerignore\n!keep.log\n",
		"a.log": "1", "keep.log": "2", "node_modules/x/y.js": "3", "src/main.go": "4",
	})
	files, err := ReadDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != ".dockerignore:-rw-r--r-- Dockerfile:-rw-r--r-- keep.log:-rw-r--r-- kit.yml:-rw-r--r-- src/main.go:-rw-r--r--" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadDirIgnoredFilesDoNotCount(t *testing.T) {
	withLimits(t, 2, 1<<20, 1<<20)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/.dockerignore": "big/\n", "kits/app/big/1": "1", "kits/app/big/2": "2", "kits/app/big/3": "3"})
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err != nil {
		t.Fatalf("ignored files counted against the limit: %v", err)
	}
	writeTree(t, root, map[string]string{"kits/app/c": "x"})
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: 3 files (limit 2)" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirTreeTooLargeIsLocated(t *testing.T) {
	withLimits(t, 2000, 4, 4)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "kkk", "kits/app/a": "aaa"})
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: more than 4 bytes in total (limit 4 bytes)" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirRefusesSymlink(t *testing.T) {
	for _, target := range []string{"/etc/passwd", "kit.yml"} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{"kits/app/kit.yml": "k"})
		if err := os.Symlink(target, filepath.Join(root, "kits/app/run.sh")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/run.sh: symlinks are not allowed" {
			t.Fatalf("%s: err = %v", target, err)
		}
	}
}

func TestReadDirRefusesSymlinkedKitDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/kit.yml": "k"})
	if err := os.MkdirAll(filepath.Join(root, "kits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real", filepath.Join(root, "kits/app")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: not a directory" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirUnsafeNameNotEchoed(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/bad\x1b[31mname": "x"})
	_, err := ReadDir(openRoot(t, root), "kits/app")
	if err == nil || !strings.HasPrefix(err.Error(), "kits/app: a file has an unsafe path") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("err = %q", err)
	}
}

func TestReadDirFileTooLarge(t *testing.T) {
	withLimits(t, 2000, 1<<20, 4)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kit.yml": "k", "big": "12345"})
	if _, err := ReadDir(openRoot(t, root), "."); err == nil || err.Error() != "big: more than 4 bytes (limit 4 bytes per file)" {
		t.Fatalf("err = %v", err)
	}
}

// tarOf builds a tar from headers; a header's Size is set from body.
func tarOf(t *testing.T, ents ...struct {
	hdr  tar.Header
	body string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := e.hdr
		h.Size = int64(len(e.body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type ent = struct {
	hdr  tar.Header
	body string
}

func TestReadTarAcceptsTarOfADirectory(t *testing.T) {
	b := tarOf(t,
		ent{tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		ent{tar.Header{Name: "./kit.yml", Typeflag: tar.TypeReg, Mode: 0o600}, "kind: studio\n"},
		ent{tar.Header{Name: "./bin/", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		ent{tar.Header{Name: "./bin/run.sh", Typeflag: tar.TypeReg, Mode: 0o700}, "echo"},
	)
	files, err := ReadTar(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "kit.yml:-rw-r--r-- bin/run.sh:-rwxr-xr-x" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadTarRefusals(t *testing.T) {
	reg := func(n string) ent { return ent{tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644}, "x"} }
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"duplicate": {tarOf(t, reg("a"), reg("./a")), "a: appears twice"},
		"symlink":   {tarOf(t, ent{tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""}), "l: symlinks are not allowed"},
		"hardlink":  {tarOf(t, ent{tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "a"}, ""}), "h: hard links are not allowed"},
		"device":    {tarOf(t, ent{tar.Header{Name: "d", Typeflag: tar.TypeChar}, ""}), "d: not a regular file"},
		"escape":    {tarOf(t, reg("../x")), "a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)"},
		"garbage":   {[]byte("not a tar at all, not a tar at all, not a tar at all"), "not a readable tar"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadTar(bytes.NewReader(tc.b)); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadTarKeepsTheBodyLimitError(t *testing.T) {
	b := tarOf(t, ent{tar.Header{Name: "kit.yml", Typeflag: tar.TypeReg}, strings.Repeat("x", 4096)})
	r := http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(b)), 1024)
	_, err := ReadTar(r)
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok || err.Error() != "not a readable tar" {
		t.Fatalf("err = %v (%T)", err, err)
	}
}

func TestWriteTarIsDeterministicAndReadsBack(t *testing.T) {
	files := []File{{Path: "z.sh", Mode: 0o700, Data: []byte("z")}, {Path: KitFile, Mode: 0o644, Data: []byte("k")}}
	var a, b bytes.Buffer
	if err := WriteTar(&a, files); err != nil {
		t.Fatal(err)
	}
	if err := WriteTar(&b, []File{files[1], files[0]}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("WriteTar is not deterministic")
	}
	tr := tar.NewReader(bytes.NewReader(a.Bytes()))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != KitFile || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uid != 0 || hdr.Uname != "" {
		t.Fatalf("first header %+v, %v", hdr, err)
	}
	got, err := ReadTar(bytes.NewReader(a.Bytes()))
	if err != nil || paths(got) != "kit.yml:-rw-r--r-- z.sh:-rwxr-xr-x" {
		t.Fatalf("read back %s, %v", paths(got), err)
	}
}

func TestIgnoreFiltersByTheRootDockerignore(t *testing.T) {
	files := []File{{Path: KitFile, Data: []byte("k")}, {Path: ".dockerignore", Data: []byte("*.tmp\n")}, {Path: "a.tmp"}, {Path: "b/c.tmp"}, {Path: "keep"}}
	got, err := Ignore(files)
	if err != nil || paths(got) != "kit.yml:---------- .dockerignore:---------- b/c.tmp:---------- keep:----------" {
		t.Fatalf("got %s, %v", paths(got), err)
	}
}

func TestReadDirRefusesSymlinkedParentDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/app/kit.yml": "k"})
	if err := os.Symlink("real", filepath.Join(root, "kits")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: not a directory" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirUnsafeDirNotEchoed(t *testing.T) {
	_, err := ReadDir(openRoot(t, t.TempDir()), "kits/bad\x1bname")
	if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.HasPrefix(err.Error(), "a file has an unsafe path") {
		t.Fatalf("err = %q", err)
	}
}

func TestReadTarSkipsPAXGlobalHeader(t *testing.T) {
	b := tarOf(t,
		ent{tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "abc"}}, ""},
		ent{tar.Header{Name: "kit.yml", Typeflag: tar.TypeReg, Mode: 0o644}, "k"},
	)
	files, err := ReadTar(bytes.NewReader(b))
	if err != nil || paths(files) != "kit.yml:-rw-r--r--" {
		t.Fatalf("files = %s, %v", paths(files), err)
	}
}

func TestReadTarTooManyFiles(t *testing.T) {
	withLimits(t, 2, 1<<20, 1<<20)
	reg := func(n string) ent { return ent{tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644}, "x"} }
	if _, err := ReadTar(bytes.NewReader(tarOf(t, reg("a"), reg("b"), reg("c")))); err == nil || err.Error() != "3 files (limit 2)" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadTarFileTooLargeWhileStreaming(t *testing.T) {
	withLimits(t, 2000, 1<<20, 4)
	b := tarOf(t, ent{tar.Header{Name: "big", Typeflag: tar.TypeReg, Mode: 0o644}, "12345"})
	if _, err := ReadTar(bytes.NewReader(b)); err == nil || err.Error() != "big: more than 4 bytes (limit 4 bytes per file)" {
		t.Fatalf("err = %v", err)
	}
}

// onHook sets the walk's test hook for one test, for the hook point at.
func onHook(t *testing.T, at string, f func(p string)) {
	t.Helper()
	testHook = func(a, p string) {
		if a == at {
			f(p)
		}
	}
	t.Cleanup(func() { testHook = nil })
}

// onOpen hooks the window between a file's Lstat and its open.
func onOpen(t *testing.T, f func(p string)) { t.Helper(); onHook(t, "open", f) }

func TestReadDirRefusesFinalSymlinkSwappedIn(t *testing.T) {
	// In the kit dir, the open succeeds and SameFile refuses; out of it, the
	// open (confined to the kit dir) fails.
	for target, want := range map[string]string{"kit.yml": "kits/app/a: not a regular file", "../../secret": "kits/app/a: cannot be read"} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/a": "real", "secret": "decoy"})
		onOpen(t, func(p string) {
			if p == "kits/app/a" {
				full := filepath.Join(root, "kits/app/a")
				if err := os.Remove(full); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(target, full); err != nil {
					t.Error(err)
				}
			}
		})
		files, err := ReadDir(openRoot(t, root), "kits/app")
		if err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, files %s", target, err, paths(files))
		}
	}
}

func TestReadDirIntermediateSymlinkSwapNeverReadsTheDecoy(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/sub/f": "real", "decoy/f": "decoy"})
	onOpen(t, func(p string) {
		if p == "kits/app/sub/f" {
			sub := filepath.Join(root, "kits/app/sub")
			if err := os.Rename(sub, sub+".moved"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink("../../decoy", sub); err != nil {
				t.Error(err)
			}
		}
	})
	// The walk holds sub open, so it reads the real file through that handle.
	files, err := ReadDir(openRoot(t, root), "kits/app")
	for _, f := range files {
		if string(f.Data) == "decoy" {
			t.Fatalf("%s: read the decoy through a swapped-in symlink (err %v)", f.Path, err)
		}
	}
	if err != nil || len(files) != 2 || files[1].Path != "sub/f" || string(files[1].Data) != "real" {
		t.Fatalf("files %s, %v", paths(files), err)
	}
}

func TestReadDirReadsDockerignoreOnce(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/.dockerignore": "*.log\n", "kits/app/a.log": "x"})
	n := 0
	onOpen(t, func(p string) {
		if p == "kits/app/.dockerignore" {
			n++
		}
	})
	files, err := ReadDir(openRoot(t, root), "kits/app")
	if err != nil || paths(files) != ".dockerignore:-rw-r--r-- kit.yml:-rw-r--r--" || n != 1 {
		t.Fatalf("files %s, %v, .dockerignore opened %d times", paths(files), err, n)
	}
}

func TestReadDirIgnoredUnsafeNameIsNotRefused(t *testing.T) {
	for _, rules := range []string{"bad*\n", "bad*\n!keep\n"} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/.dockerignore": rules, "kits/app/bad\x1bname": "x", "kits/app/badd\x1b/x": "y"})
		if _, err := ReadDir(openRoot(t, root), "kits/app"); err != nil {
			t.Fatalf("%q: err = %q", rules, err)
		}
	}
}

func TestReadDirRefusesDirectorySwappedBeforeOpen(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/sub/f": "real", "kits/app/decoy/f": "decoy"})
	onHook(t, "opendir", func(p string) {
		if p == "kits/app/sub" {
			sub := filepath.Join(root, "kits/app/sub")
			if err := os.Rename(sub, filepath.Join(root, "moved")); err != nil {
				t.Error(err)
			}
			if err := os.Symlink("decoy", sub); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/sub: not a directory" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirSkipsAnIgnoredEntryThatCannotBeStatted(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/.dockerignore": "*.log\n", "kits/app/a.log": "x"})
	onHook(t, "listed", func(p string) {
		if p == "kits/app" {
			if err := os.Remove(filepath.Join(root, "kits/app/a.log")); err != nil {
				t.Error(err)
			}
		}
	})
	if files, err := ReadDir(openRoot(t, root), "kits/app"); err != nil || paths(files) != ".dockerignore:-rw-r--r-- kit.yml:-rw-r--r--" {
		t.Fatalf("files %s, %v", paths(files), err)
	}
}

func TestReadDirCapsNestingDepth(t *testing.T) {
	for depth, want := range map[int]string{64: "", 65: "kits/app: directories nested deeper than 64"} {
		root := t.TempDir()
		deep := "kits/app/" + strings.Repeat("d/", depth) + "f"
		writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", deep: "x"})
		_, err := ReadDir(openRoot(t, root), "kits/app")
		if got := fmt.Sprint(err); (want == "" && err != nil) || (want != "" && got != want) {
			t.Fatalf("%d levels: err = %v", depth, err)
		}
	}
}

func TestReadDirCapsEntriesPerDirectory(t *testing.T) {
	withLimits(t, 2, 1<<20, 1<<20)
	root := t.TempDir()
	tree := map[string]string{"kits/app/kit.yml": "k", "kits/app/.dockerignore": "*.log\n"}
	for i := range 6 {
		tree[fmt.Sprintf("kits/app/%d.log", i)] = "x"
	}
	writeTree(t, root, tree)
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err != nil {
		t.Fatalf("8 names (limit 8): err = %v", err)
	}
	writeTree(t, root, map[string]string{"kits/app/6.log": "x"})
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: more than 8 entries" {
		t.Fatalf("err = %v", err)
	}
}

func TestIgnoreTree(t *testing.T) {
	tr, blobs, err := BuildFiles([]File{
		{Path: ".dockerignore", Data: []byte("*.log\nDockerfile\n")},
		{Path: "debug.log", Data: []byte("x")},
		{Path: "run.sh", Data: []byte("y")},
		{Path: "Dockerfile", Data: []byte("FROM x\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := IgnoreTree(tr, MapBlobs(blobs))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range got.Entries {
		paths = append(paths, e.Path)
	}
	if fmt.Sprint(paths) != "[.dockerignore Dockerfile run.sh]" {
		t.Fatalf("kept %v (Dockerfile and .dockerignore are always kept)", paths)
	}
	plain, _, _ := BuildFiles([]File{{Path: "a.log", Data: []byte("x")}})
	if kept, err := IgnoreTree(plain, MapBlobs(nil)); err != nil || len(kept.Entries) != 1 {
		t.Fatalf("no .dockerignore = %+v %v", kept, err)
	}
	if _, err := IgnoreTree(tr, MapBlobs(nil)); err == nil {
		t.Fatal("a .dockerignore whose content is missing must refuse")
	}
}
