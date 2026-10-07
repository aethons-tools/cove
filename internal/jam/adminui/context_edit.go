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

// jamPanel is the dashboard's Jam-wide session-context card; it swaps itself.
func jamPanel(store jam.Store) contextPanel {
	p := newContextPanel("Jam-wide", "/ui/jam/context", "jam-context", store.GetJamContext(), nil, sessionctx.BudgetJam, false)
	p.ID = "jam-context"
	return p
}

// registerJamContext mounts the dashboard card's writes: save the YAML or
// clear, then re-render the card.
func registerJamContext(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	write := func(what string, body func(r *http.Request) (jam.ContextBody, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !guardWrite(w, r) {
				return
			}
			if err := r.ParseForm(); err != nil {
				renderError(w, http.StatusBadRequest, "invalid form")
				return
			}
			b, err := body(r)
			if err == nil {
				err = jam.SetJamContextChecked(store, b)
			}
			if err != nil {
				renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
				return
			}
			log.Info("ui jam context "+what, "operator", jam.OperatorID(r))
			renderFragment(w, r, "dashboard", "context-panel", jamPanel(store))
		}
	}
	mux.HandleFunc("POST /ui/jam/context", write("set", parseContextForm))
	mux.HandleFunc("DELETE /ui/jam/context", write("cleared", func(*http.Request) (jam.ContextBody, error) { return jam.ContextBody{}, nil }))
}
