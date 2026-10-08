package adminui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithPoolConfigured says whether the serve-config enables a subscription
// pool, so the model-spec principal select offers jam.PoolPrincipal (and
// jam.ValidateModelSpec accepts it). Off by default.
func WithPoolConfigured(on bool) Option {
	return func(o *options) { o.poolConfigured = on }
}

// specForm is a model-spec's list/map fields in the edit form's input syntax.
type specForm struct {
	Allow, Deny, ProviderEnv, Plugins, Settings, Headers string
}

// specChoices are the select options of the model-spec form. Principals are
// credential NAMES (never values), plus jam.PoolPrincipal when a pool is on.
type specChoices struct {
	Types      []jam.HarnessType
	Principals []string
	Modes      []string
	Providers  []string
}

// specDetail is the model-spec page (and form) payload.
type specDetail struct {
	Title       string
	Spec        jam.ModelSpec
	Uses        []kitUse // roles that run this model-spec (Implicit: unbound, so the default)
	Provider    string   // Spec.Claude.Provider, "" when no body
	Form        specForm
	Choices     specChoices
	NotFound    bool
	NotFoundFor string
}

// specUI is what the model-spec pages need besides the store.
type specUI struct {
	store      jam.Store
	credExists func(string) bool
	credNames  []string
	pool       bool
}

// choices builds the select options; current (the stored principal) is kept
// even if no longer configured, so an edit never silently switches it.
func (u specUI) choices(current string) specChoices {
	p := slices.Clone(u.credNames)
	if u.pool {
		p = append(p, jam.PoolPrincipal)
	}
	if current != "" && !slices.Contains(p, current) {
		p = append(p, current)
	}
	return specChoices{
		Types:      []jam.HarnessType{jam.HarnessClaude},
		Principals: p,
		Modes:      jam.ClaudePermissionModes(),
		Providers:  jam.ClaudeProviders(),
	}
}

func (u specUI) detail(m jam.ModelSpec) specDetail {
	d := specDetail{Title: "Model-specs", Spec: m, Choices: u.choices(m.Principal.Credential), Uses: specUses(u.store, m.Name)}
	d.Form.Allow = strings.Join(m.Policy.Allow, "\n")
	d.Form.Deny = strings.Join(m.Policy.Deny, "\n")
	d.Form.Headers = formatHeaderRules(m.Principal.Headers)
	if c := m.Claude; c != nil {
		d.Provider = c.Provider
		d.Form.Plugins = strings.Join(c.Plugins, "\n")
		var lines []string
		for _, k := range slices.Sorted(maps.Keys(c.ProviderEnv)) {
			lines = append(lines, k+"="+c.ProviderEnv[k])
		}
		d.Form.ProviderEnv = strings.Join(lines, "\n")
		if len(c.Settings) > 0 {
			if b, err := json.MarshalIndent(c.Settings, "", "  "); err == nil {
				d.Form.Settings = string(b)
			}
		}
	}
	return d
}

// specUses lists the roles that run model-spec name: those bound to it and,
// for the default spec, those bound to none.
func specUses(store jam.Store, name string) []kitUse {
	var out []kitUse
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			if r.ModelSpecName() == name {
				out = append(out, kitUse{Project: p, Role: r.Name, Implicit: r.ModelSpec == ""})
			}
		}
	}
	return out
}

func (u specUI) tableData() map[string]any {
	used := map[string]int{}
	for _, m := range u.store.ListModelSpecs() {
		used[m.Name] = len(specUses(u.store, m.Name))
	}
	return map[string]any{
		"Specs":  u.store.ListModelSpecs(),
		"UsedBy": used,
		// New seeds the create form: claude on the anthropic provider.
		"New": u.detail(jam.ModelSpec{Type: jam.HarnessClaude, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}),
	}
}

// specURL is the detail page path for a model-spec.
func specURL(name string) string { return "/ui/model-specs/" + url.PathEscape(name) }

// formLines reads a one-per-line textarea, trimming and skipping blank lines;
// nil when empty.
func formLines(s string) []string {
	var out []string
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseProviderEnv reads KEY=VALUE lines. Errors name the line number or key,
// never a value (provider-env is non-secret by rule, but a pasted secret must
// not be echoed back).
func parseProviderEnv(s string) (map[string]string, error) {
	var env map[string]string
	n := 0
	for line := range strings.Lines(s) {
		n++
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, badRequest("provider-env line " + strconv.Itoa(n) + " is not KEY=VALUE")
		}
		if _, dup := env[k]; dup {
			return nil, badRequest("provider-env key " + k + " is set twice")
		}
		if env == nil {
			env = map[string]string{}
		}
		env[k] = strings.TrimSpace(v)
	}
	return env, nil
}

// formatHeaderRules renders principal header rules one per line:
// `NAME += ITEM` (ensure-list-item) or `NAME = VALUE` (set).
func formatHeaderRules(rules []jam.ModelHeaderRule) string {
	lines := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.EnsureListItem != "" {
			lines = append(lines, r.Name+" += "+r.EnsureListItem)
		} else {
			lines = append(lines, r.Name+" = "+r.Set)
		}
	}
	return strings.Join(lines, "\n")
}

