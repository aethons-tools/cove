package jam

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
