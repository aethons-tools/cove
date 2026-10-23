// Command gen-icons renders the Jam app's PNG icons from the J mark SVG into
// internal/jam/uiassets/static/icons. Run from the repo root (`just icons`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/uiassets/icongen"
)

const (
	svgPath = "internal/jam/uiassets/static/brand/j-logo-plain.svg"
	outDir  = "internal/jam/uiassets/static/icons"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-icons:", err)
		os.Exit(1)
	}
}

func run() error {
	svg, err := os.ReadFile(svgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, s := range icongen.Specs {
		img, err := icongen.Render(svg, s)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		b, err := icongen.Encode(img)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		p := filepath.Join(outDir, s.Name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		fmt.Println(p)
	}
	return nil
}
