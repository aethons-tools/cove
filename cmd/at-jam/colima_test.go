package main

import (
	"bytes"
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
	got, err := colimaConfigPath(func(k string) string { return map[string]string{"HOME": "/h"}[k] })
	if err != nil || got != "/h/.colima/default/colima.yaml" {
		t.Fatalf("HOME fallback = %q, %v", got, err)
	}
	got, _ = colimaConfigPath(func(k string) string { return map[string]string{"HOME": "/h", "COLIMA_HOME": "/c"}[k] })
	if got != "/c/default/colima.yaml" {
		t.Fatalf("COLIMA_HOME = %q", got)
	}
	if _, err := colimaConfigPath(func(string) string { return "" }); err == nil {
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
	if code != 0 || !strings.Contains(out, "colima restart") || !strings.Contains(out, "won't upgrade") {
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
