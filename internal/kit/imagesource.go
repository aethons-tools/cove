package kit

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidateImageSource enforces the kit-selectable-base rule that a kit names the
// image at-cove hardens exactly one way: either config.yml image.base, or an
// image/Dockerfile at-cove builds — never both. A Dockerfile's presence is
// filesystem state, not config, so this check is kit-dir-aware and lives outside
// the byte-only ParseConfig; Load runs it after parsing.
func ValidateImageSource(cfg Config, kitDir string) error {
	if cfg.Image.Base == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(kitDir, "image", "Dockerfile")); err == nil {
		return fmt.Errorf("config.yml: image.base is set but the kit also has an image/Dockerfile; these are mutually exclusive — remove one")
	}
	return nil
}
