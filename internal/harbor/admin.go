package harbor

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
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
	Addressing   []string `json:"addressing,omitempty"`
}

// RoleBody is the POST /admin/roles request.
type RoleBody struct {
	Project      string   `json:"project"`
	Name         string   `json:"name"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
	Addressing   []string `json:"addressing,omitempty"`
	TTLSeconds   int64    `json:"ttl_seconds"`
	Kit          string   `json:"kit,omitempty"`
}

// RoleSummary is a GET /admin/roles item.
type RoleSummary struct {
	Project      string   `json:"project"`
	Name         string   `json:"name"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
	Addressing   []string `json:"addressing,omitempty"`
	TTLSeconds   int64    `json:"ttl_seconds"`
	Kit          string   `json:"kit,omitempty"`
}

// KitBody is the POST /admin/kits request.
type KitBody struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

// KitResult is the POST /admin/kits response.
type KitResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// KitSummary is a GET /admin/kits item.
type KitSummary struct {
	Name     string `json:"name"`
	Current  int    `json:"current"`
	Versions int    `json:"versions"` // count
}

// KitConfigResult is a GET /admin/kits/{name} item.
type KitConfigResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Config  string `json:"config"`
}

// PinBody is the POST /admin/kits/{name}/pin request.
type PinBody struct {
	Version int `json:"version"`
}

