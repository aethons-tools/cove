package studio

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// KitNote is one leaf a kit ships into its sessions' context (kit/<name>).
// File names a file in the kit tree; ParseKitFS reads it into Body.
type KitNote struct {
	Name     string `yaml:"name" json:"name"`
	ReadWhen string `yaml:"read-when" json:"read-when"`
	Body     string `yaml:"body,omitempty" json:"body"`
	File     string `yaml:"file,omitempty" json:"-"`
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
			return fmt.Errorf("studio kit: note %q: file %q was not read — a kit directory inlines note files; elsewhere use body", n.Name, n.File)
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
