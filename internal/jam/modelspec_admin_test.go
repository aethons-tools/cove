package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func newModelSpecAdmin(t *testing.T, poolConfigured bool) (http.Handler, Store, *bytes.Buffer) {
	t.Helper()
	store := NewMemStore()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	credExists := func(n string) bool { return n == "anthropic" }
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, credExists, nil, log, nil, nil,
		WithModelSpecs(store, credExists, poolConfigured, log))
	return h, store, &logs
}

func specJSON(t *testing.T, m ModelSpec) io.Reader {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}

func TestAdminModelSpecCRUD(t *testing.T) {
	h, store, _ := newModelSpecAdmin(t, false)
	m := validSpec()

	if rec := doReq(t, h, "POST", "/admin/model-specs", specJSON(t, m)); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "POST", "/admin/model-specs", specJSON(t, m)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", rec.Code)
	}

	rec := doReq(t, h, "GET", "/admin/model-specs", nil)
	var list []ModelSpec
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Name != m.Name {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}

	rec = doReq(t, h, "GET", "/admin/model-specs/"+m.Name, nil)
	var got ModelSpec
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Claude == nil || got.Claude.Provider != "vertex" {
		t.Fatalf("show = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "GET", "/admin/model-specs/ghost", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("show missing = %d, want 404", rec.Code)
	}

	m.Version = "2.1.x"
	if rec := doReq(t, h, "PUT", "/admin/model-specs/"+m.Name, specJSON(t, m)); rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if got, _ := store.GetModelSpec(m.Name); got.Version != "2.1.x" {
		t.Fatalf("after update version = %q", got.Version)
	}
	ghost := validSpec()
	ghost.Name = "ghost"
	if rec := doReq(t, h, "PUT", "/admin/model-specs/ghost", specJSON(t, ghost)); rec.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d, want 404", rec.Code)
	}
	if rec := doReq(t, h, "PUT", "/admin/model-specs/other", specJSON(t, m)); rec.Code != http.StatusBadRequest {
		t.Fatalf("update with mismatched name = %d, want 400", rec.Code)
	}

	if rec := doReq(t, h, "DELETE", "/admin/model-specs/"+m.Name, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "DELETE", "/admin/model-specs/"+m.Name, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
}

func TestAdminModelSpecValidation(t *testing.T) {
	h, store, _ := newModelSpecAdmin(t, false)
	m := validSpec()
	m.Principal.Credential = PoolPrincipal // no pool configured
	if rec := doReq(t, h, "POST", "/admin/model-specs", specJSON(t, m)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "pool") {
		t.Fatalf("pool without pool = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "POST", "/admin/model-specs", strings.NewReader("{not json")); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d, want 400", rec.Code)
	}
	if len(store.ListModelSpecs()) != 0 {
		t.Fatal("a refused write reached the store")
	}

	h, _, _ = newModelSpecAdmin(t, true)
	if rec := doReq(t, h, "POST", "/admin/model-specs", specJSON(t, m)); rec.Code != http.StatusCreated {
		t.Fatalf("pool with pool = %d %s, want 201", rec.Code, rec.Body)
	}
}

func TestAdminModelSpecNeverLogsEnvValues(t *testing.T) {
	h, _, logs := newModelSpecAdmin(t, false)
	m := validSpec()
	m.Claude.ProviderEnv["CLOUD_ML_REGION"] = "region-value-marker"
	if rec := doReq(t, h, "POST", "/admin/model-specs", specJSON(t, m)); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "admin model-spec created") {
		t.Fatalf("missing audit log: %s", logs)
	}
	if strings.Contains(logs.String(), "region-value-marker") {
		t.Fatalf("log carries a provider-env value: %s", logs)
	}
}
