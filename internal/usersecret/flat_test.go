package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFlat_ParsesCredentials(t *testing.T) {
	p := writeTemp(t, `
global:
  gh: { command: ["gh", "auth", "token"] }
credentials:
  jam-db: { value: "pw" }
  git-pat: { global: gh }
`)
	st, err := LoadFlat(p)
	if err != nil {
		t.Fatalf("LoadFlat: %v", err)
	}
	if len(st.Credentials) != 2 {
		t.Fatalf("want 2 credentials, got %d", len(st.Credentials))
	}
	if st.Credentials["jam-db"].Value == nil || *st.Credentials["jam-db"].Value != "pw" {
		t.Fatalf("jam-db value not parsed: %+v", st.Credentials["jam-db"])
	}
}

func TestLoadFlat_MissingFileIsEmpty(t *testing.T) {
	st, err := LoadFlat(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("missing file should be empty, not error: %v", err)
	}
	if len(st.Credentials) != 0 {
		t.Fatalf("want empty store, got %d credentials", len(st.Credentials))
	}
}

func TestLoadFlat_UndefinedGlobalReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  git-pat: { global: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined global reference, got nil")
	}
}

func TestLoadFlat_UndefinedMintReference(t *testing.T) {
	p := writeTemp(t, `
credentials:
  anthropic: { mint: missing }
`)
	if _, err := LoadFlat(p); err == nil {
		t.Fatal("want error for undefined mint reference, got nil")
	}
}
