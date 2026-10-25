//go:build integration

package main

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
	"github.com/aethons-tools/cove/internal/secret"
)

// kitGitJam runs the real admin handler with the kit-source API mounted on a
// real gitsource.Fetcher built, as serve builds it, from a kits.git entry
// whose cred resolves to token. It returns the admin URL and the admin log.
func kitGitJam(t *testing.T, store jam.Store, repoURL, token string) (string, *bytes.Buffer) {
	t.Helper()
	return gitJam(t, store, repoURL, token, func(f *gitsource.Fetcher, log *slog.Logger) jam.AdminOption {
		return jam.WithKitFetcher(store, f, log)
	})
}

// gitJam is kitGitJam with the mounted API chosen by mount.
func gitJam(t *testing.T, store jam.Store, repoURL, token string, mount func(*gitsource.Fetcher, *slog.Logger) jam.AdminOption) (string, *bytes.Buffer) {
	t.Helper()
	prefix := repoURL[:strings.LastIndex(repoURL, "/")+1]
	cfg, err := parseServeConfig([]byte("credentials:\n  kit-git:\nkits:\n  git:\n    - url: " + prefix + "\n      cred: kit-git\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateKits(); err != nil {
		t.Fatal(err)
	}
	f, err := resolveKitFetcher(cfg, map[string]secret.Spec{"kit-git": {Name: "kit-git", Value: token, Literal: true}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil,
		mount(f, log))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL, &logs
}

// runKitAt runs `at-jam kit <sub> --admin-url adminURL <args...>` and returns
// its exit code, stdout and stderr.
func runKitAt(adminURL, sub string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(append([]string{"kit", sub, "--admin-url", adminURL}, args...), func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

// TestKitGitEndToEnd drives the kit git verbs through run(...) against a real
// admin handler, a real Fetcher and a real git http-backend behind Basic
// auth: track, show, sync to a new commit, sync again (unchanged), versions,
// and a wrong token whose value never appears in any output.
func TestKitGitEndToEnd(t *testing.T) {
	const token = "s3cret-kit-token-e2e"
	repo, first, update := gittest.Serve(t, map[string]string{
		"web/kit.yml":       "kind: studio\nnotes:\n  - name: guide.md\n    read-when: always\n    file: docs/guide.md\n",
		"web/Dockerfile":    "FROM ${COVE_BASE_IMAGE}\nRUN true\n",
		"web/docs/guide.md": "the first guide",
	}, gittest.WithAuth("git", token))
	store := jam.NewMemStore()
	admin, logs := kitGitJam(t, store, repo, token)
	src := repo + "//web@main"

	code, out, errb := runKitAt(admin, "track", "web", "--git", src)
	if want := "imported web v1 from " + src + " (" + first[:12] + ")\ntracking " + src + "\n"; code != 0 || out != want {
		t.Fatalf("track: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}

	code, out, errb = runKitAt(admin, "show", "web")
	header := "# source: " + src + " (" + first[:12] + ")\n# tracking: " + src + " (last synced " + first[:12] + ")\n"
	if code != 0 || !strings.HasPrefix(out, header) {
		t.Fatalf("show: code=%d stderr=%q\n%s", code, errb, out)
	}
	if !strings.Contains(out, "body: the first guide") || !strings.Contains(out, "  Dockerfile\n") {
		t.Errorf("show: inlined note or file list missing:\n%s", out)
	}

	second := update(map[string]string{"web/docs/guide.md": "the second guide"})
	code, out, errb = runKitAt(admin, "sync", "web")
	if want := "synced web v2 (" + second[:12] + ")\n"; code != 0 || out != want {
		t.Fatalf("sync: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}
	code, out, errb = runKitAt(admin, "sync", "web")
	if want := "web unchanged (" + second[:12] + ")\n"; code != 0 || out != want {
		t.Fatalf("sync again: code=%d out=%q stderr=%q, want %q", code, out, errb, want)
	}

	code, out, errb = runKitAt(admin, "versions", "web")
	if code != 0 {
		t.Fatalf("versions: code=%d stderr=%q", code, errb)
	}
	rows := strings.Split(strings.TrimSpace(out), "\n")
	if len(rows) != 3 {
		t.Fatalf("versions: want a header and two rows:\n%s", out)
	}
	for i, want := range []struct{ version, current, commit string }{{"v1", "", first}, {"v2", "*", second}} {
		f := strings.Fields(rows[i+1])
		got := strings.Join(append(f[:1:1], f[len(f)-2:]...), " ")
		if exp := want.version + " " + src + " " + want.commit[:12]; got != exp || (want.current == "*") != strings.Contains(rows[i+1], " * ") {
			t.Errorf("versions row %d = %q, want %q (current %q)", i+1, rows[i+1], exp, want.current)
		}
	}

	// A wrong token: the auth error names the cred, and neither the wrong
	// token nor the real one reaches the CLI's output or Jam's log.
	const wrong = "wr0ng-kit-token-e2e"
	badAdmin, badLogs := kitGitJam(t, jam.NewMemStore(), repo, wrong)
	code, out, errb = runKitAt(badAdmin, "import", "--git", src)
	if code != 1 || !strings.Contains(errb, "auth failed") || !strings.Contains(errb, "kits.git cred kit-git") {
		t.Errorf("wrong token: code=%d out=%q stderr=%q, want an auth failure naming kit-git", code, out, errb)
	}
	for name, text := range map[string]string{"stdout": out, "stderr": errb, "log": badLogs.String(), "good log": logs.String()} {
		for _, tok := range []string{wrong, token} {
			if strings.Contains(text, tok) {
				t.Errorf("%s holds a token:\n%s", name, text)
			}
		}
	}
}
