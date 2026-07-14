package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSections(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	local := filepath.Join(dir, "secrets.local.yml")
	write(t, yml, `
minters:
  gh-cove:
    github: { app-id: "1", install-id: "2", app-key: { value: "/k.pem" } }
global:
  shared-tracker: { command: ["gh", "auth", "token"] }
kits:
  cove:
    AT_TASK_GIT_TOKEN: { command: ["at-mint-shim"] }
    AT_DISPATCH_TRACKER_TOKEN: { global: shared-tracker }
`)
	write(t, local, `
kits:
  /abs/checkout/cove:
    ANTHROPIC_AUTH_TOKEN: { value: "sk-ant-oat01-test" }
`)
	st, err := Load(yml, local)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Minters["gh-cove"]; !ok {
		t.Fatal("minter gh-cove not loaded")
	}
	if _, ok := st.Global["shared-tracker"]; !ok {
		t.Fatal("global shared-tracker not loaded")
	}
	if k, _ := st.Kits["cove"]["AT_DISPATCH_TRACKER_TOKEN"].Kind(); k != "global" {
		t.Fatalf("cove tracker source kind = %q, want global", k)
	}
	if _, ok := st.Local["/abs/checkout/cove"]["ANTHROPIC_AUTH_TOKEN"]; !ok {
		t.Fatal("local path entry not loaded")
	}
}

func TestLoadMissingFilesEmpty(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "none.yml"), filepath.Join(t.TempDir(), "none.local.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Minters) != 0 || len(st.Global) != 0 || len(st.Kits) != 0 || len(st.Local) != 0 {
		t.Fatal("missing files should yield empty sections")
	}
}

func TestLoadDanglingGlobalRef(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	write(t, yml, `
kits:
  cove:
    X: { global: nope }
`)
	if _, err := Load(yml, filepath.Join(dir, "missing.local.yml")); err == nil {
		t.Fatal("want error for global: referencing a missing shared supply")
	}
}

func TestLoadDanglingMintRef(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "secrets.yml")
	write(t, yml, `
kits:
  cove:
    X: { mint: nope }
`)
	if _, err := Load(yml, filepath.Join(dir, "missing.local.yml")); err == nil {
		t.Fatal("want error for mint: referencing a missing minter profile")
	}
}
