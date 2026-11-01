package adminui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// specCred recognizes the configured credential "anth" only.
func specCred(name string) bool { return name == "anth" }

func specHandler(store jam.Store, pool bool) http.Handler {
	return adminui.Handler(store, testLogger(), nil, nil, specCred, nil, specOpts(pool)...)
}

func specOpts(pool bool) []adminui.Option {
	return []adminui.Option{adminui.WithCredentialNames("anth"), adminui.WithPoolConfigured(pool)}
}

// specEdit is a branch editor with specHandler's handlers.
func specEdit(t *testing.T, pool bool) *branchEdit {
	t.Helper()
	return editOn(t, editSetup{cred: specCred, opts: specOpts(pool), pool: pool})
}

// fullSpec is a valid create body with every field set; tests override
// single fields.
func fullSpec(over map[string]string) map[string]string {
	b := map[string]string{
		"name": "s1", "type": "claude", "version": "2.1.0", "versionConstraint": "2.x", "principal": "anth",
		"modelId": "claude-opus-5-5", "effort": "high", "mode": "acceptEdits",
		"allow": "Read\r\n\r\nBash(ls:*)\n", "deny": "WebFetch", "note": " n ",
		"provider": "vertex", "providerEnv": "CLOUD_ML_REGION=us-east5\n\nANTHROPIC_VERTEX_PROJECT_ID = proj",
		"plugins": "superpowers@claude-plugins-official\nother@claude-plugins-official", "settings": `{"theme": "dark", "nested": {"a": 1}}`,
		"headers": "anthropic-beta += oauth-2025-04-20\r\n\r\nX-Foo = bar=baz\nX-Bar+=1\n",
	}
	for k, v := range over {
		b[k] = v
	}
	return b
}

