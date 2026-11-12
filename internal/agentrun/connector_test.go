package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

type fakeSource struct {
	c   snippet.Connector
	err error
}

func (f *fakeSource) Fetch(context.Context) (snippet.Connector, error) { return f.c, f.err }

type fakeGit struct {
	calls [][5]string
	err   error // returned by every Route call while set
}

func (g *fakeGit) Route(oldBase, oldR, newBase, newR, newTarget string) error {
	g.calls = append(g.calls, [5]string{oldBase, oldR, newBase, newR, newTarget})
	return g.err
}

func newTestLogger(w *strings.Builder) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func newRefresher(src *fakeSource, git *fakeGit, initial snippet.Connector, environ []string) *connectorRefresher {
	return newConnectorRefresher(ConnectorConfig{
		Source: src, Git: git, BaseURL: "https://jam.example", Token: "tok-XYZ",
		Initial: initial, Environ: func() []string { return environ },
	}, nil)
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestConnectorAppliesFetched(t *testing.T) {
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "ANTHROPIC_API_KEY": "{token}"}}}
	r := newRefresher(src, &fakeGit{}, snippet.Connector{}, []string{"PATH=/bin"})
	env, fp, changed := r.prepare(context.Background())
	m := envMap(env)
	if m["GH_HOST"] != "jam.example" || m["ANTHROPIC_API_KEY"] != "tok-XYZ" || m["PATH"] != "/bin" {
		t.Fatalf("env = %v", m)
	}
	if fp != snippet.Fingerprint(src.c) || !changed {
		t.Fatalf("fp=%q changed=%v", fp, changed)
	}
	if _, fp2, changed2 := r.prepare(context.Background()); fp2 != fp || changed2 {
		t.Fatal("unchanged connector must not report again")
	}
}

func TestConnectorDropsRemovedKeys(t *testing.T) {
	initial := snippet.Connector{Env: map[string]string{"OLD_VAR": "x", "GH_HOST": "{host}"}}
	// jam-studio-agent's own env still carries the raise-time values.
	environ := []string{"PATH=/bin", "OLD_VAR=x", "GH_HOST=jam.example"}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	r := newRefresher(src, &fakeGit{}, initial, environ)
	env, _, _ := r.prepare(context.Background())
	if _, ok := envMap(env)["OLD_VAR"]; ok {
		t.Fatalf("removed key leaked into the spawn env: %v", env)
	}
	if n := slices.IndexFunc(env, func(s string) bool { return strings.HasPrefix(s, "GH_HOST=") }); n < 0 {
		t.Fatal("kept key missing")
	}
	// No duplicates of a connector key.
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_HOST=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("GH_HOST appears %d times", count)
	}
}

func TestConnectorFetchFailureKeepsLast(t *testing.T) {
	initial := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}
	src := &fakeSource{err: errors.New("401 unknown identity")}
	git := &fakeGit{}
	r := newRefresher(src, git, initial, []string{"PATH=/bin"})
	env, fp, changed := r.prepare(context.Background())
	if envMap(env)["GH_HOST"] != "jam.example" {
		t.Fatalf("fallback env = %v", env)
	}
	// First turn reports what it actually applied (the initial connector).
	if fp != snippet.Fingerprint(initial) || !changed {
		t.Fatalf("fp=%q changed=%v", fp, changed)
	}
	if _, _, changed := r.prepare(context.Background()); changed {
		t.Fatal("a repeated failure must not re-report")
	}
	if len(git.calls) != 0 {
		t.Fatalf("git rewritten on failure: %v", git.calls)
	}
}

func TestConnectorGitOnlyOnRouteChange(t *testing.T) {
	initial := snippet.Connector{GitRoute: "/git/"}
	src := &fakeSource{c: snippet.Connector{GitRoute: "/git/"}}
	git := &fakeGit{}
	r := newRefresher(src, git, initial, nil)
	r.prepare(context.Background())
	if len(git.calls) != 0 {
		t.Fatalf("same route rewrote git: %v", git.calls)
	}
	src.c = snippet.Connector{GitRoute: "/git2/"}
	r.prepare(context.Background())
	if len(git.calls) != 1 || git.calls[0] != [5]string{"https://jam.example", "/git/", "https://jam.example", "/git2/", snippet.GitRewriteTarget} {
		t.Fatalf("git calls = %v", git.calls)
	}
}

