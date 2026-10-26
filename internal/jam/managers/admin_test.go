package managers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
)

// adminServer serves AdminRoutes over a Hub with the given live instances.
func adminServer(t *testing.T, insts []jam.Instance) *httptest.Server {
	t.Helper()
	store := jam.NewMemStore()
	hub := NewHub(Config{Store: store, Router: fleet.New(nil), Version: "v1", Log: slog.New(slog.DiscardHandler),
		Instances: func() []jam.Instance { return insts }})
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, AdminRoutes(hub)...)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func doReq(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestAdminManagers(t *testing.T) {
	ts := adminServer(t, []jam.Instance{{ActorID: "a1", Manager: "busy", Phase: jam.PhaseLive}})
	u := ts.URL + "/admin/managers"

	code, body := doReq(t, "POST", u, `{"name":"mac"}`)
	if code != 201 {
		t.Fatalf("add: %d %s", code, body)
	}
	var tok jam.ManagerToken
	if err := json.Unmarshal([]byte(body), &tok); err != nil || tok.Name != "mac" || tok.Token == "" {
		t.Fatalf("add body: %q %v", body, err)
	}
	if code, _ := doReq(t, "POST", u, `{"name":"mac"}`); code != 409 {
		t.Fatalf("duplicate add: %d", code)
	}
	if code, _ := doReq(t, "POST", u, `{"name":"Bad Name"}`); code != 400 {
		t.Fatalf("invalid name: %d", code)
	}
	if code, _ := doReq(t, "POST", u, `{"name":"local"}`); code != 400 {
		t.Fatalf("reserved name: %d", code)
	}
	if code, _ := doReq(t, "POST", u, `not json`); code != 400 {
		t.Fatalf("bad json: %d", code)
	}

	code, body = doReq(t, "GET", u, "")
	var list []jam.ManagerStatus
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil {
		t.Fatalf("list: %d %s", code, body)
	}
	found := false
	for _, s := range list {
		found = found || s.Name == "mac"
	}
	if !found {
		t.Fatalf("mac not listed: %s", body)
	}

	code, body = doReq(t, "POST", u+"/mac/rotate", "")
	var rot jam.ManagerToken
	if code != 200 || json.Unmarshal([]byte(body), &rot) != nil || rot.Token == "" || rot.Token == tok.Token {
		t.Fatalf("rotate: %d %s", code, body)
	}
	if code, _ := doReq(t, "POST", u+"/nope/rotate", ""); code != 404 {
		t.Fatalf("rotate unknown: %d", code)
	}

	if code, _ := doReq(t, "POST", u, `{"name":"busy"}`); code != 201 {
		t.Fatal("add busy")
	}
	code, body = doReq(t, "DELETE", u+"/busy", "")
	if code != 409 || !strings.Contains(body, "hosts 1 studios") {
		t.Fatalf("revoke busy: %d %s", code, body)
	}
	if code, _ := doReq(t, "DELETE", u+"/busy?force=true", ""); code != 204 {
		t.Fatalf("force revoke: %d", code)
	}
	if code, _ := doReq(t, "DELETE", u+"/mac", ""); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := doReq(t, "DELETE", u+"/mac", ""); code != 404 {
		t.Fatalf("revoke again: %d", code)
	}
}
