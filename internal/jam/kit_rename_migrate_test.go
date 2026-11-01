package jam

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
)

// preRenameDigest is a base image digest the rename must keep.
const preRenameDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// oldDockerfile uses every pre-rename kit name, the old base repository in
// a FROM line among them.
var oldDockerfile = "ARG " + legacynames.KitRenames[2].Old + "\nFROM ${" + legacynames.KitRenames[2].Old + "}\n" +
	"ARG " + legacynames.KitRenames[1].Old + "\nENV " + legacynames.KitRenames[3].Old + "=\"GOPATH\"\n" +
	"FROM " + legacynames.BaseImageRepo + "@" + preRenameDigest + " AS tools\n"

// seedKit stores files as kit name's next version straight through the store
// (PushKitFiles would refuse the old names).
func seedKit(t *testing.T, st Store, name string, files map[string]string) int {
	t.Helper()
	var fs []kittree.File
	for p, d := range files {
		fs = append(fs, kittree.File{Path: p, Mode: 0o644, Data: []byte(d)})
	}
	tree, blobs, err := kittree.BuildFiles(fs)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.PushKitVersion(name, tree.Hash(), KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, KitVersionMeta{PushedAt: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func kitFile(t *testing.T, st Store, name, path string) string {
	t.Helper()
	cur, ok := LoadKitVersion(st, name, 0)
	if !ok {
		t.Fatalf("no kit %s", name)
	}
	for _, e := range cur.Tree.Entries {
		if e.Path == path {
			b, _ := st.KitBlob(e.SHA256)
			return string(b)
		}
	}
	t.Fatalf("kit %s has no %s", name, path)
	return ""
}

// parentYML is a kit built from the Dockerfile next to its kit.yml.
const parentYML = "kind: studio\n"

func TestMigrateKitRenameRewritesCurrentTree(t *testing.T) {
	st := NewMemStore()
	script := "echo " + legacynames.KitRenames[3].Old + " stays in scripts\n"
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "Dockerfile": oldDockerfile, "install.sh": script})
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitRename(st, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("base")
	if k.Current != 2 || k.Meta[2].Origin != KitOriginMigration {
		t.Fatalf("current v%d meta %+v (kept %v)", k.Current, k.Meta[k.Current], rep.Kept)
	}
	got := kitFile(t, st, "base", "Dockerfile")
	if _, old := legacynames.FindKitName(got); old || !strings.Contains(got, "FROM ${JAM_STUDIO_BASE_IMAGE}") ||
		!strings.Contains(got, "ENV JAM_SSHENV=") || !strings.Contains(got, "ARG JAM_STUDIO_TARGET_ARCH") ||
		!strings.Contains(got, "FROM ghcr.io/aethons-tools/jam-studio-base@"+preRenameDigest+" AS tools") {
		t.Fatalf("Dockerfile =\n%s", got)
	}
	if kitFile(t, st, "base", "install.sh") != script {
		t.Fatal("only kit.yml and Dockerfiles are rewritten")
	}
	if len(rep.Rewritten) != 1 || rep.Rewritten[0] != "base@v2" || tr.IsOpen(condition.Key(KitMigrateKind, "base")) {
		t.Fatalf("report %+v; condition open %v", rep, tr.IsOpen(condition.Key(KitMigrateKind, "base")))
	}
}

func TestMigrateKitRenameRewritesKitYMLBaseImage(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "img", map[string]string{"kit.yml": "kind: studio\nbase:\n  image: " + legacynames.BaseImageRepo + "@" + preRenameDigest + "\n"})
	rep, err := MigrateKitRename(st, nil, nil)
	if err != nil || len(rep.Rewritten) != 1 {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if got := kitFile(t, st, "img", "kit.yml"); !strings.Contains(got, "image: ghcr.io/aethons-tools/jam-studio-base@"+preRenameDigest) {
		t.Fatalf("kit.yml =\n%s", got)
	}
}

func TestMigrateKitRenameIsIdempotent(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "Dockerfile": oldDockerfile})
	if _, err := MigrateKitRename(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateKitRename(st, nil, nil)
	if k, _ := st.GetKit("base"); err != nil || len(rep.Rewritten) != 0 || k.Current != 2 {
		t.Fatalf("second run: %+v, %v, current v%d", rep, err, k.Current)
	}
}

func TestMigrateKitRenameRepinsAChildPinnedToTheOldParent(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "Dockerfile": oldDockerfile})
	seedKit(t, st, "child", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: base@v1\n"})
	tr := condition.New(condition.Options{})
	if _, err := MigrateKitRename(st, tr, nil); err != nil {
		t.Fatal(err)
	}
	if got := kitFile(t, st, "child", "kit.yml"); !strings.Contains(got, "kit: base@v2") {
		t.Fatalf("child kit.yml =\n%s", got)
	}
	if k, _ := st.GetKit("child"); k.Meta[k.Current].Origin != KitOriginMigration {
		t.Fatalf("child meta = %+v", k.Meta[k.Current])
	}
	if open := tr.OpenKeys(KitMigrateKind); len(open) != 0 {
		t.Fatalf("open = %v", open)
	}
}

