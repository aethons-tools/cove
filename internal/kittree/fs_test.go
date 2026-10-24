package kittree

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
)

func sampleFS(t *testing.T) (Tree, billy.Filesystem, map[string][]byte) {
	t.Helper()
	tr, blobs, err := Build([]File{{Path: KitFile, Data: []byte("kind: studio\n")}, {Path: "bin/run.sh", Mode: 0o755, Data: []byte("echo")}, {Path: "Dockerfile", Data: []byte("FROM x\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return tr, NewFS(tr, MapBlobs(blobs)), blobs
}

func TestFSReads(t *testing.T) {
	_, fsys, _ := sampleFS(t)
	for _, name := range []string{"bin/run.sh", "/bin/run.sh", "./bin/run.sh"} {
		f, err := fsys.Open(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != "echo" {
			t.Fatalf("%s = %q", name, b)
		}
	}
	fi, err := fsys.Stat("bin/run.sh")
	if err != nil || fi.Size() != 4 || fi.Mode() != 0o755 || fi.IsDir() || fi.Name() != "run.sh" {
		t.Fatalf("stat %v %v", fi, err)
	}
	if fi, err := fsys.Lstat("bin"); err != nil || !fi.IsDir() {
		t.Fatalf("lstat bin %v %v", fi, err)
	}
	ents, err := fsys.ReadDir("/")
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if err != nil || strings.Join(names, ",") != "Dockerfile,bin,kit.yml" {
		t.Fatalf("readdir / = %v %v", names, err)
	}
	if _, err := fsys.Stat("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, err := fsys.Open("bin"); err == nil {
		t.Fatal("opening a directory must fail")
	}
}

func TestFSIsReadOnly(t *testing.T) {
	_, fsys, _ := sampleFS(t)
	ro := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, billy.ErrReadOnly) {
			t.Fatalf("%s: err = %v, want billy.ErrReadOnly", what, err)
		}
	}
	_, err := fsys.Create("x")
	ro("create", err)
	_, err = fsys.OpenFile("kit.yml", os.O_WRONLY, 0)
	ro("openfile write", err)
	ro("remove", fsys.Remove("kit.yml"))
	ro("rename", fsys.Rename("kit.yml", "y"))
	ro("mkdirall", fsys.MkdirAll("d", 0o755))
	ro("symlink", fsys.Symlink("kit.yml", "l"))
	_, err = fsys.TempFile("", "x")
	ro("tempfile", err)
	f, _ := fsys.Open("kit.yml")
	_, err = f.Write([]byte("x"))
	ro("file write", err)
	ro("truncate", f.Truncate(0))
}

func TestFSMissingBlob(t *testing.T) {
	tr, _, _ := sampleFS(t)
	fsys := NewFS(tr, func(string) ([]byte, bool) { return nil, false })
	if _, err := fsys.Open("kit.yml"); err == nil || !strings.Contains(err.Error(), "content missing") {
		t.Fatalf("err = %v", err)
	}
}
