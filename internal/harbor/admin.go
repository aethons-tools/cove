package harbor

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// EnrollBody is the POST /admin/enrollments request. Scope comes from the role;
// there are no inline destination/repo/ttl fields.
type EnrollBody struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// EnrollResult is the POST /admin/enrollments response — the token is returned once.
type EnrollResult struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// ActorSummary is a GET /admin/roster item: never a token or hash. Each grant
// carries the effective destinations/repos after overrides.
type ActorSummary struct {
	ID     string         `json:"id"`
	Expiry time.Time      `json:"expiry"`
	Grants []GrantSummary `json:"grants"`
}

// GrantSummary is one grant with its resolved effective scope.
type GrantSummary struct {
	Project      string   `json:"project"`
	Role         string   `json:"role"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
}

// OperatorLoginConfig is the public device-flow client config harbor advertises
// at GET /admin/login-config so `at-harbor login` can self-configure. Every field
// is a public OAuth parameter — never a secret.
type OperatorLoginConfig struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
}

// NewAdminHandler builds the loopback admin API. credExists validates that a
// destination's cred_name resolves before the destination is accepted. login (may
// be nil) is the public device-flow config advertised at /admin/login-config.
func NewAdminHandler(store Store, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /admin/login-config", func(w http.ResponseWriter, r *http.Request) {
		if login == nil {
			http.Error(w, "harbor is not OIDC-gated; no login required", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, login)
	})

	mux.HandleFunc("GET /admin/destinations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.ListDestinations())
	})
	mux.HandleFunc("POST /admin/destinations", func(w http.ResponseWriter, r *http.Request) {
		var d Destination
		if !decode(w, r, &d) {
			return
		}
		if d.Name == "" || d.Route == "" || d.Upstream == "" {
			http.Error(w, "name, route and upstream are required", http.StatusBadRequest)
			return
		}
		if d.CredName != "" && !credExists(d.CredName) {
			http.Error(w, "cred_name does not resolve to a configured credential", http.StatusBadRequest)
			return
		}
		if err := store.AddDestination(d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin destination added", "operator", operatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := store.RemoveDestination(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin destination removed", "operator", operatorID(r), "name", name)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/roster", func(w http.ResponseWriter, r *http.Request) {
		var out []ActorSummary
		for _, a := range store.ListActors() {
			sum := ActorSummary{ID: a.ID, Expiry: a.Expiry}
			for _, g := range a.Grants {
				gs := GrantSummary{Project: g.Project, Role: g.Role}
				if role, ok := store.GetRole(g.Project, g.Role); ok {
					s := EffectiveScope(g, role)
					gs.Destinations, gs.Repos = s.Destinations, s.Repos
				}
				sum.Grants = append(sum.Grants, gs)
			}
			out = append(out, sum)
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/enrollments", func(w http.ResponseWriter, r *http.Request) {
		var b EnrollBody
		if !decode(w, r, &b) {
			return
		}
		if b.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if b.Role == "" {
			http.Error(w, "role is required", http.StatusBadRequest)
			return
		}
		tok, err := Enroll(store, b.ID, b.Project, b.Role, b.Overrides, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin enrolled", "operator", operatorID(r), "id", b.ID, "project", b.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, EnrollResult{ID: b.ID, Token: tok})
	})
	mux.HandleFunc("DELETE /admin/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin revoked", "operator", operatorID(r), "id", id)
		w.WriteHeader(http.StatusNoContent)
	})

	// Auth gate wraps every route.
	return authMiddleware(auth, log, mux)
}

func authMiddleware(auth OperatorAuthenticator, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// login-config is the pre-auth bootstrap (you call it to obtain a token) and
		// carries only public OAuth params — exempt it from operator auth.
		if r.Method == http.MethodGet && r.URL.Path == "/admin/login-config" {
			next.ServeHTTP(w, r)
			return
		}
		op, err := auth.Authenticate(r)
		if err != nil {
			log.Warn("admin request rejected", "reason", err.Error(), "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, withOperator(r, op))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}
