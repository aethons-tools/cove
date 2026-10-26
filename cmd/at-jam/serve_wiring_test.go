package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanCredentials_FailsClosedOnUnsupplied(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  git-pat: { value: sekret-pat-value }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"git-pat": {}, "missing": {}},
	}
	_, err := planCredentials(cfg)
	if err == nil {
		t.Fatal("want fail-closed error naming the unsupplied credential, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing") {
		t.Errorf("error should name the unsupplied credential %q: %v", "missing", err)
	}
	if !strings.Contains(msg, credFile) {
		t.Errorf("error should name the credentials file %q: %v", credFile, err)
	}
	if strings.Contains(msg, "sekret-pat-value") {
		t.Errorf("error must not leak a supplied secret value: %v", err)
	}
}

func TestPlanCredentials_ResolvesSpecsByName(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  jam-db: { value: pw }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"jam-db": {}},
	}
	specs, err := planCredentials(cfg)
	if err != nil {
		t.Fatalf("planCredentials: %v", err)
	}
	if s, ok := specs["jam-db"]; !ok || !s.Literal || s.Value != "pw" {
		t.Fatalf("jam-db spec wrong: %+v ok=%v", specs["jam-db"], ok)
	}
}