// A child pinned to an old parent version the migration did not rewrite
// (the parent's current was pushed since) is not re-pinned silently: it
// opens kit.migrate:<child> with a fixed text.
func TestMigrateKitRenameFlagsAChildPinnedToAnUnmigratedParentVersion(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "Dockerfile": oldDockerfile})
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "Dockerfile": "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN true\n"})
	seedKit(t, st, "child", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: base@v1\n"})
	tr := condition.New(condition.Options{})
	if _, err := MigrateKitRename(st, tr, nil); err != nil {
		t.Fatal(err)
	}
	if got := kitFile(t, st, "child", "kit.yml"); !strings.Contains(got, "kit: base@v1") {
		t.Fatalf("child must keep its pin:\n%s", got)
	}
	if k, _ := st.GetKit("base"); k.Current != 2 {
		t.Fatalf("the parent's current needs no rewrite: v%d", k.Current)
	}
	c, ok := tr.Get(condition.Key(KitMigrateKind, "child"))
	want := "kit child v1 pins base@v1, which still uses the pre-rename name " + legacynames.KitRenames[2].Old
	if !ok || c.Summary != want || !strings.Contains(c.Fix, "renamed-from-at.md") {
		t.Fatalf("condition = %+v, %v; want summary %q", c, ok, want)
	}
}

func TestMigrateKitRenameKeepsAnUnrewritableKit(t *testing.T) {
	st := NewMemStore()
	// Its parent does not exist, so the rewritten version cannot validate.
	seedKit(t, st, "orphan", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: nosuch\n", "Dockerfile": oldDockerfile})
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitRename(st, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := st.GetKit("orphan"); k.Current != 1 || len(rep.Kept) != 1 {
		t.Fatalf("current v%d, report %+v", k.Current, rep)
	}
	c, ok := tr.Get(condition.Key(KitMigrateKind, "orphan"))
	if !ok || !strings.Contains(c.Summary, "pre-rename name") || !strings.Contains(c.Fix, "renamed-from-at.md") {
		t.Fatalf("condition = %+v, %v", c, ok)
	}
}

func TestPushKitFilesRefusesPreRenameNames(t *testing.T) {
	st := NewMemStore()
	_, _, err := PushKitFiles(st, "k", []kittree.File{
		{Path: "kit.yml", Mode: 0o644, Data: []byte(parentYML)},
		{Path: "Dockerfile", Mode: 0o644, Data: []byte(oldDockerfile)},
	})
	if err == nil || !strings.Contains(err.Error(), "Dockerfile uses "+legacynames.KitRenames[2].Old+", renamed JAM_STUDIO_BASE_IMAGE") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = PushKitFiles(st, "k", []kittree.File{
		{Path: "kit.yml", Mode: 0o644, Data: []byte(parentYML)},
		{Path: "Dockerfile", Mode: 0o644, Data: []byte("FROM " + legacynames.BaseImageRepo + "@" + preRenameDigest + "\n")},
	})
	if err == nil || !strings.Contains(err.Error(), "renamed ghcr.io/aethons-tools/jam-studio-base") {
		t.Fatalf("a FROM naming the old base repository: err = %v", err)
	}
}

// A draft still using an old name has that as a problem, so it cannot be
// published.
func TestKitDraftProblemsNamePreRenameNames(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "k", map[string]string{"kit.yml": parentYML, "Dockerfile": oldDockerfile})
	cur, _ := LoadKitVersion(st, "k", 0)
	_, problems := draftProblems(st, "k", cur.Tree)
	if len(problems) != 1 || !strings.Contains(problems[0], "renamed JAM_STUDIO_BASE_IMAGE") {
		t.Fatalf("problems = %v", problems)
	}
}

// The kept-kit warning carries the kit's name and a fixed reason, never the
// parse or validation text (which quotes the kit's own content).
func TestMigrateKitRenameLogsNamesOnly(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "orphan", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: nosuch-parent-xyz\n", "Dockerfile": oldDockerfile})
	var logs strings.Builder
	if _, err := MigrateKitRename(st, nil, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "orphan") || !strings.Contains(logs.String(), kitKeptReason) || strings.Contains(logs.String(), "nosuch-parent-xyz") {
		t.Fatalf("log = %s", logs.String())
	}
}

// A config apply (inline kits) refuses a kit still on a pre-rename name, with
// the same text as a push.
func TestConfigPlanRefusesPreRenameKitNames(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"kits/web/kit.yml": parentYML, "kits/web/Dockerfile": oldDockerfile})
	p := gitPlanOn(t, st, tr, cfgSrc, "c1")
	want := "kit web: Dockerfile uses " + legacynames.KitRenames[2].Old + ", renamed JAM_STUDIO_BASE_IMAGE (docs/usage/jam/renamed-from-at.md)"
	if !slices.ContainsFunc(p.Refusals, func(r string) bool { return strings.Contains(r, want) }) {
		t.Fatalf("refusals = %v, want one containing %q", p.Refusals, want)
	}
}

// A kit-set import from git refuses the same way.
func TestKitImportRefusesPreRenameKitNames(t *testing.T) {
	st := NewMemStore()
	f := setFixture(t, map[string]string{"app/kit.yml": parentYML, "app/Dockerfile": oldDockerfile})
	_, err := ImportKit(context.Background(), st, f, nil, "", setSrc("app"), time.Unix(10, 0))
	if err == nil || !strings.Contains(err.Error(), "Dockerfile uses "+legacynames.KitRenames[2].Old+", renamed JAM_STUDIO_BASE_IMAGE") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := st.GetKit("app"); ok {
		t.Fatal("nothing may be stored")
	}
}
