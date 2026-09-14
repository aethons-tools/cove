package adminui

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// sameOrigin reports whether a state-changing request's Origin (or, absent that,
// Referer) host matches the request Host. Fail-closed: neither header → false.
func sameOrigin(r *http.Request) bool {
	check := func(v string) (bool, bool) {
		if v == "" {
			return false, false
		}
		u, err := url.Parse(v)
		return err == nil && u.Host == r.Host, true
	}
	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}
	return false
}

// guardWrite enforces the CSRF Origin check; it writes a 403 and returns false
// when the request must be refused.
func guardWrite(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return false
	}
	return true
}

// splitCSV parses a comma-separated form field into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// overridesFrom builds a *harbor.Override from optional comma-separated fields,
// or nil when both are empty (inherit the role's scope).
func overridesFrom(dests, repos string) *harbor.Override {
	d, rp := splitCSV(dests), splitCSV(repos)
	if len(d) == 0 && len(rp) == 0 {
		return nil
	}
	return &harbor.Override{Destinations: d, Repos: rp}
}

// renderError renders an inline error fragment with the given status.
func renderError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// Escaped by html/template's HTMLEscapeString via a tiny inline render.
	_, _ = w.Write([]byte(`<p class="error">` + template.HTMLEscapeString(msg) + `</p>`))
}

func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger) {
	mux.HandleFunc("POST /ui/enrollments", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			renderError(w, http.StatusBadRequest, "id is required")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		overrides := overridesFrom(r.FormValue("destinations"), r.FormValue("repos"))
		token, err := harbor.Enroll(store, id, project, role, overrides, time.Now())
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui enrolled", "operator", harbor.OperatorID(r), "id", id, "project", project, "role", role)
		renderFragment(w, "roster", "enroll-result", map[string]any{"ID": id, "Token": token})
	})
}
