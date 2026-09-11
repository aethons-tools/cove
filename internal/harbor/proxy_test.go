package harbor

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCreds returns canned real credentials.
type fakeCreds map[string]string

func (f fakeCreds) Resolve(name string) (string, error) { return f[name], nil }

func newTestBroker(t *testing.T, upstreamAnthropic, upstreamGit string) (*Broker, *bytes.Buffer, string) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := MintToken()
	if err := store.Add(Identity{
		ID: "spider-18", TokenHash: HashToken(tok), Project: "ACME", Role: "guest",
		Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: upstreamAnthropic, IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: upstreamGit, IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true},
	}}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewBroker(store, cfg, fakeCreds{"anthropic-bearer": "REAL-ANTHROPIC", "git-pat": "REAL-PAT"}, log), &logbuf, tok
}

func TestBrokerSwapsAnthropicBearer(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	b, logbuf, tok := newTestBroker(t, up.URL, "http://unused")
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotAuth != "Bearer REAL-ANTHROPIC" {
		t.Fatalf("upstream Authorization = %q, want swapped real cred", gotAuth)
	}
	if strings.Contains(logbuf.String(), tok) || strings.Contains(logbuf.String(), "REAL-ANTHROPIC") {
		t.Fatal("secret material leaked into logs")
	}
}

func TestBrokerSwapsGitBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	b, _, tok := newTestBroker(t, "http://unused", up.URL)
	req := httptest.NewRequest("GET", "/git/acme/api/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("x-access-token", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPass != "REAL-PAT" {
		t.Fatalf("upstream git password = %q, want REAL-PAT", gotPass)
	}
	_ = gotUser
}

func TestBrokerDeniesOutOfScopeRepo(t *testing.T) {
	b, _, tok := newTestBroker(t, "http://unused", "http://unused")
	req := httptest.NewRequest("GET", "/git/someone/secret/info/refs", nil)
	req.SetBasicAuth("x-access-token", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestBrokerRejectsUnknownIdentity(t *testing.T) {
	b, _, _ := newTestBroker(t, "http://unused", "http://unused")
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// Git sends its credential only after a WWW-Authenticate: Basic challenge, so a
// basic-password destination must challenge on the unauthenticated first request —
// otherwise git reports "Authentication failed" without ever presenting the token.
func TestBrokerChallengesBasicAuth(t *testing.T) {
	b, _, _ := newTestBroker(t, "http://unused", "http://unused")
	req := httptest.NewRequest("GET", "/git/acme/api.git/info/refs?service=git-upload-pack", nil)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic") {
		t.Fatalf("WWW-Authenticate = %q, want a Basic challenge (git won't send credentials without it)", got)
	}
}

// The Anthropic API authenticates API keys on the x-api-key header, not Bearer —
// so the anthropic destination reads the identity from x-api-key and swaps the
// real API key onto x-api-key too (LiteLLM-style gateway shape).
func TestBrokerSwapsXAPIKey(t *testing.T) {
	var gotKey, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	store, _ := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	tok, _ := MintToken()
	if err := store.Add(Identity{ID: "spider-18", TokenHash: HashToken(tok), Destinations: []string{"anthropic"}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: ApplyXAPIKey, CredName: "anthropic-key", Apply: ApplyXAPIKey},
	}}
	b := NewBroker(store, cfg, fakeCreds{"anthropic-key": "REAL-ANTHROPIC"}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Api-Key", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotKey != "REAL-ANTHROPIC" {
		t.Fatalf("upstream X-Api-Key = %q, want swapped real key", gotKey)
	}
	if gotAuth != "" {
		t.Fatalf("upstream Authorization = %q, want empty", gotAuth)
	}
}
