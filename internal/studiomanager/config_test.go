package studiomanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/runner"
)

const minimal = "name: mac1\njam-addr: jam.example.com:8443\ncolima:\n  runtime-addr: 10.0.0.1:8443\n  jam-host: jam.example.com\n"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMinimal(t *testing.T) {
	getenv := env(map[string]string{"HOME": "/h"})
	c, err := Load(write(t, minimal), getenv)
	if err != nil {
		t.Fatal(err)
	}
	d := "/h/.config/jam-colima"
	if c.TokenFile != d+"/token" || c.Colima.IdentityFile != d+"/id_ed25519" ||
		c.Colima.KnownHostsDir != d+"/known_hosts.d" || c.LogLevel != "info" || c.Colima.Profile != "default" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no name", strings.Replace(minimal, "name: mac1\n", "", 1), "name is required"},
		{"local", strings.Replace(minimal, "mac1", "local", 1), "reserved"},
		{"bad name", strings.Replace(minimal, "mac1", "Bad_Name", 1), `invalid manager name "Bad_Name"`},
		{"no jam-addr", strings.Replace(minimal, "jam-addr: jam.example.com:8443\n", "", 1), "jam-addr is required"},
		{"no port", strings.Replace(minimal, "jam.example.com:8443", "jam.example.com", 1), `jam-addr "jam.example.com" must be host:port`},
		{"capacity", minimal + "capacity: -1\n", "capacity must not be negative"},
		{"no runtime-addr", strings.Replace(minimal, "  runtime-addr: 10.0.0.1:8443\n", "", 1), "colima.runtime-addr is required"},
		{"no jam-host", strings.Replace(minimal, "  jam-host: jam.example.com\n", "", 1), "colima.jam-host is required"},
		{"profile", minimal + "  profile: \"a b\"\n", `colima.profile "a b" is not a valid colima profile name`},
		{"profile slash", minimal + "  profile: ../x\n", `colima.profile "../x"`},
		{"docker-context", minimal + "  docker-context: colima-work\n", "colima.docker-context is replaced by colima.profile (the VM name; its docker context is colima-<profile>)"},
		{"log-level", minimal + "log-level: loud\n", `log-level "loud" must be`},
		{"unknown key", minimal + "colour: red\n", "field colour not found"},
		{"empty file", "", "name is required"},
		{"comment only", "# nothing\n", "name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body), env(map[string]string{"HOME": "/h"}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestTokenTrimsAndRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	if err := os.WriteFile(p, []byte("tok123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := (Config{TokenFile: p}).Token(); err != nil || got != "tok123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := os.WriteFile(p, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Config{TokenFile: p}).Token(); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := (Config{TokenFile: filepath.Join(dir, "none")}).Token(); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestDefaultDir(t *testing.T) {
	if got := DefaultDir(env(map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"})); got != "/x/jam-colima" {
		t.Fatal(got)
	}
	if got := DefaultDir(env(map[string]string{"HOME": "/h"})); got != "/h/.config/jam-colima" {
		t.Fatal(got)
	}
}

func TestDockerContextFollowsProfile(t *testing.T) {
	for _, c := range []struct{ profile, want string }{{"", "colima"}, {"default", "colima"}, {"work", "colima-work"}, {"jam.b", "colima-jam.b"}} {
		if got := (ColimaConfig{Profile: c.profile}).DockerContext(); got != c.want {
			t.Errorf("profile %q: context %q, want %q", c.profile, got, c.want)
		}
	}
	for in, want := range map[string]string{"work": "work", "colima-work": "work", "colima": "default"} {
		c, err := Load(write(t, minimal+"  profile: "+in+"\n"), env(map[string]string{"HOME": "/h"}))
		if err != nil || c.Colima.Profile != want || c.Colima.DockerContext() != colimacfg.ProfileContext(want) {
			t.Fatalf("profile %s: %+v, %v; want %s", in, c.Colima, err, want)
		}
	}
	if _, err := Load(write(t, minimal+"  profile: colima-\n"), env(map[string]string{"HOME": "/h"})); err == nil {
		t.Fatal("profile colima- (empty after the prefix) accepted")
	}
}

func TestMigrateDirMovesTheOldDir(t *testing.T) {
	x := t.TempDir()
	getenv := env(map[string]string{"XDG_CONFIG_HOME": x, "HOME": "/h"})
	old := filepath.Join(x, legacynames.StudioManager)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	from, err := MigrateDir(getenv)
	if err != nil || from != old {
		t.Fatalf("MigrateDir = %q, %v; want %q", from, err, old)
	}
	if b, err := os.ReadFile(filepath.Join(x, "jam-colima", "id_ed25519")); err != nil || string(b) != "key" {
		t.Fatalf("identity key not moved: %q, %v", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old dir still there: %v", err)
	}
	if from, err := MigrateDir(getenv); err != nil || from != "" {
		t.Fatalf("second MigrateDir = %q, %v; want a no-op", from, err)
	}
}

func TestMigrateDirKeepsAnExistingNewDir(t *testing.T) {
	x := t.TempDir()
	getenv := env(map[string]string{"XDG_CONFIG_HOME": x})
	for _, d := range []string{legacynames.StudioManager, "jam-colima"} {
		if err := os.MkdirAll(filepath.Join(x, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if from, err := MigrateDir(getenv); err != nil || from != "" {
		t.Fatalf("MigrateDir = %q, %v; want a no-op", from, err)
	}
	if _, err := os.Stat(filepath.Join(x, legacynames.StudioManager)); err != nil {
		t.Fatalf("old dir must be left alone when the new one exists: %v", err)
	}
}

func TestMigrateDirNothingToMove(t *testing.T) {
	x := t.TempDir()
	if from, err := MigrateDir(env(map[string]string{"XDG_CONFIG_HOME": x})); err != nil || from != "" {
		t.Fatalf("MigrateDir = %q, %v", from, err)
	}
	if _, err := os.Stat(filepath.Join(x, "jam-colima")); !os.IsNotExist(err) {
		t.Fatalf("MigrateDir must not create the new dir: %v", err)
	}
}

func TestColimaBackendPinsTheProfileContext(t *testing.T) {
	for profile, want := range map[string]string{"default": "--context colima ps", "work": "--context colima-work ps"} {
		got := strings.Join(colimaBackend(&runner.Fake{}, Config{Colima: ColimaConfig{Profile: profile}}).DockerArgs("ps"), " ")
		if got != want {
			t.Errorf("profile %s: %q, want %q", profile, got, want)
		}
	}
}

func TestMovedPath(t *testing.T) {
	x := t.TempDir()
	getenv := env(map[string]string{"XDG_CONFIG_HOME": x})
	old, dir := filepath.Join(x, legacynames.StudioManager), filepath.Join(x, "jam-colima")
	in := filepath.Join(old, "sub", "config.yml")
	// Neither dir: unchanged.
	if got, moved := MovedPath(getenv, in); moved || got != in {
		t.Fatalf("no dirs: %q %v", got, moved)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The old dir is gone and the new one exists: under the new dir.
	if got, moved := MovedPath(getenv, in); !moved || got != filepath.Join(dir, "sub", "config.yml") {
		t.Fatalf("moved: %q %v", got, moved)
	}
	// Outside the old dir (or a sibling sharing its prefix): unchanged.
	for _, p := range []string{"/etc/jam/config.yml", old + "-x/config.yml", filepath.Join(dir, "config.yml")} {
		if got, moved := MovedPath(getenv, p); moved || got != p {
			t.Errorf("%s: %q %v", p, got, moved)
		}
	}
	// The old dir still exists (not moved): unchanged.
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, moved := MovedPath(getenv, in); moved || got != in {
		t.Fatalf("old dir present: %q %v", got, moved)
	}
}
