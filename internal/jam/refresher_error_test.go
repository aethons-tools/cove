package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A 400 from the token endpoint must surface the OAuth error code +
// description (neither is a secret) so the failure is diagnosable, and the
// stored token must be left unchanged.
func TestRefreshDueSurfacesOAuthErrorCode(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "refresh token is expired or revoked",
		})
	}))
	defer ts.Close()

	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccount(PoolAccount{Name: "pool-a", AccessToken: "old", RefreshToken: "stale", ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewRefresher(st, RefresherOptions{
		HTTPClient: ts.Client(), TokenURL: ts.URL,
		Now: func() time.Time { return now }, Margin: 15 * time.Minute, Log: log,
	})
	if err := r.RefreshDue(context.Background()); err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "invalid_grant") || !strings.Contains(logged, "expired or revoked") {
		t.Fatalf("log did not surface the OAuth error code/description:\n%s", logged)
	}
	// The stored token must be untouched on failure.
	accts, _ := st.Accounts()
	if accts[0].AccessToken != "old" {
		t.Fatalf("token changed on a failed refresh: %+v", accts[0])
	}
}
