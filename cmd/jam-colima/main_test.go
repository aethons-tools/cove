package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
)

func noEnv(string) string { return "" }

func TestRunBadConfigExits2(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yml")
	if code := run([]string{"run", "--config", missing}, noEnv, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2; stderr=%q", code, errb.String())
	}
	if !strings.HasPrefix(errb.String(), "jam-colima run:") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, noEnv, &out, &errb); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "jam-colima dev") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestColimaWired(t *testing.T) {
	var out, errb bytes.Buffer
	run([]string{"colima"}, noEnv, &out, &errb)
	want := "jam-colima colima: expected setup-docker|check-docker"
	if !strings.Contains(out.String()+errb.String(), want) {
		t.Fatalf("out=%q err=%q", out.String(), errb.String())
	}
}

// run moves the config dir jam-colima used, then loads from it.
func TestRunMovesTheOldConfigDir(t *testing.T) {
	x := t.TempDir()
	old := filepath.Join(x, legacynames.StudioManager)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.yml"), []byte("colima:\n  docker-context: colima\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return x
		}
		return ""
	}
	var out, errb bytes.Buffer
	code := run([]string{"run"}, getenv, &out, &errb)
	moved := "jam-colima run: moved config dir " + old + " to " + filepath.Join(x, "jam-colima")
	if code != 2 || !strings.Contains(errb.String(), moved) || !strings.Contains(errb.String(), "colima.docker-context is replaced by colima.profile") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(x, "jam-colima", "config.yml")); err != nil {
		t.Fatalf("config not moved: %v", err)
	}
}

// colima verbs read the profile from the default config when it exists.
func TestColimaReadsTheDefaultConfigProfile(t *testing.T) {
	x, colimaHome := t.TempDir(), t.TempDir()
	dir := filepath.Join(x, "jam-colima")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "name: m1\njam-addr: jam.example.com:443\ncolima:\n  runtime-addr: jam.example.com:443\n  jam-host: jam.example.com\n  profile: work\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": x, "COLIMA_HOME": colimaHome}[k]
	}
	var out, errb bytes.Buffer
	code := run([]string{"colima", "setup-docker"}, getenv, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), filepath.Join(colimaHome, "work", "colima.yaml")) || !strings.Contains(errb.String(), "colima start --profile work") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

// An explicit --config inside the moved dir follows it, with a notice to
// update the service's --config.
func TestRunFollowsAnExplicitConfigIntoTheMovedDir(t *testing.T) {
	x := t.TempDir()
	old := filepath.Join(x, legacynames.StudioManager)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.yml"), []byte("name: m1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string { return map[string]string{"XDG_CONFIG_HOME": x}[k] }
	oldCfg, newCfg := filepath.Join(old, "config.yml"), filepath.Join(x, "jam-colima", "config.yml")
	for i := 0; i < 2; i++ { // the run that moves it, and a later one
		var out, errb bytes.Buffer
		code := run([]string{"run", "--config", oldCfg}, getenv, &out, &errb)
		notice := "jam-colima run: --config " + oldCfg + " is now " + newCfg + " (its dir moved); update the service's --config"
		if code != 2 || !strings.Contains(errb.String(), notice) || !strings.Contains(errb.String(), newCfg+": jam-addr is required") {
			t.Fatalf("run %d: code=%d stderr=%q", i, code, errb.String())
		}
	}
}

// The colima verbs move the old dir too, so before the first run they still
// read the manager's profile.
func TestColimaMovesTheOldConfigDir(t *testing.T) {
	x, colimaHome := t.TempDir(), t.TempDir()
	old := filepath.Join(x, legacynames.StudioManager)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "name: m1\njam-addr: jam.example.com:443\ncolima:\n  runtime-addr: jam.example.com:443\n  jam-host: jam.example.com\n  profile: work\n"
	if err := os.WriteFile(filepath.Join(old, "config.yml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": x, "COLIMA_HOME": colimaHome}[k]
	}
	var out, errb bytes.Buffer
	code := run([]string{"colima", "setup-docker"}, getenv, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "jam-colima colima: moved config dir "+old) ||
		!strings.Contains(out.String(), "colima profile work (colima.profile in "+filepath.Join(x, "jam-colima", "config.yml")+")") ||
		!strings.Contains(errb.String(), filepath.Join(colimaHome, "work", "colima.yaml")) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
}
