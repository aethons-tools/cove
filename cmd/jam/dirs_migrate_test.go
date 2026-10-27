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

// One jam run copies the pre-rename config and state directories to
// their new homes (modes kept, old ones left, contents never logged); a second
// run copies nothing.
func TestMigrateDirsCopiesConfigAndState(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	oldCfg, oldState := filepath.Join(cfg, legacynames.ConfigDir), filepath.Join(state, legacynames.StateDir)
	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "token: SECRET-VALUE\n", 0o600)
	writeFile(t, filepath.Join(oldCfg, "ssh", "s1-cert.pub"), "cert", 0o600)
	writeFile(t, filepath.Join(oldCfg, "known_hosts.d", "x"), "kh", 0o600)
	writeFile(t, filepath.Join(oldState, "relay.cursor"), "42", 0o600)

	var log bytes.Buffer
	migrateDirs(&log)

	jam := filepath.Join(cfg, "jam")
	if got := mustRead(t, filepath.Join(jam, "settings.yml"), 0o600); got != "token: SECRET-VALUE\n" {
		t.Fatalf("settings = %q", got)
	}
	mustRead(t, filepath.Join(jam, "ssh", "s1-cert.pub"), 0o600)
	mustRead(t, filepath.Join(jam, "known_hosts.d", "x"), 0o600)
	mustRead(t, filepath.Join(state, "jam", "relay.cursor"), 0o600)
	for _, old := range []string{oldCfg, oldState} {
		if _, err := os.Stat(old); err != nil {
			t.Fatalf("old %s must be left in place: %v", old, err)
		}
	}
	if s := log.String(); !strings.Contains(s, "copied a pre-rename directory") || strings.Contains(s, "SECRET-VALUE") {
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

// A new file already in place before the first copy (say a credentials.yml
// put there by hand) is kept; the copy only fills in what is missing.
func TestMigrateDirsNeverOverwritesANewFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeFile(t, filepath.Join(cfg, legacynames.ConfigDir, "settings.yml"), "old: 1\n", 0o600)
	writeFile(t, filepath.Join(cfg, "jam", "settings.yml"), "new: 1\n", 0o600)
	migrateDirs(&bytes.Buffer{})
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

func TestStateDirIsJam(t *testing.T) {
	s := t.TempDir()
	t.Setenv("XDG_STATE_HOME", s)
	if got, err := jamStateDir(); err != nil || got != filepath.Join(s, "jam") {
		t.Fatalf("jamStateDir = %q, %v", got, err)
	}
}

// Symlinks are carried as symlinks — a top-level one (to a file or a
// directory), a nested one, a dangling one — their targets verbatim, never
// their content; a path already at the new home is kept.
func TestMigrateDirsRecreatesSymlinks(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	oldCfg := filepath.Join(cfg, legacynames.ConfigDir)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "settings.yml"), "shared", 0o600)
	writeFile(t, filepath.Join(elsewhere, "tree", "f"), "tree-content", 0o600)
	writeFile(t, filepath.Join(oldCfg, "ssh", "real"), "r", 0o600)
	links := map[string]string{
		"settings.yml":       filepath.Join(elsewhere, "settings.yml"), // top level, to a file
		"shared":             filepath.Join(elsewhere, "tree"),         // top level, to a directory
		"ssh/alias":          "real",                                   // nested, relative
		"ssh/gone":           "../nowhere",                             // nested, dangling
		"tokens.yml":         "/does/not/exist",                        // top level, dangling
		"ssh/kept-new-entry": "real",
	}
	for p, target := range links {
		if err := os.Symlink(target, filepath.Join(oldCfg, p)); err != nil {
			t.Fatal(err)
		}
	}
	jam := filepath.Join(cfg, "jam")
	writeFile(t, filepath.Join(jam, "ssh", "kept-new-entry"), "new", 0o600)

	migrateDirs(&bytes.Buffer{})

	for p, target := range links {
		if p == "ssh/kept-new-entry" {
			continue
		}
		got, err := os.Readlink(filepath.Join(jam, p))
		if err != nil || got != target {
			t.Fatalf("%s: link %q, %v; want a symlink to %q", p, got, err, target)
		}
	}
	if got := mustRead(t, filepath.Join(jam, "ssh", "kept-new-entry"), 0o600); got != "new" {
		t.Fatalf("an entry already at the new home must be kept: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(jam, "shared", "f")); err != nil {
		t.Fatalf("the directory link resolves: %v", err)
	}
	if fi, _ := os.Lstat(filepath.Join(jam, "shared")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("a linked directory is linked, not copied")
	}
}

// Directories keep their modes: a 0700 tree stays 0700, at the top and nested.
func TestMigrateDirsKeepsDirectoryModes(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	oldCfg := filepath.Join(cfg, legacynames.ConfigDir)
	writeFile(t, filepath.Join(oldCfg, "ssh", "deep", "k"), "k", 0o600)
	for _, d := range []string{oldCfg, filepath.Join(oldCfg, "ssh"), filepath.Join(oldCfg, "ssh", "deep")} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	migrateDirs(&bytes.Buffer{})
	jam := filepath.Join(cfg, "jam")
	for _, d := range []string{jam, filepath.Join(jam, "ssh"), filepath.Join(jam, "ssh", "deep")} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want 0700", d, fi, err)
		}
	}
	if got := mustRead(t, filepath.Join(jam, "ssh", "deep", "k"), 0o600); got != "k" {
		t.Fatalf("k = %q", got)
	}
}
