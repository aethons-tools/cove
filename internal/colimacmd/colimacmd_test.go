package colimacmd

import (
	"bytes"
	"errors"
	"io"
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
	return colimaProfileHome(t, "default", cfg)
}

// colimaProfileHome is colimaHome for profile's colima.yaml.
func colimaProfileHome(t *testing.T, profile, cfg string) (func(string) string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, profile, "colima.yaml")
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
	return runColimaWith(Config{}, getenv, r, args...)
}

func runColimaWith(cfg Config, getenv func(string) string, r runner.Runner, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Command("jam-colima", getenv, r, cfg)(args, cli.Globals{}, &out, &errb)
	return code, out.String(), errb.String()
}

// profileFrom is a Config whose manager config at path names profile.
func profileFrom(t *testing.T, path, profile string) Config {
	return Config{Profile: func(p string) (string, error) {
		if p != path {
			t.Errorf("read profile from %q, want %q", p, path)
		}
		return profile, nil
	}}
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
		got, err := colimaConfigPath(env(c.env), c.exists, "default")
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if got, err := colimaConfigPath(env(map[string]string{"COLIMA_HOME": "/c"}), never, "work"); err != nil || got != "/c/work/colima.yaml" {
		t.Errorf("profile work: got %q, %v", got, err)
	}
	if _, err := colimaConfigPath(env(nil), always, "default"); err == nil {
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
	if code, _, errs := runColima(getenv, absent, "check-docker"); code != 1 || !strings.Contains(errs, "jam-colima colima setup-docker") {
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
	code := Command("jam", getenv, &runner.Fake{}, Config{})([]string{"setup-docker"}, cli.Globals{DryRun: true}, &out, &errb)
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

func TestCommandUsesProgName(t *testing.T) {
	var out, errb bytes.Buffer
	code := Command("jam-colima", func(string) string { return "" }, &runner.Fake{}, Config{})(nil, cli.Globals{}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "jam-colima colima: expected setup-docker|check-docker") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestColimaSetupDockerProfileFlag(t *testing.T) {
	getenv, path := colimaProfileHome(t, "work", freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--profile", "work")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("work profile's config not updated:\n%s", b)
	}
	for _, want := range []string{"colima restart --profile work", "jam-colima colima check-docker --profile work", "colima ssh --profile work --"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestColimaSetupDockerDefaultProfileHints(t *testing.T) {
	getenv, _ := colimaHome(t, freshColima)
	_, out, _ := runColima(getenv, &runner.Fake{}, "setup-docker")
	if strings.Contains(out, "--profile") || !strings.Contains(out, "colima restart ") {
		t.Fatalf("default profile hints must not name a profile:\n%s", out)
	}
}

func TestColimaSetupDockerReadsProfileFromConfig(t *testing.T) {
	getenv, path := colimaProfileHome(t, "work", freshColima)
	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	code, _, errs := runColimaWith(profileFrom(t, cfgPath, "work"), getenv, &runner.Fake{}, "setup-docker", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("work profile's config not updated:\n%s", b)
	}
}

func TestColimaProfileFlagOverridesConfig(t *testing.T) {
	getenv, path := colimaProfileHome(t, "other", freshColima)
	cfg := Config{Profile: func(string) (string, error) { t.Error("config read despite --profile"); return "work", nil }}
	if code, _, errs := runColimaWith(cfg, getenv, &runner.Fake{}, "setup-docker", "--config", "/x/config.yml", "--profile", "other"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("--profile's config not updated:\n%s", b)
	}
}

func TestColimaDefaultConfigPath(t *testing.T) {
	getenv, path := colimaProfileHome(t, "work", freshColima)
	def := filepath.Join(t.TempDir(), "config.yml")
	cfg := profileFrom(t, def, "work")
	cfg.DefaultPath = def
	// Absent: the default profile, config unread.
	if code, _, errs := runColimaWith(cfg, getenv, &runner.Fake{}, "setup-docker"); code != 1 || !strings.Contains(errs, filepath.Join("default", "colima.yaml")) {
		t.Fatalf("absent default config: code=%d stderr=%q", code, errs)
	}
	if err := os.WriteFile(def, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runColimaWith(cfg, getenv, &runner.Fake{}, "setup-docker"); code != 0 {
		t.Fatalf("present default config: code=%d stderr=%q", code, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("work profile's config not updated:\n%s", b)
	}
}

func TestColimaBadProfile(t *testing.T) {
	getenv, _ := colimaHome(t, freshColima)
	for _, sub := range []string{"setup-docker", "check-docker"} {
		if code, _, errs := runColima(getenv, &runner.Fake{}, sub, "--profile", "../x"); code != 2 || !strings.Contains(errs, `profile "../x"`) {
			t.Errorf("%s: code=%d stderr=%q", sub, code, errs)
		}
	}
	bad := Config{Profile: func(string) (string, error) { return "", errors.New("parse boom") }}
	if code, _, errs := runColimaWith(bad, getenv, &runner.Fake{}, "check-docker", "--config", "/c.yml"); code != 2 || !strings.Contains(errs, "parse boom") {
		t.Errorf("config error: code=%d stderr=%q", code, errs)
	}
}

func TestColimaCheckDockerProfile(t *testing.T) {
	cfgPath := "/m/config.yml"
	absent := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{}}`}}}
	code, _, errs := runColimaWith(profileFrom(t, cfgPath, "work"), func(string) string { return "" }, absent, "check-docker", "--config", cfgPath)
	if code != 1 || !strings.Contains(errs, "jam-colima colima setup-docker --profile work") || !strings.Contains(errs, "colima restart --profile work") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if len(absent.Calls) != 1 || !strings.Contains(strings.Join(absent.Calls[0].Args, " "), "--context colima-work") {
		t.Fatalf("docker not pinned to colima-work: %+v", absent.Calls)
	}
	unreachable := &runner.Fake{Outputs: []runner.FakeResult{{Err: errors.New("cannot connect")}}}
	if _, _, errs := runColima(func(string) string { return "" }, unreachable, "check-docker", "--profile", "work"); !strings.Contains(errs, "colima start --profile work") {
		t.Fatalf("unreachable hint: %q", errs)
	}
}

// --profile is normalised as colima does: a colima- prefix is stripped and
// colima means the default VM.
func TestColimaProfileNormalised(t *testing.T) {
	getenv, path := colimaProfileHome(t, "work", freshColima)
	code, out, errs := runColima(getenv, &runner.Fake{}, "setup-docker", "--profile", "colima-work")
	if code != 0 || !strings.Contains(out, "colima restart --profile work") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("work profile's config not updated:\n%s", b)
	}
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"sysbox-runc":{}}`}}}
	if code, _, _ := runColima(func(string) string { return "" }, f, "check-docker", "--profile", "colima"); code != 0 ||
		!strings.Contains(strings.Join(f.Calls[0].Args, " "), "--context colima ") {
		t.Fatalf("profile colima must be the default VM: code=%d calls=%+v", code, f.Calls)
	}
}

// Both verbs say which profile they act on, and where it came from.
func TestColimaSaysWhichProfile(t *testing.T) {
	ok := func() *runner.Fake { return &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"sysbox-runc":{}}`}}} }
	none := func(string) string { return "" }
	cfgPath := "/m/config.yml"
	def := filepath.Join(t.TempDir(), "absent.yml")
	cases := []struct {
		name string
		cfg  Config
		args []string
		want string
	}{
		{"flag", Config{}, []string{"--profile", "work"}, "colima profile work (--profile)"},
		{"config", profileFrom(t, cfgPath, "work"), []string{"--config", cfgPath}, "colima profile work (colima.profile in " + cfgPath + ")"},
		{"default", Config{DefaultPath: def}, nil, "colima profile default (no config at " + def + ")"},
	}
	for _, c := range cases {
		_, out, errs := runColimaWith(c.cfg, none, ok(), append([]string{"check-docker"}, c.args...)...)
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: stdout=%q stderr=%q; want %q", c.name, out, errs, c.want)
		}
	}
}

// Prepare runs before the config is looked for (it moves an old config dir)
// and may redirect --config.
func TestColimaPrepare(t *testing.T) {
	getenv, path := colimaProfileHome(t, "work", freshColima)
	def := filepath.Join(t.TempDir(), "config.yml")
	var got []string
	cfg := Config{
		DefaultPath: def,
		Prepare: func(p string, stderr io.Writer) (string, error) {
			got = append(got, p)
			if p == "" { // the move creates the default config
				return p, os.WriteFile(def, []byte("x"), 0o600)
			}
			return "/new/config.yml", nil
		},
		Profile: func(p string) (string, error) {
			if p != def && p != "/new/config.yml" {
				t.Errorf("read %q", p)
			}
			return "work", nil
		},
	}
	if code, _, errs := runColimaWith(cfg, getenv, &runner.Fake{}, "setup-docker"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "sysbox-runc:") {
		t.Fatalf("work profile's config not updated:\n%s", b)
	}
	_, out, _ := runColimaWith(cfg, getenv, &runner.Fake{}, "setup-docker", "--config", "/old/config.yml")
	if strings.Join(got, ",") != ",/old/config.yml" || !strings.Contains(out, "colima.profile in /new/config.yml") {
		t.Fatalf("Prepare calls %q stdout=%q", got, out)
	}
	cfg.Prepare = func(string, io.Writer) (string, error) { return "", errors.New("move boom") }
	if code, _, errs := runColimaWith(cfg, getenv, &runner.Fake{}, "check-docker"); code != 1 || !strings.Contains(errs, "move boom") {
		t.Fatalf("Prepare error: code=%d stderr=%q", code, errs)
	}
}
