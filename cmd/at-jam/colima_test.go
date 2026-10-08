package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/runner"
)

const freshColima = "cpu: 2\ndocker: {}\nprovision: []\n"

// colimaHome writes cfg as the default profile's colima.yaml under a temp
// COLIMA_HOME (mode 0600) and returns its getenv and the config path.
func colimaHome(t *testing.T, cfg string) (func(string) string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "default", "colima.yaml")
	if cfg != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func(k string) string {
		if k == "COLIMA_HOME" {
			return home
		}
		return ""
	}, path
}

func runColima(getenv func(string) string, r runner.Runner, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cmdColima(getenv, r)(args, cli.Globals{}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestColimaConfigPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	never := func(string) bool { return false }
	always := func(string) bool { return true }
	cases := []struct {
		name   string
		env    map[string]string
		exists func(string) bool
		want   string
	}{
		{"COLIMA_HOME wins", map[string]string{"HOME": "/h", "COLIMA_HOME": "/c", "XDG_CONFIG_HOME": "/x"}, always, "/c/default/colima.yaml"},
		{"~/.colima when it exists", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, func(p string) bool { return p == "/h/.colima" }, "/h/.colima/default/colima.yaml"},
		{"XDG default", map[string]string{"HOME": "/h"}, never, "/h/.config/colima/default/colima.yaml"},
		{"XDG_CONFIG_HOME override", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, never, "/x/colima/default/colima.yaml"},
	}
	for _, c := range cases {
		got, err := colimaConfigPath(env(c.env), c.exists)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := colimaConfigPath(env(nil), always); err == nil {
		t.Fatal("no HOME/COLIMA_HOME must error")
	}
}

func TestColimaSetupDockerMissingConfig(t *testing.T) {
	getenv, _ := colimaHome(t, "")
	code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 1 || !strings.Contains(errs, "run 'colima start' once") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaSetupDockerDryRunWritesNothing(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if !strings.Contains(out, "+       path: /usr/bin/sysbox-runc") || !strings.Contains(out, "- docker: {}") {
		t.Fatalf("dry-run must print a diff:\n%s", out)
	}
	if b, _ := os.ReadFile(path); string(b) != freshColima {
		t.Fatalf("dry-run wrote the config:\n%s", b)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a backup: %v", err)
	}
}

func TestColimaSetupDockerWritesThenNoOps(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 0 || !strings.Contains(out, "colima restart") || !strings.Contains(out, "won't upgrade") || !strings.Contains(out, "stop running coves first") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errs)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "sysbox-runc:") || !strings.Contains(string(b), "mode: system") {
		t.Fatalf("config not updated:\n%s", b)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != freshColima {
		t.Fatalf("backup = %q, want the original", bak)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 kept", info.Mode().Perm())
	}
	code, out, _ = runColima(getenv, &runner.Fake{}, "setup-docker")
	if code != 0 || !strings.Contains(out, "already set up") {
		t.Fatalf("second run must no-op: code=%d stdout=%q", code, out)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, b) {
		t.Fatal("second run changed the file")
	}
}

func TestColimaSetupDockerRejectsOldVersion(t *testing.T) {
	getenv, _ := colimaHome(t, freshColima)
	code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--sysbox-version", "0.6.9")
	if code != 2 || !strings.Contains(errs, "older than") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaCheckDocker(t *testing.T) {
	getenv := func(string) string { return "" }
	ok := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"sysbox-runc":{"path":"/usr/bin/sysbox-runc"}}`}}}
	if code, out, _ := runColima(getenv, ok, "check-docker"); code != 0 || !strings.Contains(out, "ok:") {
		t.Fatalf("present: code=%d out=%q", code, out)
	}
	absent := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{}}`}}}
	if code, _, errs := runColima(getenv, absent, "check-docker"); code != 1 || !strings.Contains(errs, "at-jam colima setup-docker") {
		t.Fatalf("absent: code=%d stderr=%q", code, errs)
	}
}

func TestColimaUnknownSubcommand(t *testing.T) {
	if code, _, _ := runColima(func(string) string { return "" }, &runner.Fake{}, "bogus"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestColimaRejectsStrayArgs(t *testing.T) {
	getenv, _ := colimaHome(t, freshColima)
	for _, sub := range []string{"setup-docker", "check-docker"} {
		code, _, errs := runColima(getenv, &runner.Fake{}, sub, "extra")
		if code != 2 || !strings.Contains(errs, "unexpected argument") {
			t.Errorf("%s: code=%d stderr=%q", sub, code, errs)
		}
	}
}

func TestColimaNoSubcommand(t *testing.T) {
	if code, _, errs := runColima(func(string) string { return "" }, &runner.Fake{}); code != 2 || errs == "" {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaGlobalDryRunWritesNothing(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	var out, errb bytes.Buffer
	code := cmdColima(getenv, &runner.Fake{})([]string{"setup-docker"}, cli.Globals{DryRun: true}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "would change") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	if b, _ := os.ReadFile(path); string(b) != freshColima {
		t.Fatalf("global --dry-run wrote the config:\n%s", b)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("global --dry-run wrote a backup: %v", err)
	}
}

func TestColimaCheckDockerUnreachable(t *testing.T) {
	r := &runner.Fake{Outputs: []runner.FakeResult{{Err: errors.New("cannot connect")}}}
	code, _, errs := runColima(func(string) string { return "" }, r, "check-docker")
	if code != 1 || !strings.Contains(errs, "colima start") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestColimaSetupDockerLeavesNoTempFiles(t *testing.T) {
	getenv, path := colimaHome(t, freshColima)
	if code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".colima.yaml.") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestColimaSetupDockerKeepsSymlink(t *testing.T) {
	getenv, path := colimaHome(t, "")
	target := filepath.Join(t.TempDir(), "dotfiles-colima.yaml")
	if err := os.WriteFile(target, []byte(freshColima), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runColima(getenv, &runner.Fake{}, "setup-docker"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file: %v %v", info, err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("target not updated:\n%s", b)
	}
	if bak, _ := os.ReadFile(target + ".bak"); string(bak) != freshColima {
		t.Fatalf("backup next to target = %q", bak)
	}
}
