//go:build integration

package main

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// fakeResolver returns a canned real credential for any name.
type fakeResolver struct{}

func (fakeResolver) Resolve(string) (string, error) { return "REAL-ANTHROPIC", nil }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func timeNow() time.Time { return time.Now() }

// TestServeBrokersOverTLS starts the broker on a TLS listener with a self-signed
// cert, enrolls an identity, and proves an Anthropic request is credential-swapped
// end-to-end over HTTPS. Run: `go test -tags integration ./cmd/at-harbor/`.
func TestServeBrokersOverTLS(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.AddDestination(harbor.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: harbor.ApplyXAPIKey, CredName: "anthropic-key", Apply: harbor.ApplyXAPIKey}); err != nil {
		t.Fatal(err)
	}
	tok, _ := harbor.Enroll(store, "spider-18", "ACME", "guest", []string{"anthropic"}, nil, 0, timeNow())
	broker := harbor.NewBroker(store, fakeResolver{}, discardLogger())

	ts := httptest.NewTLSServer(broker)
	defer ts.Close()
	client := ts.Client() // trusts the test server's self-signed cert

	req, _ := http.NewRequest("POST", ts.URL+"/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", tok)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "REAL-ANTHROPIC" {
		t.Fatalf("upstream X-Api-Key = %q", gotAuth)
	}
	_ = tls.VersionTLS12
}
