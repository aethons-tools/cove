package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/logging"
)

// Harbor → Jam deprecation aliases for the at-jam binary itself. See
// docs/usage/jam/renamed-from-harbor.md.

func TestInvokedAsAtHarborWarnsOnceAndRuns(t *testing.T) {
	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runAs("at-harbor", []string{"version"}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "at-jam ") {
		t.Fatalf("stdout = %q, want the normal version line", out.String())
	}
	if strings.Count(errb.String(), "\n") != 1 || !strings.Contains(errb.String(), "at-harbor") || !strings.Contains(errb.String(), "at-jam") || !strings.Contains(errb.String(), logging.RenameDoc) {
		t.Fatalf("want one deprecation warning naming at-harbor → at-jam and the rename doc; stderr=%q", errb.String())
	}
}

func TestInvokedAsAtJamDoesNotWarn(t *testing.T) {
	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runAs("at-jam", []string{"version"}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("at-jam must not warn; stderr=%q", errb.String())
	}
}

func TestConfigDirMigratesFromAtHarborOnce(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	old := filepath.Join(xdg, "at-harbor")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "settings.yml"), []byte("default:\n  admin-url: http://old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "default-admin-token.json"), []byte(`{"access_token":"T"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var errb bytes.Buffer
	migrateConfigDir(&errb)

	if configDir() != filepath.Join(xdg, "at-jam") {
		t.Fatalf("configDir = %s, want …/at-jam", configDir())
	}
	if s := loadSettings("default"); s.AdminURL != "http://old" {
		t.Fatalf("settings not carried across: %+v", s)
	}
	info, err := os.Stat(filepath.Join(xdg, "at-jam", "default-admin-token.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token copy mode = %v err=%v, want 0600", info, err)
	}
	if _, err := os.Stat(filepath.Join(old, "settings.yml")); err != nil {
		t.Fatal("the old directory must be left in place")
	}
	if !strings.Contains(errb.String(), "at-harbor") || !strings.Contains(errb.String(), "at-jam") {
		t.Fatalf("want a notice naming both dirs; stderr=%q", errb.String())
	}
	if strings.Contains(errb.String(), `"access_token"`) || strings.Contains(errb.String(), `T"`) {
		t.Fatalf("notice must not log file contents; stderr=%q", errb.String())
	}

	// A second run: the new dir exists, so nothing is copied again.
	if err := os.WriteFile(filepath.Join(old, "settings.yml"), []byte("default:\n  admin-url: http://changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	migrateConfigDir(&errb)
	if s := loadSettings("default"); s.AdminURL != "http://old" {
		t.Fatalf("with both present the new dir must win untouched; got %+v", s)
	}
	if errb.Len() != 0 {
		t.Fatalf("no notice once migrated; stderr=%q", errb.String())
	}
}

func TestConfigDirNoOldNoCopy(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	var errb bytes.Buffer
	migrateConfigDir(&errb)
	if _, err := os.Stat(filepath.Join(xdg, "at-jam")); !os.IsNotExist(err) {
		t.Fatalf("nothing to migrate must not create at-jam/: %v", err)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr=%q", errb.String())
	}
}

func adminTokenServer(t *testing.T, got *string) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestAdminTokenNewEnvWins(t *testing.T) {
	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AT_JAM_ADMIN_TOKEN", "NEW-TOK")
	t.Setenv("AT_HARBOR_ADMIN_TOKEN", "OLD-TOK")
	var gotAuth string
	ts := adminTokenServer(t, &gotAuth)
	var out, errb bytes.Buffer
	if code := run([]string{"destination", "list", "--admin-url", ts.URL}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if gotAuth != "Bearer NEW-TOK" {
		t.Fatalf("Authorization = %q, want the AT_JAM_ADMIN_TOKEN value", gotAuth)
	}
	if strings.Contains(errb.String(), "deprecated") {
		t.Fatalf("the new variable must not warn; stderr=%q", errb.String())
	}
}

func TestAdminTokenOldEnvAloneWorksAndWarns(t *testing.T) {
	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AT_JAM_ADMIN_TOKEN", "")
	t.Setenv("AT_HARBOR_ADMIN_TOKEN", "OLD-TOK")
	var gotAuth string
	ts := adminTokenServer(t, &gotAuth)
	var out, errb bytes.Buffer
	if code := run([]string{"destination", "list", "--admin-url", ts.URL}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if gotAuth != "Bearer OLD-TOK" {
		t.Fatalf("Authorization = %q, want the AT_HARBOR_ADMIN_TOKEN fallback", gotAuth)
	}
	if !strings.Contains(errb.String(), "AT_HARBOR_ADMIN_TOKEN") || !strings.Contains(errb.String(), "AT_JAM_ADMIN_TOKEN") {
		t.Fatalf("want a deprecation warning naming both; stderr=%q", errb.String())
	}
	if strings.Contains(errb.String(), "OLD-TOK") {
		t.Fatalf("the token value must never be logged; stderr=%q", errb.String())
	}
}

// runtime.launcher.harbor-host is the deprecated name for jam-host.
func TestServeConfigHarborHostAlias(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  launcher:\n    jam-host: j.example\n"))
	if err != nil || c.Runtime.Launcher.JamHost != "j.example" || len(c.deprecated) != 0 {
		t.Fatalf("jam-host: cfg=%+v deprecated=%v err=%v", c.Runtime.Launcher, c.deprecated, err)
	}

	c, err = parseServeConfig([]byte("runtime:\n  launcher:\n    harbor-host: h.example\n"))
	if err != nil {
		t.Fatalf("harbor-host must still parse: %v", err)
	}
	if c.Runtime.Launcher.JamHost != "h.example" || c.Runtime.Launcher.DeprecatedHarborHost != "" {
		t.Fatalf("harbor-host must fold into JamHost: %+v", c.Runtime.Launcher)
	}
	if len(c.deprecated) != 1 || c.deprecated[0] != [2]string{"runtime.launcher.harbor-host", "runtime.launcher.jam-host"} {
		t.Fatalf("deprecated = %v", c.deprecated)
	}

	if _, err := parseServeConfig([]byte("runtime:\n  launcher:\n    jam-host: a\n    harbor-host: b\n")); err == nil ||
		!strings.Contains(err.Error(), "jam-host") || !strings.Contains(err.Error(), "harbor-host") {
		t.Fatalf("both present must be an error naming both; got %v", err)
	}
}

// serve logs one deprecation warning per aliased key, before anything else.
func TestServeWarnsOnDeprecatedKeys(t *testing.T) {
	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	cfg := filepath.Join(t.TempDir(), "jam.yml")
	// The launcher block is incomplete, so serve fails fast right after the
	// warnings — no listeners are started.
	if err := os.WriteFile(cfg, []byte("runtime:\n  launcher:\n    harbor-host: h.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"serve", "--config", cfg}, os.Getenv, &out, &errb); code == 0 {
		t.Fatal("an incomplete launcher block must fail")
	}
	if !strings.Contains(errb.String(), "runtime.launcher.harbor-host") || !strings.Contains(errb.String(), "runtime.launcher.jam-host") {
		t.Fatalf("want a deprecation warning for harbor-host; stderr=%q", errb.String())
	}
}

// runtime.dispatcher is the deprecated name for runtime.requisitioner.
func TestServeConfigDispatcherAlias(t *testing.T) {
	const block = "    role: implementer\n    max-concurrent: 2\n    wait-max: 24h\n"
	c, err := parseServeConfig([]byte("runtime:\n  requisitioner:\n" + block))
	if err != nil || c.Runtime.Requisitioner == nil || c.Runtime.Requisitioner.Role != "implementer" || len(c.deprecated) != 0 {
		t.Fatalf("requisitioner: cfg=%+v deprecated=%v err=%v", c.Runtime.Requisitioner, c.deprecated, err)
	}

	c, err = parseServeConfig([]byte("runtime:\n  dispatcher:\n" + block))
	if err != nil {
		t.Fatalf("dispatcher must still parse: %v", err)
	}
	if c.Runtime.Requisitioner == nil || c.Runtime.Requisitioner.MaxConcurrent != 2 || c.Runtime.DeprecatedDispatcher != nil {
		t.Fatalf("dispatcher must fold into Requisitioner: %+v", c.Runtime)
	}
	if len(c.deprecated) != 1 || c.deprecated[0] != [2]string{"runtime.dispatcher", "runtime.requisitioner"} {
		t.Fatalf("deprecated = %v", c.deprecated)
	}
	// The runtime.wake fallback reads the aliased block too.
	if got := c.wakeSettings(); got.MaxWait != 24*time.Hour {
		t.Fatalf("wake fallback through the dispatcher alias = %+v", got)
	}
	// Both keys are recognized (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  dispatcher:\n    role: r\n  requisitioner:\n    role: r\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}

	if _, err := parseServeConfig([]byte("runtime:\n  requisitioner:\n" + block + "  dispatcher:\n" + block)); err == nil ||
		!strings.Contains(err.Error(), "requisitioner") || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("both present must be an error naming both; got %v", err)
	}
}

// `at-jam cove …` is the deprecated alias of `at-jam studio …`.
func TestCoveVerbIsADeprecatedAliasForStudio(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	logging.ResetDeprecations()
	t.Cleanup(logging.ResetDeprecations)
	var out, errb bytes.Buffer
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio list: exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "deprecated") {
		t.Fatalf("studio must not warn; stderr=%q", errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove list (alias): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), `at-jam cove`) || !strings.Contains(errb.String(), `at-jam studio`) {
		t.Fatalf("want a deprecation warning naming at-jam cove → at-jam studio; stderr=%q", errb.String())
	}

	// A usage error under the alias still names the new verb.
	errb.Reset()
	if code := run([]string{"cove"}, getenv, &out, &errb); code != 2 || !strings.Contains(errb.String(), "at-jam studio: expected raise|list|status|teardown") {
		t.Fatalf("bare cove: exit=%d stderr=%q", code, errb.String())
	}
}

func TestHelpListsStudioNotCove(t *testing.T) {
	var out, errb bytes.Buffer
	run([]string{"help"}, func(string) string { return "" }, &out, &errb)
	help := out.String()
	if !strings.Contains(help, "studio") || !strings.Contains(help, "manage studios") {
		t.Fatalf("help must list the studio verb; got:\n%s", help)
	}
	if !strings.Contains(help, "deprecated alias for studio") {
		t.Fatalf("help should mark cove as a deprecated alias; got:\n%s", help)
	}
}
