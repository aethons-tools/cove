package adminclient

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestConfigRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
		jam.WithConfig(store, nil, jam.ConfigEnv{CredExists: creds}, log)))
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
	if _, err := c.ConfigApply(tree, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("ConfigApply stale = %v, want ErrConflict", err)
	}
	res, err := c.ConfigApply(tree, diff.Plan.Digest)
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

func TestConfigSourceMethods(t *testing.T) {
	repo, _, _ := gittest.Serve(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	serve := func(followed gitsource.Source) (*Client, *jam.MemStore) {
		store := jam.NewMemStore()
		f := &gitsource.Fetcher{}
		env := jam.ConfigEnv{CredExists: creds, Followed: followed}
		env.Follower = &jam.ConfigFollower{Store: store, Fetcher: f, Env: env}
		ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
			jam.WithConfig(store, f, env, log)))
		t.Cleanup(ts.Close)
		return New(ts.URL, ""), store
	}
	none, _ := serve(gitsource.Source{})
	if _, err := none.ConfigDiffSource(""); !errors.Is(err, ErrConflict) {
		t.Fatalf("diff with no followed repo = %v, want ErrConflict", err)
	}
	if _, err := none.ConfigSync(""); !errors.Is(err, ErrConflict) {
		t.Fatalf("sync with no followed repo = %v, want ErrConflict", err)
	}
	c, _ := serve(gitsource.Source{Repo: repo, Ref: "main"})
	diff, err := c.ConfigDiffSource("")
	if err != nil || diff.Plan.Ops[0].Action != jam.ConfigCreate {
		t.Fatalf("ConfigDiffSource = %+v, %v", diff, err)
	}
	if res, err := c.ConfigSync(diff.Plan.Digest); err != nil || !res.Applied {
		t.Fatalf("ConfigSync = %+v, %v", res, err)
	}
	if s, err := c.ConfigFollow(); err != nil || s.Applied != diff.Commit {
		t.Fatalf("ConfigFollow = %+v, %v", s, err)
	}
	if res, err := c.ConfigApplySource(repo+"@main", ""); err != nil || res.Applied {
		t.Fatalf("ConfigApplySource = %+v, %v", res, err)
	}
}

// ConfigExportTree decodes a kit directory's files, binary content and modes.
func TestConfigExportTreeKits(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
		jam.WithConfig(store, nil, jam.ConfigEnv{CredExists: creds}, log)))
	defer ts.Close()
	if _, _, err := jam.PushKitFiles(store, "k", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", "FROM x\n", "b.bin*", "\xff\x00")); err != nil {
		t.Fatal(err)
	}
	exp, err := New(ts.URL, "").ConfigExportTree(true)
	if err != nil {
		t.Fatal(err)
	}
	var b jam.ConfigKitFile
	for _, f := range exp.Kits["kits/k"] {
		if f.Path == "b.bin" {
			b = f
		}
	}
	if !bytes.Equal(b.Data, []byte{0xff, 0x00}) || b.Mode != 0o755 || len(exp.Kits["kits/k"]) != 3 {
		t.Fatalf("kits %+v", exp.Kits)
	}
}