// parseHeaderRules reads formatHeaderRules' syntax, splitting at the first
// "=" (header names never contain one); a "+" right before it makes the rule
// ensure-list-item (validation refuses names ending in "+", so this is
// unambiguous). Whitespace around the operator is syntax; anything after the
// value up to the line end is kept, so validation refuses a padded value
// rather than the form silently trimming it. Errors name the line number,
// never a value.
func parseHeaderRules(s string) ([]jam.ModelHeaderRule, error) {
	var rules []jam.ModelHeaderRule
	n := 0
	for line := range strings.Lines(s) {
		n++
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, v, ok := strings.Cut(line, "=")
		name, ensure := strings.CutSuffix(strings.TrimSpace(name), "+")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, badRequest("header rule line " + strconv.Itoa(n) + " is not NAME = VALUE or NAME += ITEM")
		}
		v = strings.TrimLeft(v, " \t")
		r := jam.ModelHeaderRule{Name: name}
		if ensure {
			r.EnsureListItem = v
		} else {
			r.Set = v
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// parseSettings reads the settings textarea: empty, or a JSON object.
func parseSettings(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, badRequest("claude.settings is not a JSON object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, badRequest("claude.settings is not a JSON object")
	}
	if len(obj) == 0 {
		return nil, nil
	}
	return obj, nil
}

// specFromForm reads every model-spec field but the name from the form. It
// only parses; jam.ValidateModelSpec owns the rules.
func specFromForm(r *http.Request, name string) (jam.ModelSpec, error) {
	env, err := parseProviderEnv(r.FormValue("provider-env"))
	if err != nil {
		return jam.ModelSpec{}, err
	}
	settings, err := parseSettings(r.FormValue("settings"))
	if err != nil {
		return jam.ModelSpec{}, err
	}
	headers, err := parseHeaderRules(r.FormValue("headers"))
	if err != nil {
		return jam.ModelSpec{}, err
	}
	m := jam.ModelSpec{
		Name:              name,
		Type:              jam.HarnessType(strings.TrimSpace(r.FormValue("type"))),
		Version:           strings.TrimSpace(r.FormValue("version")),
		VersionConstraint: strings.TrimSpace(r.FormValue("version-constraint")),
		Principal:         jam.ModelPrincipal{Credential: strings.TrimSpace(r.FormValue("principal")), Headers: headers},
		Model: jam.ModelChoice{
			ID:     strings.TrimSpace(r.FormValue("model-id")),
			Effort: strings.TrimSpace(r.FormValue("effort")),
		},
		Policy: jam.ModelPolicy{
			Mode:  strings.TrimSpace(r.FormValue("mode")),
			Allow: formLines(r.FormValue("allow")),
			Deny:  formLines(r.FormValue("deny")),
		},
		Note: strings.TrimSpace(r.FormValue("note")),
	}
	if m.Type == jam.HarnessClaude {
		m.Claude = &jam.ClaudeSpec{
			Provider:    strings.TrimSpace(r.FormValue("provider")),
			ProviderEnv: env,
			Settings:    settings,
			Plugins:     formLines(r.FormValue("plugins")),
		}
	}
	return m, nil
}

// registerModelSpecs mounts the model-spec pages. Writes go through
// jam.CreateModelSpec / jam.UpdateModelSpec (the same validation as the
// at-jam model-spec verb); audit logs carry operator, name and type only.
func registerModelSpecs(mux *http.ServeMux, u specUI, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/model-specs", func(w http.ResponseWriter, r *http.Request) {
		data := u.tableData()
		data["Title"] = "Model-specs"
		render(w, r, "model-specs", data)
	})

	mux.HandleFunc("GET /ui/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := u.store.GetModelSpec(r.PathValue("name"))
		if !ok {
			renderStatus(w, r, http.StatusNotFound, "model-spec", specDetail{Title: "Model-specs", NotFound: true, NotFoundFor: r.PathValue("name")})
			return
		}
		render(w, r, "model-spec", u.detail(m))
	})

	// Create only: an existing model-spec is edited on its page.
	mux.HandleFunc("POST /ui/model-specs", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		m, err := specFromForm(r, strings.TrimSpace(r.FormValue("name")))
		if err == nil {
			err = jam.CreateModelSpec(u.store, m, u.credExists, u.pool)
		}
		if err != nil {
			msg := err.Error()
			if jam.WriteStatus(err, 0) == http.StatusConflict {
				msg += "; edit it on its page"
			}
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), msg)
			return
		}
		log.Info("ui model-spec created", "operator", jam.OperatorID(r), "name", m.Name, "type", string(m.Type))
		w.Header().Set("HX-Redirect", specURL(m.Name))
		renderFragment(w, r, "model-specs", "model-specs-table", u.tableData())
	})

	mux.HandleFunc("POST /ui/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		m, err := specFromForm(r, r.PathValue("name"))
		if err == nil {
			err = jam.UpdateModelSpec(u.store, m, u.credExists, u.pool)
		}
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
			return
		}
		log.Info("ui model-spec updated", "operator", jam.OperatorID(r), "name", m.Name, "type", string(m.Type))
		stored, ok := u.store.GetModelSpec(m.Name)
		if !ok {
			renderError(w, http.StatusNotFound, "model-spec no longer exists")
			return
		}
		renderFragment(w, r, "model-spec", "spec-body", u.detail(stored))
	})

	mux.HandleFunc("DELETE /ui/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := u.store.RemoveModelSpec(r.PathValue("name")); err != nil {
			renderError(w, jam.ModelSpecRemoveStatus(err), err.Error())
			return
		}
		log.Info("ui model-spec deleted", "operator", jam.OperatorID(r), "name", r.PathValue("name"))
		renderFragment(w, r, "model-specs", "model-specs-table", u.tableData())
	})
}
