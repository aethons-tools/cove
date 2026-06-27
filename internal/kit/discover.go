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

// Load reads and parses <kitDir>/config.yml.
func Load(kitDir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(kitDir, "config.yml"))
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(data)
}
