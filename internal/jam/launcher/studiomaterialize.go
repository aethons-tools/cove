package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/studio"
)

// materializeDockerfileBase writes a studio kit's authored Dockerfile-context base
// into dir: the Dockerfile at dir/Dockerfile and each Base.Context entry at its
// relative path under dir. The context travels by value in the kit definition
// (remote-build-tolerant), so this is the only host input. It is fail-closed on an
// unsafe path — absolute, or escaping dir via ".." — so an authored context can
// never write outside the build dir.
func materializeDockerfileBase(dir string, base studio.Base) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("materialize dockerfile base: %w", err)
	}
	for p, content := range base.Context {
		rel, err := safeContextPath(p)
		if err != nil {
			return fmt.Errorf("materialize dockerfile base: context %q: %w", p, err)
		}
		if rel == "Dockerfile" {
			return fmt.Errorf("materialize dockerfile base: context path %q collides with the kit's Dockerfile", p)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("materialize dockerfile base: %w", err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("materialize dockerfile base: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(base.Dockerfile), 0o644); err != nil {
		return fmt.Errorf("materialize dockerfile base: %w", err)
	}
	return nil
}

// safeContextPath validates that p is a relative path staying within its root once
// cleaned, returning the cleaned form. Absolute paths and any escaping via ".."
// are rejected, so a malicious or mistaken context key cannot write outside dir.
func safeContextPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("must be a relative path")
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("escapes the build context")
	}
	return clean, nil
}
