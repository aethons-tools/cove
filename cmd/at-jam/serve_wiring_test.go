package main

import (
	"io"
	"log/slog"
	"reflect"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
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

func TestServeWiresRouterWithLocal(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := jam.NewMemStore()

	got, err := buildLauncher(serveConfig{}, st, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.launcher.(placeholderLauncher); !ok {
		t.Fatalf("no launcher block: want placeholderLauncher, got %T", got.launcher)
	}
	if got.images != nil || got.haveDefaultKit {
		t.Errorf("no launcher block: want no images and no default kit, got %+v", got)
	}

	id := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(id+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg serveConfig
	cfg.Runtime.Launcher = &launcherConfig{IdentityFile: id, DockerContext: "colima-jam"}
	got, err = buildLauncher(cfg, st, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	router, ok := got.launcher.(*fleet.Router)
	if !ok {
		t.Fatalf("launcher block: want *fleet.Router, got %T", got.launcher)
	}
	if names := router.Names(); !reflect.DeepEqual(names, []string{"local"}) {
		t.Errorf("Names() = %v, want [local]", names)
	}
	if _, ok := got.launcher.(jam.KitDraftBuilder); !ok {
		t.Error("router must satisfy jam.KitDraftBuilder")
	}
	if got.images == nil || !got.haveDefaultKit {
		t.Errorf("want images and default kit set, got %+v", got)
	}
}
