package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func newBackupServer(t *testing.T, store jam.Store) *httptest.Server {
	t.Helper()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func seedStore(t *testing.T) *jam.MemStore {
	t.Helper()
	s := jam.NewMemStore()
	if err := s.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddActor(jam.Actor{ID: "spider-18", TokenHash: jam.HashToken("tok"), Grants: []jam.Grant{{Project: jam.DefaultProject, Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExportImportRoundTripJSON(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)

	file := filepath.Join(t.TempDir(), "backup.json")
	var out, errb bytes.Buffer
	if code := run([]string{"export", "--admin-url", srcTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("export exit=%d stderr=%s", code, errb.String())
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		t.Fatalf("backup file empty: %v", err)
	}

	dst := jam.NewMemStore()
	dstTS := newBackupServer(t, dst)
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("import exit=%d stderr=%s", code, errb.String())
	}
	if a, ok := dst.Lookup(jam.HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("import did not restore actor: %+v ok=%v", a, ok)
	}
}

func TestExportYAMLThenImportSniffs(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)
	file := filepath.Join(t.TempDir(), "backup.yaml")
	var out, errb bytes.Buffer
	if code := run([]string{"export", "--admin-url", srcTS.URL, "--format", "yaml", file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("export yaml exit=%d stderr=%s", code, errb.String())
	}
	data, _ := os.ReadFile(file)
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		t.Fatalf("expected YAML, got JSON-looking output: %s", data)
	}
	dst := jam.NewMemStore()
	dstTS := newBackupServer(t, dst)
	// No --format: import must sniff YAML.
	if code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("import sniff exit=%d stderr=%s", code, errb.String())
	}
	if _, ok := dst.GetRole(jam.DefaultProject, "guest"); !ok {
		t.Fatal("role not restored from YAML")
	}
}

func TestImportIntoNonEmptyFails(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)
	file := filepath.Join(t.TempDir(), "backup.json")
	var out, errb bytes.Buffer
	run([]string{"export", "--admin-url", srcTS.URL, file}, func(string) string { return "" }, &out, &errb)

	dst := seedStore(t) // already has config
	dstTS := newBackupServer(t, dst)
	out.Reset()
	errb.Reset()
	code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb)
	if code == 0 {
		t.Fatalf("import into non-empty should fail; stderr=%s", errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("not empty")) {
		t.Fatalf("stderr should explain non-empty target: %s", errb.String())
	}
}

func TestExportTightensPermsOnExistingFile(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)
	file := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"export", "--admin-url", srcTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("export exit=%d stderr=%s", code, errb.String())
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}