// A create stores every field the body carries; the detail shows them back
// in the edit fields' syntax.
func TestModelSpecsAPIFields(t *testing.T) {
	e := specEdit(t, false)
	h := e.H
	if rec := apiDo(t, h, "POST", "/app/admin/api/model-specs", fullSpec(nil), nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	m, ok := e.Store().GetModelSpec("s1")
	if !ok {
		t.Fatal("not stored")
	}
	if m.Type != jam.HarnessClaude || m.Version != "2.1.0" || m.VersionConstraint != "2.x" || m.Principal.Credential != "anth" ||
		m.Model.ID != "claude-opus-5-5" || m.Model.Effort != "high" || m.Policy.Mode != "acceptEdits" ||
		strings.Join(m.Policy.Allow, ",") != "Read,Bash(ls:*)" || strings.Join(m.Policy.Deny, ",") != "WebFetch" || m.Note != "n" {
		t.Fatalf("stored envelope = %+v", m)
	}
	c := m.Claude
	if c == nil || c.Provider != "vertex" || c.ProviderEnv["ANTHROPIC_VERTEX_PROJECT_ID"] != "proj" || c.ProviderEnv["CLOUD_ML_REGION"] != "us-east5" || len(c.ProviderEnv) != 2 ||
		len(c.Plugins) != 2 || c.Settings["theme"] != "dark" || c.Settings["nested"] == nil {
		t.Fatalf("stored claude body = %+v", c)
	}
	want := []jam.ModelHeaderRule{
		{Name: "anthropic-beta", EnsureListItem: "oauth-2025-04-20"},
		{Name: "X-Foo", Set: "bar=baz"},
		{Name: "X-Bar", EnsureListItem: "1"},
	}
	if len(m.Principal.Headers) != 3 || m.Principal.Headers[0] != want[0] || m.Principal.Headers[1] != want[1] || m.Principal.Headers[2] != want[2] {
		t.Fatalf("stored headers = %+v", m.Principal.Headers)
	}

	var d adminui.SpecDetail
	apiDo(t, h, "GET", "/app/admin/api/model-specs/s1", nil, &d)
	f := d.Fields
	if f.Headers != "anthropic-beta += oauth-2025-04-20\nX-Foo = bar=baz\nX-Bar += 1" || f.Allow != "Read\nBash(ls:*)" || f.Deny != "WebFetch" ||
		f.ProviderEnv != "ANTHROPIC_VERTEX_PROJECT_ID=proj\nCLOUD_ML_REGION=us-east5" || f.Plugins != "superpowers@claude-plugins-official\nother@claude-plugins-official" ||
		!strings.Contains(f.Settings, `"theme": "dark"`) || f.Provider != "vertex" || f.ModelID != "claude-opus-5-5" || f.Mode != "acceptEdits" || f.Note != "n" {
		t.Errorf("fields = %+v", f)
	}
	if len(d.HeaderRules) != 3 || d.HeaderRules[1] != (adminui.HeaderRuleRow{Name: "X-Foo", Op: "=", Value: "bar=baz"}) ||
		strings.Join(d.ProviderEnvKeys, ",") != "ANTHROPIC_VERTEX_PROJECT_ID,CLOUD_ML_REGION" || strings.Join(d.SettingsKeys, ",") != "nested,theme" || len(d.Plugins) != 2 {
		t.Errorf("detail = %+v", d)
	}
	if strings.Join(d.Choices.Principals, ",") != "anth" || strings.Join(d.Choices.Types, ",") != "claude" || len(d.Choices.Modes) == 0 || len(d.Choices.Providers) == 0 {
		t.Errorf("choices = %+v", d.Choices)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/model-specs", fullSpec(nil), nil), 409, "conflict")
}

// Empty list and map fields stay unset.
func TestModelSpecsAPIMinimal(t *testing.T) {
	e := specEdit(t, false)
	if rec := apiDo(t, e.H, "POST", "/app/admin/api/model-specs",
		map[string]string{"name": "min", "type": "claude", "version": "2.1.0", "principal": "anth", "provider": "anthropic", "settings": " {} "}, nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	m, _ := e.Store().GetModelSpec("min")
	if m.Claude == nil || m.Claude.ProviderEnv != nil || m.Claude.Settings != nil || m.Claude.Plugins != nil || m.Policy.Allow != nil || m.Principal.Headers != nil {
		t.Fatalf("empty fields should stay unset: %+v %+v", m, m.Claude)
	}
}

// Every refusal is a 400 naming the problem — never an env or header value —
// and nothing is stored.
func TestModelSpecsAPIRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		over map[string]string
		want string
	}{
		"no name":           {map[string]string{"name": ""}, "name is required"},
		"no version":        {map[string]string{"version": " "}, "version is required"},
		"unknown cred":      {map[string]string{"principal": "ghost"}, "ghost"},
		"pool unconfigured": {map[string]string{"principal": "pool"}, "no subscription pool"},
		"bad mode":          {map[string]string{"mode": "yolo"}, "policy.mode"},
		"bad provider":      {map[string]string{"provider": "azure"}, "claude.provider"},
		"cred env":          {map[string]string{"providerEnv": "ANTHROPIC_API_KEY=sk-secret-value"}, "carries a credential"},
		"env no equals":     {map[string]string{"providerEnv": "A=1\nJUSTAKEY"}, "provider-env line 2 is not KEY=VALUE"},
		"env bad key":       {map[string]string{"providerEnv": "sk-secret-value==x"}, "provider-env line 1 is not KEY=VALUE"},
		"env duplicate":     {map[string]string{"providerEnv": "A=1\nA=sk-secret-value"}, "provider-env key A is set twice"},
		"settings array":    {map[string]string{"settings": `[1,2]`}, "claude.settings is not a JSON object"},
		"settings garbage":  {map[string]string{"settings": `{nope`}, "claude.settings is not a JSON object"},
		"settings trailing": {map[string]string{"settings": `{} {}`}, "claude.settings is not a JSON object"},
		"settings env":      {map[string]string{"settings": `{"env": {"X": "1"}}`}, "not a preference"},
		"dup plugin":        {map[string]string{"plugins": "a@claude-plugins-official\na@claude-plugins-official"}, "twice"},
		"bad type":          {map[string]string{"type": "codex"}, "harness family"},
		"range version":     {map[string]string{"version": "2.x"}, "version-constraint"},
		"bad constraint":    {map[string]string{"versionConstraint": "3.x"}, "does not admit"},
		"header no op":      {map[string]string{"headers": "X-Ok = 1\nX-Foo sk-secret-value"}, "header rule line 2 is not NAME = VALUE or NAME += ITEM"},
		"header no name":    {map[string]string{"headers": " += sk-secret-value"}, "header rule line 1"},
		"denied header":     {map[string]string{"headers": "Cookie = sk-secret-value"}, "Cookie"},
		"header no value":   {map[string]string{"headers": "X-Foo ="}, "exactly one of set or ensure-list-item"},
		"padded value":      {map[string]string{"headers": "X-Foo = bar "}, "whitespace"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			rec := apiDo(t, adminui.RegisterAppForTest(store, testLogger(), nil, nil, specCred, nil, specOpts(false)...), "POST", "/app/admin/api/model-specs", fullSpec(tc.over), nil)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("= %d %s, want 400 containing %q", rec.Code, rec.Body, tc.want)
			}
			if strings.Contains(rec.Body.String(), "sk-secret-value") {
				t.Error("a refusal echoed a value")
			}
			if len(store.ListModelSpecs()) != 0 {
				t.Error("a refused model-spec was stored")
			}
		})
	}
}