// A failed git rewrite leaves the old route in effect, so the reported
// fingerprint is the applied connector with the route actually configured —
// never the fetched one, which Jam would wrongly show as ok.
func TestConnectorGitFailureReportsRouteInEffect(t *testing.T) {
	initial := snippet.Connector{GitRoute: "/git/"}
	want := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}, GitRoute: "/git2/"}
	src := &fakeSource{c: want}
	git := &fakeGit{err: errors.New("git config: lock held")}
	r := newRefresher(src, git, initial, nil)
	env, fp, changed := r.prepare(context.Background())
	if envMap(env)["GH_HOST"] != "jam.example" {
		t.Fatalf("env must still apply when git fails: %v", env)
	}
	inEffect := snippet.Connector{Env: want.Env, GitRoute: "/git/"}
	if fp != snippet.Fingerprint(inEffect) || !changed {
		t.Fatalf("fp=%q changed=%v, want the in-effect connector's fingerprint", fp, changed)
	}
	git.err = nil
	_, fp, changed = r.prepare(context.Background())
	if len(git.calls) != 2 || git.calls[1] != [5]string{"https://jam.example", "/git/", "https://jam.example", "/git2/", snippet.GitRewriteTarget} {
		t.Fatalf("route not retried: %v", git.calls)
	}
	if fp != snippet.Fingerprint(want) || !changed {
		t.Fatalf("after the retry lands fp=%q changed=%v, want the fetched connector", fp, changed)
	}
}

// A route rewrite that failed is retried on a later turn even when that turn's
// fetch fails too: the fallback connector still wants the new route.
func TestConnectorFetchFailureRetriesPendingRoute(t *testing.T) {
	want := snippet.Connector{GitRoute: "/git2/"}
	src := &fakeSource{c: want}
	git := &fakeGit{err: errors.New("git config: lock held")}
	r := newRefresher(src, git, snippet.Connector{GitRoute: "/git/"}, nil)
	r.prepare(context.Background())
	src.err, git.err = errors.New("jam down"), nil
	_, fp, changed := r.prepare(context.Background())
	if len(git.calls) != 2 || git.calls[1] != [5]string{"https://jam.example", "/git/", "https://jam.example", "/git2/", snippet.GitRewriteTarget} {
		t.Fatalf("pending route not retried on fetch failure: %v", git.calls)
	}
	if fp != snippet.Fingerprint(want) || !changed {
		t.Fatalf("fp=%q changed=%v", fp, changed)
	}
}

func TestConnectorNeverLogsToken(t *testing.T) {
	var buf strings.Builder
	src := &fakeSource{err: errors.New("boom")}
	r := newConnectorRefresher(ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://j", Token: "tok-XYZ",
		Environ: func() []string { return nil }}, newTestLogger(&buf))
	r.prepare(context.Background())
	if strings.Contains(buf.String(), "tok-XYZ") {
		t.Fatalf("token logged: %s", buf.String())
	}
}

func TestConnectorDroppedKeyStaysDropped(t *testing.T) {
	initial := snippet.Connector{Env: map[string]string{"OLD_VAR": "x", "GH_HOST": "{host}"}}
	environ := []string{"PATH=/bin", "OLD_VAR=x", "GH_HOST=jam.example"}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	r := newRefresher(src, &fakeGit{}, initial, environ)
	for turn := 1; turn <= 2; turn++ {
		env, _, _ := r.prepare(context.Background())
		if _, ok := envMap(env)["OLD_VAR"]; ok {
			t.Fatalf("turn %d: dropped key resurfaced: %v", turn, env)
		}
	}
}

func TestHTTPConnectorSourceHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HTTPConnectorSource(srv.URL, "TOK").Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch on a cancelled ctx = %v, want context.Canceled", err)
	}
}

