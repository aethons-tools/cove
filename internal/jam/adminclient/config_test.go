package adminclient

import (
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/configtar"
)

func TestConfigRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(store, jam.ConfigEnv{CredExists: creds}, log)))
	defer ts.Close()
	c := New(ts.URL, "")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users/bob.yml"), []byte("kind: user\nversion: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := configtar.Pack(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := c.ConfigDiff(tree)
	if err != nil || diff.Applied || len(diff.Plan.Ops) != 1 || diff.Plan.Ops[0].Action != jam.ConfigCreate {
		t.Fatalf("ConfigDiff = %+v, %v", diff, err)
	}
	if _, err := c.ConfigApply(tree, false, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("ConfigApply stale = %v, want ErrConflict", err)
	}
	res, err := c.ConfigApply(tree, true, diff.Plan.Digest)
	if err != nil || !res.Applied || res.Counts[jam.ConfigCreate] != 1 {
		t.Fatalf("ConfigApply = %+v, %v", res, err)
	}
	exp, err := c.ConfigExportTree(false)
	if err != nil || exp.Files["users/bob.yml"] == "" {
		t.Fatalf("ConfigExportTree = %+v, %v", exp, err)
	}
	if exp, err = c.ConfigExportTree(true); err != nil || exp.Files["users/bob.yml"] == "" {
		t.Fatalf("ConfigExportTree all = %+v, %v", exp, err)
	}
}
