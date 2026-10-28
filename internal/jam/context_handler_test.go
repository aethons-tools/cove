package jam

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestContextHandler(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(store, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/context", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get(tok)
	var b sessionctx.Bundle
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &b) != nil || b.Fingerprint != fl.gotSpec.Context.Fingerprint {
		t.Fatalf("GET /context = %d %s", rec.Code, rec.Body)
	}
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no identity = %d", rec.Code)
	}
	if rec := get("bogus"); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown identity = %d", rec.Code)
	}
	orphan, err := Enroll(store, "hand", "default", "guest", nil, *clk)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(orphan); rec.Code != http.StatusNotFound || rec.Header().Get("X-Jam-Context") != "1" {
		t.Errorf("identity without an instance = %d (marker %q), want a marked 404", rec.Code, rec.Header().Get("X-Jam-Context"))
	}
	req := httptest.NewRequest(http.MethodPost, "/context", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
}

// A cove that speaks the structured turn end asks for its guidance with
// ?turn-protocol=2; anything else gets the legacy boilerplate.
func TestContextHandlerTurnProtocol(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(store, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(query string) sessionctx.Bundle {
		req := httptest.NewRequest(http.MethodGet, "/context"+query, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var b sessionctx.Bundle
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &b) != nil {
			t.Fatalf("GET /context%s = %d %s", query, rec.Code, rec.Body)
		}
		want := 0
		if query == "?turn-protocol=2" {
			want = 2
		}
		if b.TurnProtocol != want || (want == 0) != !strings.Contains(rec.Body.String(), `"turn_protocol"`) {
			t.Fatalf("GET /context%s: turn_protocol = %d, want %d (absent at 0)", query, b.TurnProtocol, want)
		}
		return b
	}
	legacy, structured := get(""), get("?turn-protocol=2")
	if structured.Fingerprint == legacy.Fingerprint || !strings.Contains(structured.Core, "End your turn with your reply") {
		t.Fatalf("turn-protocol=2 left the context unchanged:\n%s", structured.Core)
	}
	if strings.Contains(legacy.Core, "End your turn with your reply") {
		t.Fatal("the legacy context carries the structured guidance")
	}
	if other := get("?turn-protocol=3"); other.Fingerprint != legacy.Fingerprint {
		t.Fatal("an unknown turn protocol changed the context")
	}
}

func TestContextForRemembersTurnProtocol(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(store, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/context?turn-protocol=2", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var served sessionctx.Bundle
	if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	// Setup inspection recompiles what the cove was served, not the legacy
	// guidance (which would show its context as stale).
	again, err := sup.ContextFor("w1")
	if err != nil || again.Fingerprint != served.Fingerprint {
		t.Fatalf("ContextFor after a structured fetch: %v, fingerprint %s want %s", err, again.Fingerprint, served.Fingerprint)
	}
}
