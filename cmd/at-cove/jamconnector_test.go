package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// Tests in this package never reach a real Jam: jamPlan's connector fetch is
// stubbed to "no connector" (the legacy contract) unless a test overrides it.
func init() {
	fetchJamConnector = func(host, token string) (*snippet.Connector, error) { return nil, nil }
}

func TestJamConnectorFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connector":
			_, _ = w.Write([]byte(`{"env":{"GH_HOST":"{host}"}}`))
		case "/old/connector":
			http.NotFound(w, r)
		default:
			http.Error(w, "connector conflict", http.StatusConflict)
		}
	}))
	var warn bytes.Buffer
	c, err := jamConnector(srv.Client(), srv.URL, "T", &warn)
	if err != nil || c == nil || c.Env["GH_HOST"] != "{host}" {
		t.Fatalf("200: %+v, %v", c, err)
	}
	if c, err := jamConnector(srv.Client(), srv.URL+"/old", "T", &warn); err != nil || c != nil {
		t.Fatalf("404 must fall back to legacy (nil, nil): %+v, %v", c, err)
	}
	if _, err := jamConnector(srv.Client(), srv.URL+"/clash", "T", &warn); err == nil {
		t.Fatal("409 must be an error (fail closed)")
	}
	url := srv.URL
	srv.Close()
	warn.Reset()
	if c, err := jamConnector(&http.Client{}, url, "T", &warn); err != nil || c != nil || !strings.Contains(warn.String(), "legacy") {
		t.Fatalf("unreachable Jam: %+v, %v, warn=%q (want legacy fallback + warning)", c, err, warn.String())
	}
}

func TestJamPlanCarriesConnector(t *testing.T) {
	served := &snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}
	prev := fetchJamConnector
	defer func() { fetchJamConnector = prev }()
	var fetched string
	fetchJamConnector = func(host, token string) (*snippet.Connector, error) {
		fetched = host + "|" + token
		return served, nil
	}
	cfg := kit.Config{Name: "k", Jam: &kit.JamConfig{Host: "jam.local", Identity: "JAM_ID"}}
	store := usersecret.Store{Kits: map[string]map[string]usersecret.Source{"k": {"JAM_ID": {Value: ptr("tok-abc")}}}}
	ha, _, err := jamPlan(cfg, store, nil, "k", "cove-1", "/kp", "/s.yml", &runner.Fake{})
	if err != nil || ha.Connector != served || fetched != "jam.local|tok-abc" {
		t.Fatalf("pre-supplied: %+v, %v (fetched %q)", ha, err, fetched)
	}
	// auto-enroll: the connector rides on `at-jam enroll --json`; no fetch.
	fetched = ""
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"id":"c","token":"TKN","connector":{"env":{"X":"1"}}}` + "\n"}}}
	ha, _, err = jamPlan(kit.Config{Name: "k", Jam: &kit.JamConfig{Host: "jam.local"}}, usersecret.Store{}, nil, "k", "c", "/kp", "/s.yml", f)
	if err != nil || ha.Connector == nil || ha.Connector.Env["X"] != "1" || fetched != "" {
		t.Fatalf("auto-enroll: %+v, %v (fetched %q)", ha, err, fetched)
	}
}

// Only "can't reach Jam" falls back; a TLS failure (or any non-dial error) is a
// real signal and fails closed.
func TestJamConnectorTLSErrorFailsClosed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var warn bytes.Buffer
	if _, err := jamConnector(&http.Client{}, srv.URL, "T", &warn); err == nil { // untrusted test cert
		t.Fatalf("TLS verification failure must be an error, got fallback (warn=%q)", warn.String())
	}
}
