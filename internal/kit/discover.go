package kit

import (
	"fmt"
	"os"
	"path/filepath"
)

// Discover walks up from start to the nearest directory containing a .at-cove/
// child, returning the path to that .at-cove directory.
func Discover(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, ".at-cove")
		if info, err := os.Stat(cand); err == nil && info.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .at-cove/ found in %s or any parent", start)
		}
		dir = parent
	}
}

// Load reads and parses <kitDir>/config.yml, then runs the kit-dir-aware
// validations that ParseConfig (byte-only) cannot — currently the image.base vs
// image/Dockerfile mutual exclusion.
func Load(kitDir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(kitDir, "config.yml"))
	if err != nil {
		return Config{}, err
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, err
	}
	if err := ValidateImageSource(cfg, kitDir); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
