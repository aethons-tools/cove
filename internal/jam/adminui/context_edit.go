package adminui

import (
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextPanel is the shared "Session context" card: the stored layer, the
// size of the core a session receives against its budget, and the YAML editor.
type contextPanel struct {
	Scope, Action, Target string // Target: the hx-target element id (#role, #project, #jam-context)
	ID                    string // the section's own id; set for the Jam panel, which swaps itself
	Core                  string
	CoreBytes, Budget     int
	Over, Empty           bool
	ShowResources         bool
	Leaves                []sessionctx.Leaf
	Resources             []sessionctx.Resource
	YAML                  string
}

func newContextPanel(scope, action, target string, l sessionctx.Layer, rs []sessionctx.Resource, budget int, showResources bool) contextPanel {
	delivered := sessionctx.ProjectLayer(l, rs) // a no-op without resources
	p := contextPanel{
		Scope: scope, Action: action, Target: target, Core: strings.TrimSpace(l.Core),
		CoreBytes: len(strings.TrimSpace(delivered.Core)), Budget: budget,
		Leaves: l.Leaves, Resources: rs, ShowResources: showResources,
		Empty: l.Empty() && len(rs) == 0,
	}
	p.Over = p.CoreBytes > budget
	if !p.Empty {
		if y, err := jam.MarshalContextYAML(jam.ContextBody{Core: l.Core, Leaves: l.Leaves, Resources: rs}); err == nil {
			p.YAML = string(y)
		}
	}
	return p
}

// parseContextForm reads the panel's YAML; the UI can't read host files.
func parseContextForm(r *http.Request) (jam.ContextBody, error) {
	b, err := jam.ParseContextYAML([]byte(r.FormValue("yaml")), nil)
	if err != nil {
		return jam.ContextBody{}, &jam.WriteError{Status: http.StatusBadRequest, Msg: err.Error()}
	}
	return b, nil
}
