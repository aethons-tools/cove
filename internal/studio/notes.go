package studio

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// KitNote is one leaf a kit ships into its sessions' context (kit/<name>).
// File is a CLIENT-ONLY authoring convenience resolved into Body at push.
type KitNote struct {
	Name     string `yaml:"name" json:"name"`
	ReadWhen string `yaml:"read-when" json:"read_when"`
	Body     string `yaml:"body,omitempty" json:"body"`
	File     string `yaml:"file,omitempty" json:"-"`
}

// ResolveNoteFiles reads each note's file (relative to dir, never escaping it)
// into its body. A note may set body or file, not both.
func (sk *StudioKit) ResolveNoteFiles(dir string) error {
	for i, n := range sk.Notes {
		if n.File == "" {
			continue
		}
		if n.Body != "" {
			return fmt.Errorf("note %q: set body or file, not both", n.Name)
		}
		if !filepath.IsLocal(n.File) {
			return fmt.Errorf("note %q: file %q must be a relative path inside %s", n.Name, n.File, dir)
		}
		raw, err := os.ReadFile(filepath.Join(dir, n.File))
		if err != nil {
			return fmt.Errorf("note %q: %w", n.Name, err)
		}
		sk.Notes[i].Body, sk.Notes[i].File = string(raw), ""
	}
	return nil
}

// Leaves returns the notes as session-context leaves.
func (sk StudioKit) Leaves() []sessionctx.Leaf {
	out := make([]sessionctx.Leaf, len(sk.Notes))
	for i, n := range sk.Notes {
		out[i] = sessionctx.Leaf{Name: n.Name, ReadWhen: n.ReadWhen, Body: n.Body}
	}
	return out
}

// CheckNotes applies the authored-leaf rules (an authoring check, on push
// only, like CheckPrompt); tools.md is reserved for the generated tool list.
func (sk StudioKit) CheckNotes() error {
	for _, n := range sk.Notes {
		if n.File != "" {
			return fmt.Errorf("studio kit: note %q: file %q was not read — `at-jam kit push` inlines note files; elsewhere use body", n.Name, n.File)
		}
		if n.Name == sessionctx.ToolsLeaf {
			return fmt.Errorf("studio kit: note name %s is reserved for the generated tool list", sessionctx.ToolsLeaf)
		}
	}
	if err := sessionctx.ValidateLayer(sessionctx.Layer{Leaves: sk.Leaves()}, 0); err != nil {
		return fmt.Errorf("studio kit: notes: %w", err)
	}
	return nil
}
