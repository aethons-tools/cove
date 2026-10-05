package jam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

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
	if m.Principal.Credential != PoolPrincipal || m.Type != HarnessClaude || m.Version != ">=2.0.0" ||
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

func TestValidateModelSpecVersionConstraint(t *testing.T) {
	for _, v := range []string{"2.x", "2.1.x", "2.1.287", ">=2.0.0", "*"} {
		m := validSpec()
		m.Version = v
		if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
			t.Errorf("version %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"latest", "~2.1", "2"} {
		m := validSpec()
		m.Version = v
		if err := ValidateModelSpec(m, credIs("anthropic"), false); WriteStatus(err, 0) != http.StatusBadRequest {
			t.Errorf("version %q: err = %v, want 400", v, err)
		}
	}
}
