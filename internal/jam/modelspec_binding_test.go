package jam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// putSpec stores a valid claude spec named name with model id model.
func putSpec(t *testing.T, st Store, name, model string) {
	t.Helper()
	m := validSpec()
	m.Name, m.Model.ID = name, model
	if err := st.PutModelSpec(m); err != nil {
		t.Fatal(err)
	}
}

func TestRoleModelSpecAdminRoundTrip(t *testing.T) {
	h, st, _ := newModelSpecAdmin(t, false)
	putSpec(t, st, "opus", "claude-opus-5-5")

	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "w", ModelSpec: "opus"}); rec.Code != http.StatusCreated {
		t.Fatalf("put role = %d %s", rec.Code, rec.Body)
	}
	rec := doReq(t, h, "GET", "/admin/roles", nil)
	var roles []RoleSummary
	if json.Unmarshal(rec.Body.Bytes(), &roles) != nil || len(roles) != 1 || roles[0].ModelSpec != "opus" {
		t.Fatalf("list = %s", rec.Body)
	}
	if r, _ := st.GetRole("", "w"); r.ModelSpec != "opus" || r.ModelSpecName() != "opus" {
		t.Fatalf("stored role = %+v", r)
	}

	// Unknown name rejected; the stored role is unchanged.
	rec = doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "w", ModelSpec: "ghost"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `model-spec "ghost" does not exist`) {
		t.Fatalf("unknown spec = %d %s", rec.Code, rec.Body)
	}
	if r, _ := st.GetRole("", "w"); r.ModelSpec != "opus" {
		t.Fatalf("refused put changed the role: %+v", r)
	}

	// Empty is accepted (even before claude-default exists) and resolves to it.
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "plain"}); rec.Code != http.StatusCreated {
		t.Fatalf("unbound role = %d %s", rec.Code, rec.Body)
	}
	if r, _ := st.GetRole("", "plain"); r.ModelSpec != "" || r.ModelSpecName() != DefaultModelSpec {
		t.Fatalf("unbound role = %+v resolves to %q", r, r.ModelSpecName())
	}
}

