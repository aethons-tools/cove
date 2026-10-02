package jam

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func connectorReq(h http.Handler, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/connector", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestConnectorHandler(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic, legacyGit, ghAPI}, map[string]Scope{
		"w":     {Destinations: []string{"anthropic", "git"}},
		"other": {Destinations: []string{"github-api"}},
	})
	tok, _ := MintToken()
	expTok, _ := MintToken()
	now := time.Now()
	if err := st.AddActor(Actor{ID: "a", TokenHash: HashToken(tok), Grants: []Grant{{Project: "p", Role: "w"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddActor(Actor{ID: "old", TokenHash: HashToken(expTok), Grants: []Grant{{Project: "p", Role: "w"}}, Expiry: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	h := NewConnectorHandler(st, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for name, auth := range map[string]string{"none": "", "unknown": "Bearer nope", "expired": "Bearer " + expTok} {
		if rec := connectorReq(h, auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
	for _, auth := range []string{"Bearer " + tok, "token " + tok} {
		rec := connectorReq(h, auth)
		var c snippet.Connector
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
			t.Fatalf("%q: status = %d body=%s", auth, rec.Code, rec.Body.String())
		}
		if c.Env["ANTHROPIC_API_KEY"] != "{token}" || c.GitRoute != "/git/" || c.Env["GH_HOST"] != "" {
			t.Fatalf("connector = %+v (other role's destinations must not leak in)", c)
		}
	}
	post := httptest.NewRequest(http.MethodPost, "/connector", nil)
	post.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

func TestConnectorHandlerConflict(t *testing.T) {
	clash := Destination{Name: "y", Route: "/y/", Upstream: "https://y", Env: map[string]string{"GH_HOST": "other"}}
	st := connectorStore(t, []Destination{ghAPI, clash}, map[string]Scope{"w": {Destinations: []string{"github-api", "y"}}})
	tok, _ := MintToken()
	if err := st.AddActor(Actor{ID: "a", TokenHash: HashToken(tok), Grants: []Grant{{Project: "p", Role: "w"}}}); err != nil {
		t.Fatal(err)
	}
	h := NewConnectorHandler(st, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rec := connectorReq(h, "Bearer "+tok); rec.Code != http.StatusConflict {
		t.Fatalf("conflict = %d, want 409", rec.Code)
	}
}