// WithPoolConfigured offers the pool principal and lets a spec name it.
func TestModelSpecsAPIPool(t *testing.T) {
	e := specEdit(t, true)
	h := e.H
	if rec := apiDo(t, h, "POST", "/app/admin/api/model-specs", fullSpec(map[string]string{"principal": jam.PoolPrincipal}), nil); rec.Code != 201 {
		t.Fatalf("pool principal with a pool = %d %s", rec.Code, rec.Body)
	}
	var d adminui.SpecDetail
	apiDo(t, h, "GET", "/app/admin/api/model-specs/s1", nil, &d)
	if strings.Join(d.Choices.Principals, ",") != "anth,"+jam.PoolPrincipal || d.Fields.Principal != jam.PoolPrincipal {
		t.Errorf("choices = %v principal %q", d.Choices.Principals, d.Fields.Principal)
	}
	var list adminui.SpecsResponse
	apiDo(t, h, "GET", "/app/admin/api/model-specs", nil, &list)
	if strings.Join(list.Choices.Principals, ",") != "anth,"+jam.PoolPrincipal {
		t.Errorf("new-spec choices = %v", list.Choices.Principals)
	}
	var off adminui.SpecsResponse
	apiDo(t, specHandler(e.Store(), false), "GET", "/app/admin/api/model-specs", nil, &off)
	if strings.Join(off.Choices.Principals, ",") != "anth" {
		t.Errorf("no pool: choices = %v", off.Choices.Principals)
	}
}

// An edit replaces every field (the name is the key, never renamed); a
// refused edit changes nothing; a missing spec is a 404.
func TestModelSpecsAPIEdit(t *testing.T) {
	e := specEdit(t, false)
	h := e.H
	apiDo(t, h, "POST", "/app/admin/api/model-specs", fullSpec(map[string]string{"name": "default"}), nil)
	var d adminui.SpecDetail
	if rec := apiDo(t, h, "POST", "/app/admin/api/model-specs/default",
		map[string]string{"type": "claude", "version": "3.0.0", "principal": "anth", "provider": "anthropic", "name": "renamed"}, &d); rec.Code != 200 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	m, _ := e.Store().GetModelSpec("default")
	if m.Version != "3.0.0" || m.Model.ID != "" || m.Policy.Mode != "" || m.Policy.Allow != nil || m.Note != "" || m.Principal.Headers != nil ||
		m.Claude.Provider != "anthropic" || m.Claude.ProviderEnv != nil || m.Claude.Settings != nil || m.Claude.Plugins != nil {
		t.Fatalf("after edit = %+v %+v", m, m.Claude)
	}
	if _, ok := e.Store().GetModelSpec("renamed"); ok || d.Name != "default" {
		t.Error("an edit must not rename")
	}
	for _, over := range []map[string]string{{"mode": "yolo"}, {"providerEnv": "nope"}, {"settings": "[]"}, {"headers": "nope"}} {
		wantErr(t, apiDo(t, h, "POST", "/app/admin/api/model-specs/default", fullSpec(over), nil), 400, "bad_request")
	}
	if m2, _ := e.Store().GetModelSpec("default"); m2.Version != "3.0.0" {
		t.Error("a refused edit changed the stored spec")
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/model-specs/ghost", fullSpec(nil), nil), 404, "not_found")
}
