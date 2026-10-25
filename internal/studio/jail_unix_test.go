//go:build unix

package studio

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadFileInRootRefusesFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "n.md"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := ReadFileInRoot(root, "n.md"); err == nil || err.Error() != `file "n.md": not a regular file` {
		t.Fatalf("want a not-a-regular-file refusal, got %v", err)
	}
}
