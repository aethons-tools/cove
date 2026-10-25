package jam

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// writeExport writes an export as a writer would: Files at 0644, each kit dir's files at their
// modes. It replaces the helper of the same name in the deleted config_export_context_test.go.
func writeExport(t *testing.T, dir string, exp ConfigTreeExport) {
	t.Helper()
	for p, body := range exp.Files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(p)), body, 0o644)
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			mustWrite(t, filepath.Join(dir, filepath.FromSlash(d+"/"+f.Path)), string(f.Data), fs.FileMode(f.Mode))
		}
	}
}

func kitsJam(t *testing.T) *MemStore {
	t.Helper()
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "web", kittreetest.Files("kit.yml", "kind: studio\n# web\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\nbase: {kit: web@v1}\n")); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestExportWritesKitDirectories(t *testing.T) {
	exp, err := ExportConfigTree(kitsJam(t).ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	web := exp.Kits["kits/web"]
	var got []string
	for _, f := range web {
		got = append(got, f.Path+":"+fs.FileMode(f.Mode).String())
	}
	if strings.Join(got, " ") != ".dockerignore:-rw-r--r-- Dockerfile:-rw-r--r-- kit.yml:-rw-r--r-- run.sh:-rwxr-xr-x" {
		t.Fatalf("kits/web = %v", got)
	}
	app := exp.Kits["kits/app"]
	if len(app) != 1 || string(app[0].Data) != "kind: studio\nbase: {kit: ../web}\n" {
		t.Fatalf("kits/app = %+v", app)
	}
	if _, ok := exp.Files["kits/app/kit.yml"]; ok {
		t.Fatal("a kit dir's files belong in Kits, not Files")
	}
}

func TestExportMirrorsAGoneManagedKitToNothing(t *testing.T) {
	st := kitsJam(t)
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "old", Commit: "c"}}
	exp, err := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if files, ok := exp.Kits["kits/old"]; !ok || len(files) != 0 {
		t.Fatalf("kits/old = %v, %v", files, ok)
	}
}

// TestConfigTreeRoundTripUnchanged: write → (commit, clone: a copy on disk)
// → apply adopts every kit with no new version — modes, a .dockerignored
// file on disk and an in-tree parent included — and a second apply is a no-op.
func TestConfigTreeRoundTripUnchanged(t *testing.T) {
	st := kitsJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeExport(t, dir, exp)
	mustWrite(t, filepath.Join(dir, "kits/web/debug.log"), "local noise", 0o644)
	if fi, err := os.Stat(filepath.Join(dir, "kits/web/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode on disk: %v %v", fi, err)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"web", "app"} {
		if op := opOf(res.Plan, ConfigKindKit, n); op.Action != ConfigAdopt {
			t.Fatalf("%s: %+v (the repo copy must equal Jam's tree)", n, op)
		}
		if k, _ := st.GetKit(n); k.Current != 1 {
			t.Fatalf("%s got a new version: v%d", n, k.Current)
		}
	}
	again, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil || again.Plan.Counts()[ConfigUnchanged] != len(again.Plan.Ops) {
		t.Fatalf("second apply is not a no-op: %v %+v", err, again.Plan.Ops)
	}
}

// TestConfigTreeRoundTripFileNoteThenDockerfileChange: a pushed kit with a
// file: note writes and applies back unchanged; then a change to its
// Dockerfile alone plans as an update and makes a new version.
func TestConfigTreeRoundTripFileNoteThenDockerfileChange(t *testing.T) {
	st := NewMemStore()
	yml := "kind: studio\nnotes:\n  - {name: r.md, read-when: x, file: docs/r.md}\n"
	if _, _, err := PushKitFiles(st, "doc", kittreetest.Files("kit.yml", yml, "Dockerfile", dockerfile, "docs/r.md", "# R\n")); err != nil {
		t.Fatal(err)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeExport(t, dir, exp)
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil {
		t.Fatal(err)
	}
	if op := opOf(res.Plan, ConfigKindKit, "doc"); op.Action != ConfigAdopt {
		t.Fatalf("doc: %+v", op)
	}
	mustWrite(t, filepath.Join(dir, "kits/doc/Dockerfile"), dockerfile+"RUN true\n", 0o644)
	if tr, err = LoadConfigTree(dir); err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	res, err = ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil {
		t.Fatal(err)
	}
	if op := opOf(res.Plan, ConfigKindKit, "doc"); op.Action != ConfigUpdate || !strings.Contains(op.After, " Dockerfile\n") || op.Before == op.After {
		t.Fatalf("Dockerfile change: %+v", op)
	}
	if v, _ := LoadKitVersion(st, "doc", 0); v.Version != 2 || v.Kit.Notes[0].Body != "# R\n" {
		t.Fatalf("doc after the update: v%d %+v", v.Version, v.Kit.Notes)
	}
}

func TestKitCanonIsTheTreeListing(t *testing.T) {
	st := kitsJam(t)
	v, _ := LoadKitVersion(st, "web", 0)
	got := string(kitCanon(v.Tree, st.KitBlob, &KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main", Commit: "c"}))
	if !strings.HasPrefix(got, "kind: studio\n# web\n# files:\n#   0644 6 ") || !strings.Contains(got, " run.sh\n") ||
		!strings.HasSuffix(got, "# source: https://github.com/acme/kits//web@main\n") {
		t.Fatalf("canon =\n%s", got)
	}
}

// A kit whose current version is broken exports as an object with a fixed
// "# broken:" canon and no files: the export, plan and drift check go on,
// and the repo's copy of that kit is left as it is.
func TestExportBrokenKitIsNotAnError(t *testing.T) {
	st := kitsJam(t)
	if err := st.PutKitDocs([]Kit{{Name: "old", Current: 1, Broken: map[int]string{1: "its context cannot be read"}}}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	objs, err := exportObjects(stateFromSnapshot(st.ExportConfig(), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := objs[ConfigKey{ConfigKindKit, "old"}]; string(o.Canon) != "# broken: its context cannot be read\n" || o.Files != nil || o.KitFiles != nil {
		t.Fatalf("broken kit object %+v", o)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), []ConfigMark{{Kind: ConfigKindKit, Name: "old"}}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exp.Kits["kits/old"]; ok || slices.Contains(exp.Remove, "kits/old.source.yml") || len(exp.Kits["kits/web"]) == 0 {
		t.Fatalf("kits %+v remove %v", exp.Kits, exp.Remove)
	}
}
