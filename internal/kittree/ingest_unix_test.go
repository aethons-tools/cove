//go:build unix

package kittree

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadDirRefusesFIFO(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k"})
	if err := syscall.Mkfifo(filepath.Join(root, "kits/app/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/pipe: not a regular file" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirRefusesHardlink(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "outside": "secret"})
	if err := os.Link(filepath.Join(root, "outside"), filepath.Join(root, "kits/app/data")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/data: hard links are not allowed" {
		t.Fatalf("err = %v", err)
	}
}
