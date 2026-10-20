//go:build unix

package studio

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestResolveInRootRefusesFIFONoteWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "n.md"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	sk := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "n.md", File: "n.md"}}}
	err := sk.ResolveInRoot(root, ".")
	if err == nil || err.Error() != `note "n.md" file "n.md": not a regular file` {
		t.Fatalf("want a not-a-regular-file refusal, got %v", err)
	}
}
