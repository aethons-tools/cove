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
	"github.com/aethons-tools/cove/internal/studio"
)

func TestConfigRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
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
	diff, err := c.ConfigDiff(tree, false)
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
	if err := c.ConfigRelease(jam.ConfigKindUser, "bob"); err != nil {
		t.Fatalf("ConfigRelease = %v", err)
	}
	if len(store.ListConfigMarks()) != 0 {
		t.Fatalf("marks after release: %v", store.ListConfigMarks())
	}
	if err := c.ConfigRelease(jam.ConfigKindUser, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ConfigRelease = %v, want ErrNotFound", err)
	}
}

func TestConfigSourceMethods(t *testing.T) {
	repo, _, _ := gittest.Serve(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(store, &gitsource.Fetcher{}, jam.ConfigEnv{CredExists: creds}, log)))
	defer ts.Close()
	c := New(ts.URL, "")
	src := repo + "@main"
	if _, err := c.ConfigDiffSource("", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("diff with nothing tracked = %v, want ErrConflict", err)
	}
	tr, err := c.ConfigTrack(src)
	if err != nil || tr.Commit == "" || tr.Applied {
		t.Fatalf("ConfigTrack = %+v, %v", tr, err)
	}
	diff, err := c.ConfigDiffSource("", false)
	if err != nil || diff.Plan.Ops[0].Action != jam.ConfigCreate {
		t.Fatalf("ConfigDiffSource = %+v, %v", diff, err)
	}
	if res, err := c.ConfigSync(false, diff.Plan.Digest); err != nil || !res.Applied {
		t.Fatalf("ConfigSync = %+v, %v", res, err)
	}
	if res, err := c.ConfigApplySource(src, false, ""); err != nil || res.Applied {
		t.Fatalf("ConfigApplySource = %+v, %v", res, err)
	}
	if err := c.ConfigUntrack(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConfigSync(false, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("sync after untrack = %v, want ErrConflict", err)
	}
}

// ConfigExportTree decodes a context kit's files, binary content and modes.
func TestConfigExportTreeBinary(t *testing.T) {
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(store, nil, jam.ConfigEnv{CredExists: creds}, log)))
	defer ts.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0xff, 0x00}, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "b.bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	b64, err := studio.PackContextDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(store, "k", "kind: studio\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	exp, err := New(ts.URL, "").ConfigExportTree(true)
	if err != nil {
		t.Fatal(err)
	}
	if b := exp.Binary["kits/k/context/b.bin"]; !bytes.Equal(b.Data, []byte{0xff, 0x00}) || b.Mode != 0o755 {
		t.Fatalf("blob %+v", b)
	}
	if len(exp.Prune) != 1 || exp.Prune[0] != "kits/k/context" {
		t.Fatalf("prune %v", exp.Prune)
	}
}
