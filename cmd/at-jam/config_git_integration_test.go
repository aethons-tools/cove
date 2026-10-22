//go:build integration

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

// configGitJam is kitGitJam with the config API mounted instead: the real
// admin handler on a real Fetcher built, as serve builds it, from a kits.git
// entry for repoURL's server whose cred resolves to token.
func configGitJam(t *testing.T, store jam.Store, repoURL, token string) (string, *bytes.Buffer) {
	t.Helper()
	return gitJam(t, store, repoURL, token, func(f *gitsource.Fetcher, log *slog.Logger) jam.AdminOption {
		return jam.WithConfig(store, f, jam.ConfigEnv{CredExists: func(string) bool { return true }}, log)
	})
}

// TestConfigGitEndToEnd: track and sync a private config repo through the
// real fetcher behind Basic auth, with its kit-source kit; a wrong token and
// a pasted credential URL never reach any output or log.
func TestConfigGitEndToEnd(t *testing.T) {
	const token = "s3cret-config-token-e2e"
	kitsURL, _, _ := gittest.Serve(t, map[string]string{"api/kit.yml": "kind: studio\nprompt: api\n"})
	cfgURL, first, _ := gittest.Serve(t, map[string]string{
		"users/bob.yml":       "kind: user\nversion: 1\n",
		"kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: " + kitsURL + "//api@main\n",
	}, gittest.WithAuth("git", token))
	st := jam.NewMemStore()
	url, logs := configGitJam(t, st, cfgURL, token)
	src := cfgURL + "@main"
	if code, out, errs := runJam("config", "track", "--git", src, "--admin-url", url); code != 0 {
		t.Fatalf("track: %d\n%s\n%s", code, out, errs)
	}
	if code, out, errs := runJam("config", "sync", "--yes", "--admin-url", url); code != 0 || !strings.Contains(out, "applied: 2 created") {
		t.Fatalf("sync: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != first {
		t.Fatalf("record %+v", got)
	}
	k, _ := st.GetKit("api")
	if m := k.Meta[1].Source; m == nil || m.Repo != kitsURL || m.Dir != "api" || m.Commit == "" {
		t.Fatalf("provenance %+v", m)
	}

	wrongURL, wrongLogs := configGitJam(t, jam.NewMemStore(), cfgURL, "wrong-token")
	code, out, errs := runJam("config", "diff", "--git", src, "--admin-url", wrongURL)
	if code != 1 || !strings.Contains(errs, "auth failed") {
		t.Fatalf("wrong token: %d\n%s\n%s", code, out, errs)
	}
	pasted := strings.Replace(cfgURL, "://", "://x-access-token:"+token+"@", 1) + "@main"
	code, out2, errs2 := runJam("config", "track", "--git", pasted, "--admin-url", url)
	if code != 1 {
		t.Fatalf("pasted credential: %d", code)
	}
	for _, s := range []string{out, errs, out2, errs2, logs.String(), wrongLogs.String()} {
		if strings.Contains(s, token) || strings.Contains(s, "wrong-token") {
			t.Fatalf("a token leaked:\n%s", s)
		}
	}
}
