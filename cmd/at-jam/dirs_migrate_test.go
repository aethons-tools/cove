package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/legacynames"
)

func writeFile(t *testing.T, path, data string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil { // whatever the umask
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string, perm os.FileMode) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if fi.Mode().Perm() != perm {
		t.Fatalf("%s mode %v, want %v", path, fi.Mode().Perm(), perm)
	}
	b, _ := os.ReadFile(path)
	return string(b)
}

// One jam run copies the pre-rename config, state and launcher directories to
// their new homes (modes kept, old ones left, contents never logged); a second
// run copies nothing.
func TestMigrateDirsCopiesConfigStateAndLauncher(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	oldCfg, oldLaunch, oldState := filepath.Join(cfg, legacynames.ConfigDir), filepath.Join(cfg, legacynames.LauncherDir), filepath.Join(state, legacynames.StateDir)
	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "token: SECRET-VALUE\n", 0o600)
	writeFile(t, filepath.Join(oldCfg, "ssh", "s1-cert.pub"), "cert", 0o600)
	writeFile(t, filepath.Join(oldLaunch, "id_ed25519"), "PRIVATE-KEY", 0o600)
	writeFile(t, filepath.Join(oldLaunch, "id_ed25519.pub"), "ssh-ed25519 AAAA", 0o644)
	writeFile(t, filepath.Join(oldLaunch, "known_hosts.d", "x"), "kh", 0o600)
	writeFile(t, filepath.Join(oldState, "relay.cursor"), "42", 0o600)

	var log bytes.Buffer
	migrateDirs(&log)

	jam := filepath.Join(cfg, "jam")
	if got := mustRead(t, filepath.Join(jam, "settings.yml"), 0o600); got != "token: SECRET-VALUE\n" {
		t.Fatalf("settings = %q", got)
	}
	mustRead(t, filepath.Join(jam, "ssh", "s1-cert.pub"), 0o600)
	mustRead(t, filepath.Join(jam, "id_ed25519"), 0o600)
	mustRead(t, filepath.Join(jam, "id_ed25519.pub"), 0o644)
	mustRead(t, filepath.Join(jam, "known_hosts.d", "x"), 0o600)
	mustRead(t, filepath.Join(state, "jam", "relay.cursor"), 0o600)
	for _, old := range []string{oldCfg, oldLaunch, oldState} {
		if _, err := os.Stat(old); err != nil {
			t.Fatalf("old %s must be left in place: %v", old, err)
		}
	}
	if s := log.String(); !strings.Contains(s, "copied a pre-rename directory") || strings.Contains(s, "SECRET-VALUE") || strings.Contains(s, "PRIVATE-KEY") {
		t.Fatalf("log = %q", s)
	}
	for _, d := range []string{jam, filepath.Join(jam, "ssh"), filepath.Join(jam, "known_hosts.d"), filepath.Join(state, "jam")} {
		if left, _ := filepath.Glob(filepath.Join(d, migrateTempPrefix+"*")); len(left) != 0 {
			t.Fatalf("temp files left behind: %v", left)
		}
	}

	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "token: CHANGED\n", 0o600)
	log.Reset()
	migrateDirs(&log)
	if got := mustRead(t, filepath.Join(jam, "settings.yml"), 0o600); got != "token: SECRET-VALUE\n" || log.Len() != 0 {
		t.Fatalf("second run copied again: settings %q, log %q", got, log.String())
	}
}

// A Jam host with only the launcher directory gets its key under ~/.config/jam
// (before serve's keys.EnsureFile could mint a fresh one); an existing new key
// is never overwritten.
func TestMigrateDirsLauncherOnlyAndNewWins(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeFile(t, filepath.Join(cfg, legacynames.LauncherDir, "id_ed25519"), "OLD", 0o600)
	migrateDirs(&bytes.Buffer{})
	if got := mustRead(t, filepath.Join(cfg, "jam", "id_ed25519"), 0o600); got != "OLD" {
		t.Fatalf("key = %q", got)
	}
	writeFile(t, filepath.Join(cfg, "jam", "id_ed25519"), "NEW", 0o600)
	migrateDirs(&bytes.Buffer{})
	if got := mustRead(t, filepath.Join(cfg, "jam", "id_ed25519"), 0o600); got != "NEW" {
		t.Fatalf("an existing new key was overwritten: %q", got)
	}
}

