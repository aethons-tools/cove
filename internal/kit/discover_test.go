package kit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverWalksUp(t *testing.T) {
	root := t.TempDir()
	cove := filepath.Join(root, ".cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != cove {
		t.Fatalf("Discover = %q, want %q", got, cove)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	if _, err := Discover(t.TempDir()); err == nil {
		t.Error("expected error when no .cove exists")
	}
}

func TestLoadReadsConfig(t *testing.T) {
	kitDir := t.TempDir()
	yml := "name: x\nbackend: colima\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "x" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
