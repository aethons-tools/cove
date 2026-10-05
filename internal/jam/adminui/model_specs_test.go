package adminui_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// specCred recognizes the configured credential "anth" only.
func specCred(name string) bool { return name == "anth" }

func specHandler(store jam.Store, pool bool) http.Handler {
	return adminui.Handler(store, testLogger(), nil, nil, specCred, nil,
		adminui.WithCredentialNames("anth"), adminui.WithPoolConfigured(pool))
}

// seedSpec stores a full claude model-spec named "default".
func seedSpec(t *testing.T, store jam.Store) {
	t.Helper()
	if err := store.PutModelSpec(jam.ModelSpec{
		Name: "default", Type: jam.HarnessClaude, Version: "2.1.0",
		Principal: jam.ModelPrincipal{Credential: "anth"},
		Model:     jam.ModelChoice{ID: "claude-opus-5-5", Effort: "high"},
		Policy:    jam.ModelPolicy{Mode: "dontAsk", Allow: []string{"Bash(go test:*)", "Read"}, Deny: []string{"WebFetch"}},
		Note:      "the house default",
		Claude: &jam.ClaudeSpec{
			Provider:    "vertex",
			ProviderEnv: map[string]string{"CLOUD_ML_REGION": "us-east5", "ANTHROPIC_VERTEX_PROJECT_ID": "proj"},
			Settings:    map[string]any{"theme": "dark"},
			Plugins:     []string{"superpowers@claude-plugins-official"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// specForm is a valid create form; tests override single fields.
func specForm(over url.Values) url.Values {
	f := url.Values{
		"name": {"s1"}, "type": {"claude"}, "version": {"2.1.0"}, "version-constraint": {"2.x"}, "principal": {"anth"},
		"model-id": {"claude-opus-5-5"}, "effort": {"high"}, "mode": {"acceptEdits"},
		"allow": {"Read\r\n\r\nBash(ls:*)\n"}, "deny": {"WebFetch"}, "note": {"n"},
		"provider": {"vertex"}, "provider-env": {"CLOUD_ML_REGION=us-east5\nANTHROPIC_VERTEX_PROJECT_ID = proj"},
		"plugins": {"superpowers@claude-plugins-official\nother@claude-plugins-official"}, "settings": {`{"theme": "dark", "nested": {"a": 1}}`},
	}
	for k, v := range over {
		f[k] = v
	}
	return f
}

func TestModelSpecsListAndNav(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	rec := get(t, specHandler(store, false), "/ui/model-specs")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/ui/model-specs/default"`, "2.1.0", "anth", "claude-opus-5-5", "vertex",
		`aria-current="page">Model-specs`,
		`hx-post="/ui/model-specs"`,                // the create form
		`<option value="anth">anth</option>`,       // credential select, names only
		`<option value="bedrock">bedrock</option>`, // provider select
		`<option value="bypassPermissions">`,       // policy-mode select
		`hx-delete="/ui/model-specs/default"`,      // row delete
	} {
		if !strings.Contains(body, want) {
			t.Errorf("model-specs list missing %q", want)
		}
	}
	if strings.Contains(body, `<option value="pool">`) {
		t.Error("pool must not be offered when no pool is configured")
	}
	if !strings.Contains(get(t, specHandler(store, true), "/ui/model-specs").Body.String(), `<option value="pool">pool</option>`) {
		t.Error("pool should be offered when a pool is configured")
	}
}

func TestModelSpecDetailPrefilled(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	rec := get(t, specHandler(store, false), "/ui/model-specs/default")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>default</h1>", "the house default",
		`hx-post="/ui/model-specs/default"`,
		`name="version" value="2.1.0"`,
		`name="model-id" value="claude-opus-5-5"`,
		`<option value="anth" selected>`,
		`<option value="dontAsk" selected>`,
		`<option value="">unset = bypassPermissions (legacy)</option>`,
		`<option value="vertex" selected>`,
		"Bash(go test:*)\nRead</textarea>",
		"ANTHROPIC_VERTEX_PROJECT_ID=proj\nCLOUD_ML_REGION=us-east5</textarea>",
		"superpowers@claude-plugins-official</textarea>",
		"&#34;theme&#34;: &#34;dark&#34;",
		`aria-current="page">Model-specs`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

func TestModelSpecDetailNotFound(t *testing.T) {
	rec := get(t, specHandler(newStore(t), false), "/ui/model-specs/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<nav") {
		t.Fatalf("missing model-spec = %d", rec.Code)
	}
}

// A stale principal (no longer configured) stays selected on the edit form
// rather than silently switching to another credential.
func TestModelSpecDetailKeepsUnknownPrincipal(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	m, _ := store.GetModelSpec("default")
	m.Principal.Credential = "retired"
	if err := store.PutModelSpec(m); err != nil {
		t.Fatal(err)
	}
	body := get(t, specHandler(store, false), "/ui/model-specs/default").Body.String()
	if !strings.Contains(body, `<option value="retired" selected>`) {
		t.Error("the stored principal should remain selected")
	}
}

func TestCreateModelSpecAllFields(t *testing.T) {
	store := newStore(t)
	h := specHandler(store, false)
	rec := post(t, h, "/ui/model-specs", specForm(nil))
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/model-specs/s1" {
		t.Fatalf("create = %d redirect=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	m, ok := store.GetModelSpec("s1")
	if !ok {
		t.Fatal("model-spec not stored")
	}
	if m.Type != jam.HarnessClaude || m.Version != "2.1.0" || m.VersionConstraint != "2.x" || m.Principal.Credential != "anth" ||
		m.Model.ID != "claude-opus-5-5" || m.Model.Effort != "high" || m.Policy.Mode != "acceptEdits" ||
		len(m.Policy.Allow) != 2 || m.Policy.Allow[1] != "Bash(ls:*)" || len(m.Policy.Deny) != 1 || m.Note != "n" {
		t.Fatalf("stored envelope = %+v", m)
	}
	c := m.Claude
	if c == nil || c.Provider != "vertex" || c.ProviderEnv["ANTHROPIC_VERTEX_PROJECT_ID"] != "proj" || len(c.ProviderEnv) != 2 ||
		len(c.Plugins) != 2 || c.Settings["theme"] != "dark" || c.Settings["nested"] == nil {
		t.Fatalf("stored claude body = %+v", c)
	}
	if rec := post(t, h, "/ui/model-specs", specForm(nil)); rec.Code != http.StatusConflict {
		t.Errorf("re-create = %d, want 409", rec.Code)
	}
}

func TestCreateModelSpecMinimal(t *testing.T) {
	store := newStore(t)
	rec := post(t, specHandler(store, false), "/ui/model-specs", url.Values{
		"name": {"min"}, "type": {"claude"}, "version": {"2.1.0"}, "principal": {"anth"}, "provider": {"anthropic"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create minimal = %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := store.GetModelSpec("min")
	if m.Claude == nil || m.Claude.ProviderEnv != nil || m.Claude.Settings != nil || m.Claude.Plugins != nil || m.Policy.Allow != nil {
		t.Fatalf("empty fields should stay unset: %+v %+v", m, m.Claude)
	}
}

// Every refusal is a 400 rendered as the inline error, and nothing is stored.
// Rules come from jam.ValidateModelSpec; the form only parses.
func TestCreateModelSpecValidation(t *testing.T) {
	cases := map[string]struct {
		over url.Values
		want string
	}{
		"no name":           {url.Values{"name": {""}}, "name is required"},
		"no version":        {url.Values{"version": {" "}}, "version is required"},
		"unknown cred":      {url.Values{"principal": {"ghost"}}, "ghost"},
		"pool unconfigured": {url.Values{"principal": {"pool"}}, "no subscription pool"},
		"bad mode":          {url.Values{"mode": {"yolo"}}, "policy.mode"},
		"bad provider":      {url.Values{"provider": {"azure"}}, "claude.provider"},
		"cred env":          {url.Values{"provider-env": {"ANTHROPIC_API_KEY=sk-secret-value"}}, "carries a credential"},
		"env no equals":     {url.Values{"provider-env": {"JUSTAKEY"}}, "KEY=VALUE"},
		"env duplicate":     {url.Values{"provider-env": {"A=1\nA=2"}}, "set twice"},
		"settings array":    {url.Values{"settings": {`[1,2]`}}, "JSON object"},
		"settings garbage":  {url.Values{"settings": {`{nope`}}, "JSON object"},
		"settings env":      {url.Values{"settings": {`{"env": {"X": "1"}}`}}, "not a preference"},
		"dup plugin":        {url.Values{"plugins": {"a@claude-plugins-official\na@claude-plugins-official"}}, "twice"},
		"bad type":          {url.Values{"type": {"codex"}}, "harness family"},
		"range version":     {url.Values{"version": {"2.x"}}, "version-constraint"},
		"bad constraint":    {url.Values{"version-constraint": {"3.x"}}, "does not admit"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			rec := post(t, specHandler(store, false), "/ui/model-specs", specForm(tc.over))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), `class="error"`) {
				t.Fatalf("= %d %q, want 400 containing %q", rec.Code, rec.Body.String(), tc.want)
			}
			if strings.Contains(rec.Body.String(), "sk-secret-value") {
				t.Error("a refusal must never echo an env value")
			}
			if len(store.ListModelSpecs()) != 0 {
				t.Error("a refused model-spec must not be stored")
			}
		})
	}
}

func TestCreateModelSpecPoolWhenConfigured(t *testing.T) {
	store := newStore(t)
	if rec := post(t, specHandler(store, true), "/ui/model-specs", specForm(url.Values{"principal": {"pool"}})); rec.Code != http.StatusOK {
		t.Fatalf("pool principal with a pool = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEditModelSpecReplacesEveryField(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	h := specHandler(store, false)
	rec := post(t, h, "/ui/model-specs/default", url.Values{
		"type": {"claude"}, "version": {"3.0.0"}, "principal": {"anth"}, "provider": {"anthropic"},
		"name": {"renamed"}, // ignored: the name is the key
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="spec"`) {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := store.GetModelSpec("default")
	if m.Version != "3.0.0" || m.Model.ID != "" || m.Policy.Mode != "" || m.Policy.Allow != nil || m.Note != "" ||
		m.Claude.Provider != "anthropic" || m.Claude.ProviderEnv != nil || m.Claude.Settings != nil || m.Claude.Plugins != nil {
		t.Fatalf("after edit = %+v %+v", m, m.Claude)
	}
	if _, ok := store.GetModelSpec("renamed"); ok {
		t.Error("edit must not rename")
	}
	if rec := post(t, h, "/ui/model-specs/default", specForm(url.Values{"mode": {"yolo"}})); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid edit = %d, want 400", rec.Code)
	}
	if m2, _ := store.GetModelSpec("default"); m2.Version != "3.0.0" {
		t.Error("a refused edit must not change the stored spec")
	}
	if rec := post(t, h, "/ui/model-specs/ghost", specForm(nil)); rec.Code != http.StatusNotFound {
		t.Errorf("edit missing = %d, want 404", rec.Code)
	}
}

func TestDeleteModelSpec(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	h := specHandler(store, false)
	req := httptest.NewRequest(http.MethodDelete, "/ui/model-specs/default", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="model-specs"`) {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.ListModelSpecs()) != 0 {
		t.Error("model-spec should be gone")
	}
	req = httptest.NewRequest(http.MethodDelete, "/ui/model-specs/default", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", rec.Code)
	}
}

// TestModelSpecWritesCSRF: cross-origin create, edit and delete are refused
// and change nothing.
func TestModelSpecWritesCSRF(t *testing.T) {
	store := newStore(t)
	seedSpec(t, store)
	h := specHandler(store, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/ui/model-specs"},
		{http.MethodPost, "/ui/model-specs/default"},
		{http.MethodDelete, "/ui/model-specs/default"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(specForm(url.Values{"version": {"9.x"}}).Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin %s %s = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	if m, ok := store.GetModelSpec("default"); !ok || m.Version != "2.1.0" || len(store.ListModelSpecs()) != 1 {
		t.Error("cross-origin writes must change nothing")
	}
}

// Principal header rules use one line per rule: `NAME += ITEM`
// (ensure-list-item) or `NAME = VALUE` (set); the edit form shows them back in
// that syntax and the detail page lists them.
func TestModelSpecPrincipalHeaders(t *testing.T) {
	store := newStore(t)
	h := specHandler(store, false)
	rec := post(t, h, "/ui/model-specs", specForm(url.Values{
		"headers": {"anthropic-beta += oauth-2025-04-20\r\n\r\nX-Foo = bar=baz\nX-Bar+=1\n"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := store.GetModelSpec("s1")
	want := []jam.ModelHeaderRule{
		{Name: "anthropic-beta", EnsureListItem: "oauth-2025-04-20"},
		{Name: "X-Foo", Set: "bar=baz"},
		{Name: "X-Bar", EnsureListItem: "1"},
	}
	if len(m.Principal.Headers) != 3 || m.Principal.Headers[0] != want[0] || m.Principal.Headers[1] != want[1] || m.Principal.Headers[2] != want[2] {
		t.Fatalf("stored headers = %+v, want %+v", m.Principal.Headers, want)
	}
	body := html.UnescapeString(get(t, h, "/ui/model-specs/s1").Body.String())
	for _, w := range []string{
		"anthropic-beta += oauth-2025-04-20\nX-Foo = bar=baz\nX-Bar += 1</textarea>", // the edit form
		"anthropic-beta += oauth-2025-04-20</span>",                                  // the detail list
	} {
		if !strings.Contains(body, w) {
			t.Errorf("detail missing %q", w)
		}
	}
}

func TestModelSpecPrincipalHeadersRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"no operator":   {"X-Foo bar", "header rule line 1"},
		"empty name":    {" = bar", "header rule line 1"},
		"denied header": {"Cookie = sk-secret-value", "Cookie"},
		"no value":      {"X-Foo =", "exactly one of set or ensure-list-item"},
		"padded value":  {"X-Foo = bar ", "whitespace"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			rec := post(t, specHandler(store, false), "/ui/model-specs", specForm(url.Values{"headers": {tc.in}}))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("= %d %q, want 400 containing %q", rec.Code, rec.Body.String(), tc.want)
			}
			if strings.Contains(rec.Body.String(), "sk-secret-value") {
				t.Error("a refusal must never echo a header value")
			}
		})
	}
}