func TestRoleWritersCheckModelSpec(t *testing.T) {
	st := NewMemStore()
	if err := CreateRole(st, "", Role{Name: "r", ModelSpec: "ghost"}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("CreateRole unknown spec: %v", err)
	}
	if err := CreateRole(st, "", Role{Name: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRole(st, "", "r", func(r *Role) error { r.ModelSpec = "ghost"; return nil }); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("UpdateRole unknown spec: %v", err)
	}
	putSpec(t, st, "opus", "")
	if err := UpdateRole(st, "", "r", func(r *Role) error { r.ModelSpec = "opus"; return nil }); err != nil {
		t.Fatal(err)
	}
	// A re-put through PutRoleKeeping replaces the binding (it is role-body owned).
	if err := PutRoleKeeping(st, "", Role{Name: "r"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.GetRole("", "r"); r.ModelSpec != "" {
		t.Fatalf("binding not replaced: %+v", r)
	}
}

func TestDeleteReferencedModelSpecConflicts(t *testing.T) {
	h, st, _ := newModelSpecAdmin(t, false)
	putSpec(t, st, "opus", "")
	putSpec(t, st, DefaultModelSpec, "")
	putSpec(t, st, "spare", "")
	if err := st.PutRole("", Role{Name: "bound", ModelSpec: "opus"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("", Role{Name: "unbound"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opus", DefaultModelSpec} {
		rec := doReq(t, h, "DELETE", "/admin/model-specs/"+name, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "default/") {
			t.Fatalf("delete %s = %d %s, want 409 naming the role", name, rec.Code, rec.Body)
		}
		if _, ok := st.GetModelSpec(name); !ok {
			t.Fatalf("%s deleted despite the 409", name)
		}
	}
	if rec := doReq(t, h, "DELETE", "/admin/model-specs/spare", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete unreferenced = %d", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/admin/model-specs/spare", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete absent = %d", rec.Code)
	}
	if err := st.RemoveModelSpec("opus"); !errors.Is(err, ErrModelSpecInUse) {
		t.Fatalf("store remove = %v, want ErrModelSpecInUse", err)
	}
	if err := st.RemoveRole("", "bound"); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "DELETE", "/admin/model-specs/opus", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete once unbound = %d %s", rec.Code, rec.Body)
	}
}

func TestModelSpecFor(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic}, map[string]Scope{"w": {Destinations: []string{"anthropic"}}, "v": {}})

	// Unbound and no default seeded: nothing delivered (built-in defaults).
	if m, err := ModelSpecFor(st, actorWith("w")); m != nil || err != nil {
		t.Fatalf("unseeded default = %+v, %v", m, err)
	}
	putSpec(t, st, DefaultModelSpec, "")
	if m, err := ModelSpecFor(st, actorWith("w")); err != nil || m == nil || m.Name != DefaultModelSpec {
		t.Fatalf("unbound role = %+v, %v; want %s", m, err, DefaultModelSpec)
	}
	if m, err := ModelSpecFor(st, actorWith()); m != nil || err != nil {
		t.Fatalf("no grants = %+v, %v", m, err)
	}

	putSpec(t, st, "opus", "claude-opus-5-5")
	if err := UpdateRole(st, "p", "w", func(r *Role) error { r.ModelSpec = "opus"; return nil }); err != nil {
		t.Fatal(err)
	}
	m, err := ModelSpecFor(st, actorWith("w"))
	if err != nil || m == nil || m.Model.ID != "claude-opus-5-5" {
		t.Fatalf("bound role = %+v, %v", m, err)
	}
	// Two roles resolving to different specs: fail closed.
	if _, err := ModelSpecFor(st, actorWith("w", "v")); err == nil || !strings.Contains(err.Error(), "different model-specs") {
		t.Fatalf("conflict = %v", err)
	}
	// An explicit binding whose spec vanished (e.g. a config import): error.
	st.(*MemStore).applyRemoveModelSpec("opus")
	if _, err := ModelSpecFor(st, actorWith("w")); err == nil || !strings.Contains(err.Error(), `"opus"`) {
		t.Fatalf("missing bound spec = %v", err)
	}
}

// GET /connector carries the role's resolved spec — names only — and an edit
// is visible on the next fetch (what cove-master does before every episode).
func TestConnectorHandlerDeliversModelSpec(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic}, map[string]Scope{"w": {Destinations: []string{"anthropic"}}})
	putSpec(t, st, "opus", "claude-opus-5-5")
	if err := UpdateRole(st, "p", "w", func(r *Role) error { r.ModelSpec = "opus"; return nil }); err != nil {
		t.Fatal(err)
	}
	tok, _ := MintToken()
	if err := st.AddActor(Actor{ID: "a", TokenHash: HashToken(tok), Grants: []Grant{{Project: "p", Role: "w"}}}); err != nil {
		t.Fatal(err)
	}
	h := NewConnectorHandler(st, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fetch := func() snippet.Connector {
		t.Helper()
		rec := connectorReq(h, "Bearer "+tok)
		var c snippet.Connector
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
			t.Fatalf("GET /connector = %d %s", rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), tok) {
			t.Fatal("the connector carries the identity token")
		}
		return c
	}
	c := fetch()
	if c.ModelSpec == nil || c.ModelSpec.Name != "opus" || c.ModelSpec.Model.ID != "claude-opus-5-5" || c.ModelSpec.Principal.Credential != "anthropic" {
		t.Fatalf("delivered spec = %+v", c.ModelSpec)
	}
	before := snippet.Fingerprint(c)

	edited := validSpec()
	edited.Name, edited.Model.ID = "opus", "claude-opus-5-5[1m]"
	if err := UpdateModelSpec(st, edited, credIs("anthropic"), false); err != nil {
		t.Fatal(err)
	}
	c = fetch()
	if c.ModelSpec.Model.ID != "claude-opus-5-5[1m]" || snippet.Fingerprint(c) == before {
		t.Fatalf("edit not delivered: %+v", c.ModelSpec)
	}
}

func TestEnsureDefaultModelSpec(t *testing.T) {
	// A pool configured: the default authenticates as the pool.
	st := NewMemStore()
	if created, err := EnsureDefaultModelSpec(st, true); !created || err != nil {
		t.Fatalf("seed = %v, %v", created, err)
	}
	m, _ := st.GetModelSpec(DefaultModelSpec)
	if m.Principal.Credential != PoolPrincipal || m.Type != HarnessClaude || m.Version != modelspec.DefaultClaudeVersion || m.VersionConstraint != "" ||
		!slices.Equal(m.Claude.Plugins, modelspec.DefaultClaudePlugins()) ||
		m.Policy.Mode != "bypassPermissions" || m.Claude == nil || m.Claude.Provider != "anthropic" || m.Model != (ModelChoice{}) {
		t.Fatalf("seeded = %+v", m)
	}
	if err := ValidateModelSpec(m, credIs(), true); err != nil {
		t.Fatalf("the seeded default must itself be valid: %v", err)
	}

	// No pool: the anthropic destination's credential.
	st = NewMemStore()
	_ = st.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", CredName: "anthropic-key"})
	if _, err := EnsureDefaultModelSpec(st, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetModelSpec(DefaultModelSpec); m.Principal.Credential != "anthropic-key" {
		t.Fatalf("principal = %q", m.Principal.Credential)
	}
	// An operator's edit survives a restart.
	m, _ = st.GetModelSpec(DefaultModelSpec)
	m.Model.ID = "claude-opus-5-5"
	_ = st.PutModelSpec(m)
	if created, err := EnsureDefaultModelSpec(st, false); created || err != nil {
		t.Fatalf("re-seed = %v, %v", created, err)
	}
	if m, _ := st.GetModelSpec(DefaultModelSpec); m.Model.ID != "claude-opus-5-5" {
		t.Fatal("re-seed clobbered the operator's edit")
	}

	// Found by route when not named anthropic.
	st = NewMemStore()
	_ = st.AddDestination(Destination{Name: "claude", Route: "/anthropic/", CredName: "k2"})
	if _, err := EnsureDefaultModelSpec(st, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetModelSpec(DefaultModelSpec); m.Principal.Credential != "k2" {
		t.Fatalf("principal = %q", m.Principal.Credential)
	}

	// Neither: nothing seeded.
	st = NewMemStore()
	if created, err := EnsureDefaultModelSpec(st, false); created || !errors.Is(err, ErrNoDefaultPrincipal) {
		t.Fatalf("no principal = %v, %v", created, err)
	}
	if _, ok := st.GetModelSpec(DefaultModelSpec); ok {
		t.Fatal("seeded without a principal")
	}
}

// The version split (COV-242): version is an exact X.Y.Z install pin; any
// range goes in version-constraint, which must admit the pin.
func TestValidateModelSpecVersionConstraint(t *testing.T) {
	for _, v := range []string{"2.1.287", "0.0.1", "10.20.30"} {
		m := validSpec()
		m.Version = v
		if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
			t.Errorf("version %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"2.x", "2.1.x", ">=2.0.0", "*", "latest", "~2.1", "2", "v2.1.0", "2.1.0-beta", "2.1.0;id"} {
		m := validSpec()
		m.Version = v
		if err := ValidateModelSpec(m, credIs("anthropic"), false); WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), "version") {
			t.Errorf("version %q: err = %v, want 400", v, err)
		}
	}
	for _, c := range []string{"", "2.x", "2.1.x", "2.1.287", ">=2.0.0", "*"} {
		m := validSpec()
		m.Version, m.VersionConstraint = "2.1.287", c
		if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
			t.Errorf("version-constraint %q refused: %v", c, err)
		}
	}
	for c, want := range map[string]string{"latest": "version-constraint", "3.x": "does not admit", ">=2.2.0": "does not admit", "2.1.288": "does not admit"} {
		m := validSpec()
		m.Version, m.VersionConstraint = "2.1.287", c
		if err := ValidateModelSpec(m, credIs("anthropic"), false); WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), want) {
			t.Errorf("version-constraint %q: err = %v, want 400 mentioning %q", c, err, want)
		}
	}
}

