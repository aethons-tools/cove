// Package gittest serves throwaway git repositories over smart HTTP for
// tests, using the git CLI and git-http-backend. Tests that use it skip when
// either is missing.
package gittest

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Option configures Serve.
type Option func(*config)

type config struct{ user, pass string }

// WithAuth requires HTTP Basic auth with exactly user and pass.
func WithAuth(user, pass string) Option {
	return func(c *config) { c.user, c.pass = user, pass }
}

// Serve commits files (slash paths to contents) to the main branch of a new
// bare repository, tags that commit with the annotated tag v1, and serves the
// repository read-only. It returns the repository URL, the first commit, and
// update, which writes more files over the work tree, commits them to main,
// and returns the new commit.
func Serve(t testing.TB, files map[string]string, opts ...Option) (url, commit string, update func(files map[string]string) string) {
	t.Helper()
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	execPath := strings.TrimSpace(run(t, "", "git", "--exec-path"))
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not installed")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "r.git")
	work := filepath.Join(root, "work")
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", bare)
	// Like the big forges: a client may fetch any reachable commit by id.
	run(t, bare, "git", "config", "uploadpack.allowReachableSHA1InWant", "true")
	run(t, "", "git", "init", "-q", "-b", "main", work)

	commitFiles := func(files map[string]string) string {
		for name, body := range files {
			p := filepath.Join(work, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run(t, work, "git", "add", "-A")
		run(t, work, "git", "commit", "-q", "--allow-empty", "-m", "update")
		run(t, work, "git", "push", "-q", bare, "main")
		return strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))
	}
	commit = commitFiles(files)
	run(t, work, "git", "tag", "-a", "v1", "-m", "v1")
	run(t, work, "git", "push", "-q", bare, "v1")

	var h http.Handler = &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"},
		// Tests provoke backend failures on purpose (a client over its byte
		// limit hangs up, an unknown commit is wanted): keep output quiet.
		Logger: log.New(io.Discard, "", 0),
		Stderr: io.Discard,
	}
	if cfg.user != "" {
		next := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != cfg.user || p != cfg.pass {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL + "/r.git", commit, commitFiles
}

// run runs a git command hermetically (no user or system config) and returns
// its stdout.
func run(t testing.TB, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}
