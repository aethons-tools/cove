// Command gen-icons renders the Jam app's PNG icons from the J mark SVG into
// internal/jam/uiassets/static/icons, and the VS Code extension's into
// web/vscode/media. Run from the repo root (`just icons`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/uiassets/icongen"
)

const (
	svgPath   = "internal/jam/uiassets/static/brand/j-logo-plain.svg"
	outDir    = "internal/jam/uiassets/static/icons"
	vscodeDir = "web/vscode/media"
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
	if err := write(svg, icongen.Specs, outDir); err != nil {
		return err
	}
	return write(svg, icongen.VSCodeSpecs, vscodeDir)
}

func write(svg []byte, specs []icongen.Spec, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, s := range specs {
		img, err := icongen.Render(svg, s)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		b, err := icongen.Encode(img)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		p := filepath.Join(dir, s.Name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		fmt.Println(p)
	}
	return nil
}