// A git destination whose upstream changes host keeps its route but must
// rewrite the new host: the target is part of what is applied.
func TestConnectorReroutesOnTargetChange(t *testing.T) {
	initial := snippet.Connector{GitRoute: "/git/"}
	src := &fakeSource{c: snippet.Connector{GitRoute: "/git/", GitTarget: "https://gitlab.example.com/"}}
	git := &fakeGit{}
	r := newRefresher(src, git, initial, nil)
	_, fp, _ := r.prepare(context.Background())
	want := [5]string{"https://jam.example", "/git/", "https://jam.example", "/git/", "https://gitlab.example.com/"}
	if len(git.calls) != 1 || git.calls[0] != want {
		t.Fatalf("git calls = %v, want [%v]", git.calls, want)
	}
	if fp != snippet.Fingerprint(src.c) {
		t.Fatal("fingerprint does not report the applied target")
	}
	r.prepare(context.Background())
	if len(git.calls) != 1 {
		t.Fatalf("unchanged route+target rewrote git again: %v", git.calls)
	}
}

// A git endpoint on a subdomain routes the upstream host through
// https://<GitHost><route>: the rewrite and the credential helper move from the
// Jam host to the endpoint's host, and the fingerprint reports the host.
func TestConnectorRoutesGitOnSubdomain(t *testing.T) {
	src := &fakeSource{c: snippet.Connector{GitRoute: "/", GitHost: "git.jam.example"}}
	git := &fakeGit{}
	r := newRefresher(src, git, snippet.Connector{GitRoute: "/git/"}, nil)
	_, fp, _ := r.prepare(context.Background())
	want := [5]string{"https://jam.example", "/git/", "https://git.jam.example", "/", snippet.GitRewriteTarget}
	if len(git.calls) != 1 || git.calls[0] != want {
		t.Fatalf("git calls = %v, want [%v]", git.calls, want)
	}
	if fp != snippet.Fingerprint(src.c) {
		t.Fatal("fingerprint does not report the applied host")
	}
	r.prepare(context.Background())
	if len(git.calls) != 1 {
		t.Fatalf("unchanged route+host rewrote git again: %v", git.calls)
	}
	// Back to the Jam host on the same route: the host alone changed.
	src.c = snippet.Connector{GitRoute: "/"}
	r.prepare(context.Background())
	if len(git.calls) != 2 || git.calls[1] != [5]string{"https://git.jam.example", "/", "https://jam.example", "/", snippet.GitRewriteTarget} {
		t.Fatalf("host change not applied: %v", git.calls)
	}
}

// End to end: a Jam serving a connector whose git endpoint is on a subdomain,
// fetched over HTTP and applied with the real git (a throwaway global config):
// github.com rewrites to the subdomain, the credential helper is scoped to it,
// and nothing is left on the Jam host's base.
func TestConnectorGitOnSubdomainEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	conn := snippet.Connector{GitRoute: "/git/"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connector" || r.Header.Get("Authorization") != "Bearer tok-XYZ" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(conn)
	}))
	defer srv.Close()
	r := newConnectorRefresher(ConnectorConfig{Source: HTTPConnectorSource(srv.URL, "tok-XYZ"), BaseURL: "https://jam.example",
		Token: "tok-XYZ", Environ: func() []string { return nil }}, nil)
	gitGet := func(key string) string {
		out, _ := exec.Command("git", "config", "--global", "--get", key).Output()
		return strings.TrimSpace(string(out))
	}
	r.prepare(context.Background())
	if got := gitGet("url.https://jam.example/git/.insteadOf"); got != snippet.GitRewriteTarget {
		t.Fatalf("Jam-host route: insteadOf = %q", got)
	}
	conn = snippet.Connector{GitRoute: "/", GitHost: "git.jam.example", GitTarget: "https://gitlab.example.com/"}
	r.prepare(context.Background())
	if got := gitGet("url.https://git.jam.example/.insteadOf"); got != "https://gitlab.example.com/" {
		t.Fatalf("subdomain route: insteadOf = %q", got)
	}
	if gitGet("credential.https://git.jam.example.helper") != snippet.GitHelper() {
		t.Fatal("credential helper not scoped to the subdomain")
	}
	if gitGet("url.https://jam.example/git/.insteadOf") != "" || gitGet("credential.https://jam.example.helper") != "" {
		b, _ := os.ReadFile(cfg)
		t.Fatalf("Jam-host routing left behind:\n%s", b)
	}
}