// A new file already in place before the first copy (say a credentials.yml
// put there by hand) is kept; the copy only fills in what is missing.
func TestMigrateDirsNeverOverwritesANewFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeFile(t, filepath.Join(cfg, legacynames.LauncherDir, "id_ed25519"), "OLD", 0o600)
	writeFile(t, filepath.Join(cfg, legacynames.ConfigDir, "settings.yml"), "old: 1\n", 0o600)
	writeFile(t, filepath.Join(cfg, "jam", "id_ed25519"), "NEW", 0o600)
	writeFile(t, filepath.Join(cfg, "jam", "settings.yml"), "new: 1\n", 0o600)
	migrateDirs(&bytes.Buffer{})
	if got := mustRead(t, filepath.Join(cfg, "jam", "id_ed25519"), 0o600); got != "NEW" {
		t.Fatalf("key = %q", got)
	}
	if got := mustRead(t, filepath.Join(cfg, "jam", "settings.yml"), 0o600); got != "new: 1\n" {
		t.Fatalf("settings = %q", got)
	}
}

// A copy cut short (no completion marker yet, a temp file left behind) is
// finished by the next run: missing files are copied, the stray temp file is
// removed, and only then is the target marked complete.
func TestMigrateDirsRecopiesAPartialTarget(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldCfg, jam := filepath.Join(cfg, legacynames.ConfigDir), filepath.Join(cfg, "jam")
	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "a: 1\n", 0o600)
	writeFile(t, filepath.Join(oldCfg, "default-admin-token.json"), "{}", 0o600)
	// The interrupted run: one file copied, one temp file, no marker.
	writeFile(t, filepath.Join(jam, "settings.yml"), "a: 1\n", 0o600)
	stray := filepath.Join(jam, migrateTempPrefix+"123")
	writeFile(t, stray, "{", 0o600)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	migrateDirs(&log)
	if got := mustRead(t, filepath.Join(jam, "default-admin-token.json"), 0o600); got != "{}" {
		t.Fatalf("token = %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(jam, migrateTempPrefix+"*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	if !strings.Contains(log.String(), "copied a pre-rename directory") {
		t.Fatalf("log = %q", log.String())
	}
	log.Reset()
	migrateDirs(&log)
	if log.Len() != 0 {
		t.Fatalf("a completed target was copied again: %q", log.String())
	}
}

// A copy that fails leaves no completion marker, so the next run tries again.
func TestMigrateDirsFailedCopyRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bad := filepath.Join(cfg, legacynames.ConfigDir, "unreadable")
	writeFile(t, bad, "x", 0o000)
	var log bytes.Buffer
	migrateDirs(&log)
	if !strings.Contains(log.String(), "could not copy a pre-rename directory") {
		t.Fatalf("log = %q", log.String())
	}
	if err := os.Chmod(bad, 0o600); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	migrateDirs(&log)
	if got := mustRead(t, filepath.Join(cfg, "jam", "unreadable"), 0o600); got != "x" || !strings.Contains(log.String(), "copied a pre-rename directory") {
		t.Fatalf("retry: file %q, log %q", got, log.String())
	}
}

// Every jam invocation copies the launcher key across before the command runs,
// so serve's launcherPublicKey (keys.EnsureFile) at the default identity-file
// finds the carried-over key and does not mint a fresh one.
func TestRunAsCopiesTheLauncherKeyBeforeServeCouldMintOne(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeFile(t, filepath.Join(cfg, legacynames.LauncherDir, "id_ed25519"), "OLD-PRIVATE", 0o600)
	writeFile(t, filepath.Join(cfg, legacynames.LauncherDir, "id_ed25519.pub"), "ssh-ed25519 AAAA carried\n", 0o644)
	var out, errb bytes.Buffer
	if code := runAs("jam", []string{"version"}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	c, err := parseServeConfig([]byte("runtime:\n  launcher:\n    runtime-addr: jam.example.com:443\n    jam-host: jam.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateLauncher(); err != nil {
		t.Fatal(err)
	}
	pub, err := launcherPublicKey(c.Runtime.Launcher.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA carried\n" {
		t.Fatalf("pub = %q, want the carried-over key", pub)
	}
	if got := mustRead(t, c.Runtime.Launcher.IdentityFile, 0o600); got != "OLD-PRIVATE" {
		t.Fatalf("private key = %q", got)
	}
}

func TestLauncherDefaultsUnderJamConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	c, err := parseServeConfig([]byte("runtime:\n  launcher:\n    runtime-addr: jam.example.com:443\n    jam-host: jam.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateLauncher(); err != nil {
		t.Fatal(err)
	}
	lc := c.Runtime.Launcher
	if lc.IdentityFile != filepath.Join(cfg, "jam", "id_ed25519") || lc.KnownHostsDir != filepath.Join(cfg, "jam", "known_hosts.d") {
		t.Fatalf("launcher defaults = %q, %q", lc.IdentityFile, lc.KnownHostsDir)
	}
}

func TestStateDirIsJam(t *testing.T) {
	s := t.TempDir()
	t.Setenv("XDG_STATE_HOME", s)
	if got, err := jamStateDir(); err != nil || got != filepath.Join(s, "jam") {
		t.Fatalf("jamStateDir = %q, %v", got, err)
	}
}
