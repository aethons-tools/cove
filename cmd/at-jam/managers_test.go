package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
)

func managerServer(t *testing.T) *httptest.Server {
	t.Helper()
	seen := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/managers", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]jam.ManagerStatus{
			{Name: "mac", Connected: true, Version: "v1", Studios: 2, Capacity: 4, LastSeen: seen, Strays: []jam.ManagedStudio{{ActorID: "x"}}},
			{Name: "box"},
		})
	})
	mux.HandleFunc("POST /admin/managers", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(jam.ManagerToken{Name: b.Name, Token: "tok-add"})
	})
	mux.HandleFunc("POST /admin/managers/{name}/rotate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jam.ManagerToken{Name: r.PathValue("name"), Token: "tok-rot"})
	})
	mux.HandleFunc("DELETE /admin/managers/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("force") != "true" {
			http.Error(w, "manager mac hosts 2 studios; revoke with force to forget them", http.StatusConflict)
			return
		}
		w.WriteHeader(204)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func runManager(t *testing.T, ts *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdManager(append(args, "--admin-url", ts.URL), cli.Globals{}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestManagerAddRotate(t *testing.T) {
	ts := managerServer(t)
	code, out, errs := runManager(t, ts, "add", "mac")
	want := "manager mac token (shown once): tok-add\nput it in the manager's config; Jam stores only its hash\n"
	if code != 0 || out != want || strings.Contains(errs, "tok-add") {
		t.Fatalf("add: %d %q %q", code, out, errs)
	}
	code, out, _ = runManager(t, ts, "rotate", "mac")
	if code != 0 || !strings.HasPrefix(out, "manager mac token (shown once): tok-rot\n") {
		t.Fatalf("rotate: %d %q", code, out)
	}
}

func TestManagerList(t *testing.T) {
	code, out, errs := runManager(t, managerServer(t), "list")
	want := "mac\tconnected=true\tversion=v1\tstudios=2\tcapacity=4\tlast-seen=2026-10-26T12:00:00Z\tstrays=1\n" +
		"box\tconnected=false\tversion=\tstudios=0\tcapacity=0\tlast-seen=-\tstrays=0\n"
	if code != 0 || out != want {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
}

func TestManagerRevoke(t *testing.T) {
	ts := managerServer(t)
	code, out, errs := runManager(t, ts, "revoke", "mac")
	if code != 1 || out != "" || !strings.Contains(errs, "hosts 2 studios") {
		t.Fatalf("revoke: %d %q %q", code, out, errs)
	}
	code, out, errs = runManager(t, ts, "revoke", "mac", "--force")
	if code != 0 || out != "revoked mac\n" {
		t.Fatalf("force: %d %q %q", code, out, errs)
	}
}