// Plugin ids reach a Dockerfile RUN line: name@marketplace, shell-inert, known
// marketplace only.
func TestValidateModelSpecPlugins(t *testing.T) {
	for id, want := range map[string]string{
		"superpowers":                    "name@marketplace",
		"x@unknown-market":               "not a known marketplace",
		"a$(id)@claude-plugins-official": "name@marketplace",
		"a'b@claude-plugins-official":    "name@marketplace",
	} {
		m := validSpec()
		m.Claude.Plugins = []string{id}
		if err := ValidateModelSpec(m, credIs("anthropic"), false); WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), want) {
			t.Errorf("plugin %q: err = %v, want 400 mentioning %q", id, err, want)
		}
	}
}

// The one-time store migration (COV-242) rewrites every stored spec — exact
// versions included — keeps a legacy range that admits the pin, drops what it
// must with warnings, backfills the default plugins, and records the marker so
// it never runs again (an explicit plugins: [] written later is kept).
func TestMigrateModelSpecs(t *testing.T) {
	st := NewMemStore()
	legacySeed := modelspec.Default("pool")
	legacySeed.Version, legacySeed.Claude.Plugins = modelspec.LegacyDefaultVersion, nil
	custom := validSpec()
	custom.Name, custom.Version, custom.Claude.Plugins = "custom", "2.x", []string{"superpowers@official"}
	narrow := validSpec()
	narrow.Name, narrow.Version = "narrow", "1.x"
	exact := validSpec()
	exact.Name, exact.Version, exact.Claude.Plugins = "exact", "2.1.0", nil
	for _, m := range []ModelSpec{legacySeed, custom, narrow, exact} {
		if err := st.PutModelSpec(m); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := MigrateModelSpecs(st)
	if err != nil || !slices.Equal(rep.Migrated, []string{"claude-default", "custom", "exact", "narrow"}) {
		t.Fatalf("migrated = %+v, %v", rep, err)
	}
	if st.ModelSpecSchema() != ModelSpecSchemaVersion {
		t.Fatalf("marker = %d", st.ModelSpecSchema())
	}
	if m, _ := st.GetModelSpec("claude-default"); m.Version != modelspec.DefaultClaudeVersion || m.VersionConstraint != modelspec.LegacyDefaultVersion ||
		!slices.Equal(m.Claude.Plugins, modelspec.DefaultClaudePlugins()) {
		t.Fatalf("migrated seed = %+v %+v", m, m.Claude)
	}
	if m, _ := st.GetModelSpec("custom"); m.Version != modelspec.DefaultClaudeVersion || m.VersionConstraint != "2.x" ||
		!slices.Equal(m.Claude.Plugins, modelspec.DefaultClaudePlugins()) {
		t.Fatalf("migrated custom = %+v %+v", m, m.Claude)
	}
	if m, _ := st.GetModelSpec("narrow"); m.VersionConstraint != m.Version {
		t.Fatalf("a non-admitting range must become the pin: %+v", m)
	}
	if m, _ := st.GetModelSpec("exact"); m.Version != "2.1.0" || !slices.Equal(m.Claude.Plugins, modelspec.DefaultClaudePlugins()) {
		t.Fatalf("an exact legacy spec must keep its version and get the default plugins: %+v", m)
	}
	joined := strings.Join(rep.Warnings, "\n")
	for _, want := range []string{`"custom"`, "superpowers@official", `"narrow"`, "1.x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	for _, m := range st.ListModelSpecs() {
		if err := ValidateModelSpec(m, credIs("anthropic"), true); err != nil {
			t.Errorf("migrated %s is invalid: %v", m.Name, err)
		}
	}
	// Once recorded, never again: an explicit plugins: [] stays empty.
	exact, _ = st.GetModelSpec("exact")
	exact.Claude.Plugins = nil
	if err := st.PutModelSpec(exact); err != nil {
		t.Fatal(err)
	}
	if again, err := MigrateModelSpecs(st); err != nil || len(again.Migrated) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
	if m, _ := st.GetModelSpec("exact"); len(m.Claude.Plugins) != 0 {
		t.Fatalf("a post-migration explicit empty plugin list was backfilled: %v", m.Claude.Plugins)
	}
}

// A pre-marker backup is migrated on import and imports as current; a current
// one is taken as written.
func TestMigrateSnapshotModelSpecs(t *testing.T) {
	legacy := validSpec()
	legacy.Version, legacy.Claude.Plugins = "2.x", nil
	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, ModelSpecs: []ModelSpec{legacy}}
	MigrateSnapshotModelSpecs(&snap)
	if snap.ModelSpecSchema != ModelSpecSchemaVersion || snap.ModelSpecs[0].Version != modelspec.DefaultClaudeVersion ||
		!slices.Equal(snap.ModelSpecs[0].Claude.Plugins, modelspec.DefaultClaudePlugins()) {
		t.Fatalf("migrated snapshot = %+v", snap)
	}
	current := ConfigSnapshot{ModelSpecSchema: ModelSpecSchemaVersion, ModelSpecs: []ModelSpec{validSpec()}}
	current.ModelSpecs[0].Claude.Plugins = nil
	MigrateSnapshotModelSpecs(&current)
	if current.ModelSpecs[0].Claude.Plugins != nil {
		t.Fatal("a current snapshot must not be migrated")
	}
}

