package studiomanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	d := "/h/.config/at-studio-manager"
	if c.TokenFile != d+"/token" || c.Colima.IdentityFile != d+"/id_ed25519" ||
		c.Colima.KnownHostsDir != d+"/known_hosts.d" || c.LogLevel != "info" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no name", strings.Replace(minimal, "name: mac1\n", "", 1), "name"},
		{"local", strings.Replace(minimal, "mac1", "local", 1), "name"},
		{"bad name", strings.Replace(minimal, "mac1", "Bad_Name", 1), "name"},
		{"no jam-addr", strings.Replace(minimal, "jam-addr: jam.example.com:8443\n", "", 1), "jam-addr"},
		{"no port", strings.Replace(minimal, "jam.example.com:8443", "jam.example.com", 1), "jam-addr"},
		{"capacity", minimal + "capacity: -1\n", "capacity"},
		{"no runtime-addr", strings.Replace(minimal, "  runtime-addr: 10.0.0.1:8443\n", "", 1), "runtime-addr"},
		{"no jam-host", strings.Replace(minimal, "  jam-host: jam.example.com\n", "", 1), "jam-host"},
		{"docker-context", minimal + "  docker-context: \"a b\"\n", "docker-context"},
		{"log-level", minimal + "log-level: loud\n", "log-level"},
		{"unknown key", minimal + "colour: red\n", "colour"},
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
	os.WriteFile(p, []byte("tok123\n"), 0o600)
	if got, err := (Config{TokenFile: p}).Token(); err != nil || got != "tok123" {
		t.Fatalf("got %q, %v", got, err)
	}
	os.WriteFile(p, []byte(" \n\t"), 0o600)
	if _, err := (Config{TokenFile: p}).Token(); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := (Config{TokenFile: filepath.Join(dir, "none")}).Token(); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestDefaultDir(t *testing.T) {
	if got := DefaultDir(env(map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"})); got != "/x/at-studio-manager" {
		t.Fatal(got)
	}
	if got := DefaultDir(env(map[string]string{"HOME": "/h"})); got != "/h/.config/at-studio-manager" {
		t.Fatal(got)
	}
}
