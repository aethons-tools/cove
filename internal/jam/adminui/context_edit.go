package adminui

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextPanel is the shared "Session context" card: the stored layer, the
// size of the core a session receives against its budget, and the YAML editor.
type contextPanel struct {
	Scope             string
	Core              string
	CoreBytes, Budget int
	Over, Empty       bool
	ShowResources     bool
	Leaves            []sessionctx.Leaf
	Resources         []sessionctx.Resource
	YAML              string
}

func newContextPanel(scope string, l sessionctx.Layer, rs []sessionctx.Resource, budget int, showResources bool) contextPanel {
	delivered := sessionctx.ProjectLayer(l, rs) // a no-op without resources
	p := contextPanel{
		Scope: scope, Core: strings.TrimSpace(l.Core),
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

// jamPanel is the dashboard's Jam-wide session-context card; it swaps itself.
func jamPanel(store jam.Store) contextPanel {
	return newContextPanel("Jam-wide", store.GetJamContext(), nil, sessionctx.BudgetJam, false)
}

// setJamContext stores the Jam-wide context layer (b empty: clears it) and
// logs what (set, cleared) with the operator — never the YAML.
func setJamContext(store jam.Store, log *slog.Logger, r *http.Request, what string, b jam.ContextBody) error {
	if err := jam.SetJamContextChecked(store, b); err != nil {
		return err
	}
	log.Info("ui jam context "+what, "operator", jam.OperatorID(r))
	return nil
}
