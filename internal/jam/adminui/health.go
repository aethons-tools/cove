package adminui

import (
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type healthRow struct {
	condition.Condition
	Age, Lasted string
}

func healthData(t *condition.Tracker, amURL string) map[string]any {
	now := time.Now()
	var open, resolved []healthRow
	for _, c := range t.Open() {
		open = append(open, healthRow{Condition: c, Age: fmtDur(now.Sub(c.Since))})
	}
	for _, c := range t.Resolved() {
		resolved = append(resolved, healthRow{Condition: c, Age: fmtDur(now.Sub(*c.ResolvedAt)), Lasted: fmtDur(c.ResolvedAt.Sub(c.Since))})
	}
	silences := ""
	if amURL != "" {
		silences = amURL + "/#/silences"
	}
	return map[string]any{"Title": "Health", "Open": open, "Resolved": resolved, "Silences": silences}
}

// registerHealth serves the Jam Health tab; an htmx poll gets the body fragment.
func registerHealth(mux *http.ServeMux, t *condition.Tracker, amURL string) {
	mux.HandleFunc("GET /ui/health", func(w http.ResponseWriter, r *http.Request) {
		data := healthData(t, amURL)
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, r, "health", "health-body", data)
			return
		}
		render(w, r, "health", data)
	})
}
