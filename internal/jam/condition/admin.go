package condition

import (
	"encoding/json"
	"net/http"
)

// AdminHandler serves GET /admin/attention?state=open|resolved|all (default
// open) as a JSON array of conditions. Mount it behind the admin gate.
func AdminHandler(t *Tracker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []Condition
		switch r.URL.Query().Get("state") {
		case "", "open":
			out = t.Open()
		case "resolved":
			out = t.Resolved()
		case "all":
			out = append(t.Open(), t.Resolved()...)
		default:
			http.Error(w, "state must be open, resolved or all", http.StatusBadRequest)
			return
		}
		if out == nil {
			out = []Condition{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