// GrantBody is the POST /admin/actors/{id}/grants request.
type GrantBody struct {
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// CoveRaiseBody is the POST /admin/coves request.
type CoveRaiseBody struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Role    string `json:"role"`
	Unit    string `json:"unit,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// CoveRaiseResult is the POST /admin/coves response — the identity token is
// returned once (the launcher will consume it to connect the cove).
type CoveRaiseResult struct {
	ID           string `json:"id"`
	Token        string `json:"token"`
	LaunchSecret string `json:"launch_secret"`
	Phase        string `json:"phase"`
	Location     string `json:"location,omitempty"`
}

// CoveSummary is a GET /admin/coves item: runtime only, never a token or hash.
type CoveSummary struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Role        string    `json:"role"`
	Unit        string    `json:"unit,omitempty"`
	Phase       string    `json:"phase"`
	Activity    string    `json:"activity,omitempty"`
	LeaseHolder string    `json:"lease_holder"`
	RaisedAt    time.Time `json:"raised_at"`
	LastSeen    time.Time `json:"last_seen"`
}

// CoveSummaries returns the managed-cove runtime registry as scrubbed
// summaries — never a token, hash, or launch secret. The JSON coves handler
// and the read-only UI both render from this, so the two cannot drift.
func CoveSummaries(store Store) []CoveSummary {
	var out []CoveSummary
	for _, i := range store.ListInstances() {
		out = append(out, CoveSummary{
			ID: i.ActorID, Project: i.Project, Role: i.Role, Unit: i.Unit,
			Phase: string(i.Phase), Activity: string(i.Activity),
			LeaseHolder: i.Lease.Holder, RaisedAt: i.RaisedAt, LastSeen: i.LastSeen,
		})
	}
	return out
}

// CoveStatusBody is the POST /admin/coves/{id}/status request.
type CoveStatusBody struct {
	Activity string `json:"activity"`
}

// parseActivity validates a cove-reported activity string.
func parseActivity(s string) (Activity, bool) {
	switch Activity(s) {
	case ActivityRunning, ActivityWaiting, ActivityBlocked, ActivityDone:
		return Activity(s), true
	}
	return "", false
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

// RosterSummaries returns one ActorSummary per enrolled actor, each grant
// carrying its effective destinations/repos after override resolution. It never
// includes a token or hash. The JSON roster handler and the read-only UI both
// render from this, so the two surfaces cannot drift.
func RosterSummaries(store Store) []ActorSummary {
	var out []ActorSummary
	for _, a := range store.ListActors() {
		sum := ActorSummary{ID: a.ID, Expiry: a.Expiry}
		for _, g := range a.Grants {
			gs := GrantSummary{Project: g.Project, Role: g.Role}
			if role, ok := store.GetRole(g.Project, g.Role); ok {
				s := EffectiveScope(g, role)
				gs.Destinations, gs.Repos = s.Destinations, s.Repos
				gs.Addressing = s.Addressing
			}
			sum.Grants = append(sum.Grants, gs)
		}
		out = append(out, sum)
	}
	return out
}

// NewAdminHandler builds the loopback admin API. credExists validates that a
// destination's cred_name resolves before the destination is accepted. login (may
// be nil) is the public device-flow config advertised at /admin/login-config.
func NewAdminHandler(store Store, sup *Supervisor, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger, ui http.Handler) http.Handler {
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
		log.Info("admin destination added", "operator", OperatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := store.RemoveDestination(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin destination removed", "operator", OperatorID(r), "name", name)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/roster", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, RosterSummaries(store))
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
		log.Info("admin enrolled", "operator", OperatorID(r), "id", b.ID, "project", b.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, EnrollResult{ID: b.ID, Token: tok})
	})
	mux.HandleFunc("DELETE /admin/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin revoked", "operator", OperatorID(r), "id", id)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.ListProjects())
	})
	mux.HandleFunc("GET /admin/roles", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		var out []RoleSummary
		for _, ro := range store.ListRoles(project) {
			out = append(out, RoleSummary{
				Project: orDefaultProject(project), Name: ro.Name,
				Destinations: ro.Scope.Destinations, Repos: ro.Scope.Repos,
				Addressing: ro.Scope.Addressing,
				TTLSeconds: int64(ro.Scope.TTL / time.Second),
				Kit:        ro.Kit,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/roles", func(w http.ResponseWriter, r *http.Request) {
		var b RoleBody
		if !decode(w, r, &b) {
			return
		}
		if b.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if b.Kit != "" {
			if _, ok := store.GetKit(b.Kit); !ok {
				http.Error(w, "kit does not exist", http.StatusBadRequest)
				return
			}
		}
		role := Role{Name: b.Name, Scope: Scope{Destinations: b.Destinations, Repos: b.Repos, Addressing: b.Addressing, TTL: time.Duration(b.TTLSeconds) * time.Second}, Kit: b.Kit}
		if err := store.PutRole(b.Project, role); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin role put", "operator", OperatorID(r), "project", orDefaultProject(b.Project), "role", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, name := r.PathValue("project"), r.PathValue("name")
		if err := store.RemoveRole(project, name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin role removed", "operator", OperatorID(r), "project", project, "role", name)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /admin/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		var b GrantBody
		if !decode(w, r, &b) {
			return
		}
		if b.Role == "" {
			http.Error(w, "role is required", http.StatusBadRequest)
			return
		}
		if err := store.AddGrant(r.PathValue("id"), Grant{Project: b.Project, Role: b.Role, Overrides: b.Overrides}); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin grant added", "operator", OperatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(b.Project), "role", b.Role)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin grant removed", "operator", OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/projects/{project}/roster", func(w http.ResponseWriter, r *http.Request) {
		rr, _ := store.GetRoster(r.PathValue("project"))
		writeJSON(w, http.StatusOK, rr)
	})
	mux.HandleFunc("POST /admin/projects/{project}/humans", func(w http.ResponseWriter, r *http.Request) {
		var b Human
		if !decode(w, r, &b) {
			return
		}
		if err := store.AddHuman(r.PathValue("project"), b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin roster human", "operator", operatorID(r), "project", r.PathValue("project"), "name", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /admin/projects/{project}/channels", func(w http.ResponseWriter, r *http.Request) {
		var b Channel
		if !decode(w, r, &b) {
			return
		}
		if err := store.AddChannel(r.PathValue("project"), b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin roster channel", "operator", operatorID(r), "project", r.PathValue("project"), "name", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/humans/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveHuman(r.PathValue("project"), r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin roster human removed", "operator", operatorID(r), "project", r.PathValue("project"), "name", r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/channels/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveChannel(r.PathValue("project"), r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin roster channel removed", "operator", operatorID(r), "project", r.PathValue("project"), "name", r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /admin/kits", func(w http.ResponseWriter, r *http.Request) {
		var b KitBody
		if !decode(w, r, &b) {
			return
		}
		if b.Name == "" || b.Config == "" {
			http.Error(w, "name and config are required", http.StatusBadRequest)
			return
		}
		v, err := store.PushKit(b.Name, b.Config)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin kit pushed", "operator", OperatorID(r), "kit", b.Name, "version", v)
		writeJSON(w, http.StatusCreated, KitResult{Name: b.Name, Version: v})
	})
	mux.HandleFunc("GET /admin/kits", func(w http.ResponseWriter, r *http.Request) {
		var out []KitSummary
		for _, k := range store.ListKits() {
			out = append(out, KitSummary{Name: k.Name, Current: k.Current, Versions: len(k.Versions)})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /admin/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		version := 0
		if q := r.URL.Query().Get("version"); q != "" {
			n, err := strconv.Atoi(q)
			if err != nil {
				http.Error(w, "version must be an integer", http.StatusBadRequest)
				return
			}
			version = n
		}
		cfg, ok := store.KitConfig(name, version)
		if !ok {
			http.Error(w, "no such kit or version", http.StatusNotFound)
			return
		}
		if version == 0 {
			k, _ := store.GetKit(name)
			version = k.Current
		}
		writeJSON(w, http.StatusOK, KitConfigResult{Name: name, Version: version, Config: cfg})
	})
	mux.HandleFunc("GET /admin/kits/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		k, ok := store.GetKit(r.PathValue("name"))
		if !ok {
			http.Error(w, "no such kit", http.StatusNotFound)
			return
		}
		vers := make([]int, 0, len(k.Versions))
		for v := range k.Versions {
			vers = append(vers, v)
		}
		sort.Ints(vers)
		writeJSON(w, http.StatusOK, vers)
	})
	mux.HandleFunc("POST /admin/kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		var b PinBody
		if !decode(w, r, &b) {
			return
		}
		if err := store.PinKit(r.PathValue("name"), b.Version); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin kit pinned", "operator", OperatorID(r), "kit", r.PathValue("name"), "version", b.Version)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if project, role, ok := store.RoleReferencingKit(name); ok {
			http.Error(w, fmt.Sprintf("kit %q is referenced by role %s/%s", name, project, role), http.StatusConflict)
			return
		}
		if err := store.RemoveKit(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin kit removed", "operator", OperatorID(r), "kit", name)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/coves", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, CoveSummaries(store))
	})
	mux.HandleFunc("POST /admin/coves", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		var b CoveRaiseBody
		if !decode(w, r, &b) {
			return
		}
		if b.ID == "" || b.Role == "" {
			http.Error(w, "id and role are required", http.StatusBadRequest)
			return
		}
		inst, tok, secret, err := sup.Raise(r.Context(), RaiseSpec{ActorID: b.ID, Project: b.Project, Role: b.Role, Unit: b.Unit, Prompt: b.Prompt})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin cove raised", "operator", OperatorID(r), "id", b.ID, "project", inst.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, CoveRaiseResult{ID: b.ID, Token: tok, LaunchSecret: secret, Phase: string(inst.Phase), Location: inst.Location})
	})
	mux.HandleFunc("POST /admin/coves/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		var b CoveStatusBody
		if !decode(w, r, &b) {
			return
		}
		act, ok := parseActivity(b.Activity)
		if !ok {
			http.Error(w, "activity must be one of running|waiting|blocked|done", http.StatusBadRequest)
			return
		}
		if err := sup.Report(r.Context(), r.PathValue("id"), act); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin cove status", "operator", OperatorID(r), "id", r.PathValue("id"), "activity", b.Activity)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		if err := sup.Teardown(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin cove torn down", "operator", OperatorID(r), "id", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	guarded := authMiddleware(auth, log, mux) // guards every /admin/* route
	if ui == nil {
		return guarded
	}
	// The UI subtree owns its own gate (loopback-or-session), so it is mounted
	// OUTSIDE the /admin/* authenticator rather than wrapped by it.
	parent := http.NewServeMux()
	parent.Handle("/admin/", guarded)
	parent.Handle("/ui/", ui)
	parent.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	return parent
}

func orDefaultProject(p string) string {
	if p == "" {
		return DefaultProject
	}
	return p
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
		next.ServeHTTP(w, WithOperator(r, op))
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