// Schema step 2 (COV-245): a store already at schema 1 gets only the
// preference step — its stored claude-default gains the preference keys it
// lacks, an operator's values are kept, and every other spec is untouched.
func TestMigrateModelSpecsDefaultSettings(t *testing.T) {
	st := NewMemStore()
	def := modelspec.Default("pool")
	def.Claude.Settings = map[string]any{"theme": "light"}
	custom := validSpec()
	custom.Name, custom.Claude.Settings, custom.Claude.Plugins = "custom", nil, nil
	// A spec stored when the managed policy keys were still accepted.
	policy := validSpec()
	policy.Name = "policy"
	policy.Claude.Settings = map[string]any{"theme": "light", "autoUpdates": true, "disableAutoMode": "disable"}
	for _, m := range []ModelSpec{def, custom, policy} {
		if err := st.PutModelSpec(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetModelSpecSchema(1); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateModelSpecs(st)
	if err != nil || !slices.Equal(rep.Migrated, []string{"claude-default", "policy"}) || len(rep.Warnings) != 2 {
		t.Fatalf("migrated = %+v, %v", rep, err)
	}
	if w := strings.Join(rep.Warnings, "\n"); !strings.Contains(w, `"policy"`) || !strings.Contains(w, "autoUpdates") || !strings.Contains(w, "disableAutoMode") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if m, _ := st.GetModelSpec("policy"); !maps.Equal(m.Claude.Settings, map[string]any{"theme": "light"}) {
		t.Fatalf("policy keys kept: %v", m.Claude.Settings)
	} else if err := UpdateModelSpec(st, m, credIs("anthropic"), true); err != nil {
		t.Fatalf("a migrated spec must stay updatable: %v", err)
	}
	if st.ModelSpecSchema() != ModelSpecSchemaVersion {
		t.Fatalf("marker = %d (want %d)", st.ModelSpecSchema(), ModelSpecSchemaVersion)
	}
	got, _ := st.GetModelSpec("claude-default")
	if got.Claude.Settings["theme"] != "light" {
		t.Fatalf("operator theme overwritten: %v", got.Claude.Settings)
	}
	for k, v := range modelspec.DefaultClaudeSettings() {
		if k != "theme" && got.Claude.Settings[k] != v {
			t.Errorf("claude-default %q = %v, want %v", k, got.Claude.Settings[k], v)
		}
	}
	if err := ValidateModelSpec(got, credIs("anthropic"), true); err != nil {
		t.Errorf("migrated claude-default is invalid: %v", err)
	}
	if m, _ := st.GetModelSpec("custom"); m.Claude.Settings != nil || m.Claude.Plugins != nil {
		t.Fatalf("a schema-1 non-default spec was touched: %+v", m.Claude)
	}
	// Recorded: an operator later removing a preference is not undone.
	got.Claude.Settings = map[string]any{}
	if err := st.PutModelSpec(got); err != nil {
		t.Fatal(err)
	}
	if again, err := MigrateModelSpecs(st); err != nil || len(again.Migrated) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

// A schema-1 backup gets only step 2 on import: preferences for
// claude-default, the managed policy keys dropped (with a warning) — so it
// stays importable.
func TestMigrateSnapshotModelSpecsDefaultSettings(t *testing.T) {
	def := modelspec.Default("pool")
	def.Claude.Settings = nil
	custom := validSpec()
	custom.Name, custom.Claude.Plugins = "custom", nil
	custom.Claude.Settings["skipDangerousModePermissionPrompt"] = true
	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, ModelSpecSchema: 1, ModelSpecs: []ModelSpec{def, custom}}
	if err := validateSnapshotContext(snap); err != nil {
		t.Fatalf("a schema-1 backup with a now-refused key must validate (as migrated): %v", err)
	}
	if w := MigrateSnapshotModelSpecs(&snap); len(w) != 1 || !strings.Contains(w[0], "skipDangerousModePermissionPrompt") {
		t.Fatalf("warnings = %v", w)
	}
	if _, ok := snap.ModelSpecs[1].Claude.Settings["skipDangerousModePermissionPrompt"]; ok {
		t.Fatal("managed policy key kept on import")
	}
	if snap.ModelSpecSchema != ModelSpecSchemaVersion || !maps.Equal(snap.ModelSpecs[0].Claude.Settings, modelspec.DefaultClaudeSettings()) {
		t.Fatalf("migrated snapshot = %+v", snap.ModelSpecs[0].Claude)
	}
	if snap.ModelSpecs[1].Claude.Plugins != nil {
		t.Fatal("a schema-1 snapshot's custom spec got the schema-1 step again")
	}
}
