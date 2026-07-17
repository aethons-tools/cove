package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigImageBase(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nimage:\n  base: ghcr.io/aethons-tools/cove-image@sha256:abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Base != "ghcr.io/aethons-tools/cove-image@sha256:abc" {
		t.Fatalf("Base = %q", cfg.Image.Base)
	}
}

// ValidateImageSource enforces the design's mutual exclusion: a kit names its
// base either via image.base or via an image/Dockerfile, never both.
func TestValidateImageSource(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		dockerfile bool
		wantErr    bool
	}{
		{"neither", "", false, false},
		{"base only", "img@sha256:abc", false, false},
		{"dockerfile only", "", true, false},
		{"both", "img@sha256:abc", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kitDir := t.TempDir()
			if tt.dockerfile {
				dir := filepath.Join(kitDir, "image")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg := Config{Name: "k", Image: ImageConfig{Base: tt.base}}
			err := ValidateImageSource(cfg, kitDir)
			if tt.wantErr && err == nil {
				t.Fatal("expected a mutual-exclusion error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Load composes ParseConfig with the kit-dir-aware ValidateImageSource, so a
// misconfigured kit is rejected wherever config is loaded.
func TestLoadRejectsBaseWithDockerfile(t *testing.T) {
	kitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"),
		[]byte("name: k\nimage:\n  base: img@sha256:abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	imgDir := filepath.Join(kitDir, "image")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(kitDir)
	if err == nil {
		t.Fatal("Load must reject a kit that sets both image.base and image/Dockerfile")
	}
	if !strings.Contains(err.Error(), "image.base") {
		t.Fatalf("error should mention image.base, got: %v", err)
	}
}
