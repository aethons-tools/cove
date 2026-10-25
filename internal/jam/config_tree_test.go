package jam

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
	"github.com/aethons-tools/cove/internal/studio"
)

// writeTree writes files (slash paths → content) under a fresh temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadConfigTreeKinds(t *testing.T) {
	root := writeTree(t, map[string]string{
		"README.md":                 "ignored",
		"jam.yml":                   "display-name: Aethon\n",
		"context.md":                "Be kind.\n",
		"destinations/gh.yml":       "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n",
		"users/bob.yml":             "kind: user\nversion: 1\n",
		"connections/acme.yml":      "kind: connection\nversion: 1\nservice: linear\n",
		"model-specs/fast.yml":      "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: a}\n",
		"kits/web-dev/kit.yml":      "kind: studio\negress: [registry.npmjs.org]\n",
		"kits/app/kit.yml":          "kind: studio\nbase: {kit: ../web-dev}\n",
		"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: bob}]\n",
		"projects/app/context.md":   "Ship it.\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: app\n",
	})
	tr, err := LoadConfigTree(root)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("err=%v refusals=%v", err, tr.Refusals)
	}
	if tr.Serve["display-name"] != "Aethon" || tr.JamContext.Core != "Be kind." || tr.Projects["app"].Context.Core != "Ship it." {
		t.Fatalf("tree %+v", tr)
	}
	if len(tr.Kits) != 2 || tr.Kits[0].Name != "web-dev" || tr.Kits[1].Name != "app" || tr.Kits[1].Dir != "kits/app" {
		t.Fatalf("kits %+v", tr.Kits)
	}
	if _, ok := tr.Roles["app/ic"]; !ok {
		t.Fatal("role")
	}
}

func TestLoadConfigTreeRefusals(t *testing.T) {
	root := writeTree(t, map[string]string{
		"users/bob.yaml":            "kind: user\nversion: 1\n",
		"users/sub/x.yml":           "kind: user\nversion: 1\n",
		"users/eve.yml":             "kind: user\nversion: 1\noidc: []\n",
		"kits/web.source.yml":       "kind: kit-source\n",
		"kits/stray.yml":            "x",
		"kits/empty/README":         "x",
		"projects/x/notes.txt":      "x",
		"projects/y/context.md":     "orphan\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web@v2\n",
	})
	tr, err := LoadConfigTree(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kits/empty: no kit.yml",
		"kits/stray.yml: unexpected file (a kit is kits/<name>/kit.yml or kits/<name>.source.yml)",
		"kits/web.source.yml: version must be 1",
		"projects/app/roles/ic.yml: kit web@v2 pins a version; versions are per-Jam — name the kit",
		"projects/x/notes.txt: unexpected file",
		"projects/y/context.md: no projects/y/project.yml",
		"users/bob.yaml: use .yml",
		`users/eve.yml: unknown field "oidc"`,
		"users/sub: unexpected directory",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
}

func TestLoadKitsRefusesMisplacedParent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shared/kit.yml":      "kind: studio\n",
		"kits/app/kit.yml":    "kind: studio\nbase: {kit: ../../shared}\n",
		"kits/web/kit.yml":    "kind: studio\nname: web-dev\n",
		"kits/pinned/kit.yml": "kind: studio\nbase: {kit: web@v3}\n",
		"kits/ok/kit.yml":     "kind: studio\n",
	})
	tr, _ := LoadConfigTree(root)
	joined := strings.Join(tr.Refusals, "\n")
	for _, want := range []string{
		"kits/app/kit.yml: parent ../../shared is not a kit under kits/ (kits/<name>/kit.yml)",
		"kits/web/kit.yml: ", // CheckName's own text follows: the name: differs from the directory
		"kits/pinned/kit.yml: base.kit web@v3 pins a version; versions are per-Jam — name the kit or use a path parent",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(tr.Kits) != 1 || tr.Kits[0].Name != "ok" {
		t.Fatalf("only the clean kit loads: %+v", tr.Kits)
	}
}

func TestLoadKitsOrderByRegistryParentInTree(t *testing.T) {
	root := writeTree(t, map[string]string{
		"kits/a/kit.yml": "kind: studio\nbase: {kit: z}\n", // follows z, which is in the tree
		"kits/z/kit.yml": "kind: studio\n",
	})
	tr, _ := LoadConfigTree(root)
	if len(tr.Kits) != 2 || tr.Kits[0].Name != "z" {
		t.Fatalf("order %+v refusals %v", tr.Kits, tr.Refusals)
	}
}

func TestLoadKitsRefusesRegistryCycle(t *testing.T) {
	root := writeTree(t, map[string]string{
		"kits/a/kit.yml":  "kind: studio\nbase: {kit: b}\n",
		"kits/b/kit.yml":  "kind: studio\nbase: {kit: a}\n",
		"kits/ok/kit.yml": "kind: studio\n",
	})
	tr, _ := LoadConfigTree(root)
	if !slices.Equal(tr.Refusals, []string{"kits: cycle among a, b"}) {
		t.Fatalf("refusals %v", tr.Refusals)
	}
	if len(tr.Kits) != 1 || tr.Kits[0].Name != "ok" {
		t.Fatalf("kits %+v", tr.Kits)
	}
}

func TestLoadConfigTreeBadJamYML(t *testing.T) {
	root := writeTree(t, map[string]string{"jam.yml": "a: [\n"})
	tr, err := LoadConfigTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tr.Refusals, []string{"jam.yml: not valid YAML"}) || tr.Serve != nil {
		t.Fatalf("refusals %v serve %v", tr.Refusals, tr.Serve)
	}
}

func TestLoadConfigTreeUnusableRoot(t *testing.T) {
	if _, err := LoadConfigTree(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want error")
	}
}

func TestResolveKitEntriesForest(t *testing.T) {
	st := NewMemStore()
	set := []kitSetEntry{
		{Name: "base", Dir: "kits/base", Kit: studio.StudioKit{Kind: "studio"}, files: kittreetest.Files("kit.yml", "kind: studio\n")},
		{Name: "a", Dir: "kits/a", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}, files: kittreetest.Files("kit.yml", "kind: studio\nbase: {kit: ../base}\n")},
		{Name: "b", Dir: "kits/b", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}, files: kittreetest.Files("kit.yml", "kind: studio\nbase: {kit: ../base}\n")},
	}
	out, err := resolveKitEntries(st, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[1].Kit.Base.Kit != "base@v1" || out[2].Kit.Base.Kit != "base@v1" || string(kitYMLOf(out[2].files)) != "kind: studio\nbase: {kit: base@v1}\n" {
		t.Fatalf("siblings not both pinned to base: %+v", out)
	}
}

func TestLoadConfigTreeUnsafeEntryNames(t *testing.T) {
	root := writeTree(t, map[string]string{
		"users/bad\nname.yml":          "x",
		"kits/\x1b[31mred/kit.yml":     "kind: studio\n",
		"projects/app/roles/a b.yml":   "x",
		"destinations/ok.yml.bak":      "x",
		"\x1b[2Jtop-level-ignored.txt": "x",
	})
	tr, err := LoadConfigTree(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"destinations/ok.yml.bak: unexpected file",
		"kits: an entry has an unsafe name",
		"projects/app/roles: an entry has an unsafe name",
		"users: an entry has an unsafe name",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%q", tr.Refusals)
	}
	for _, r := range tr.Refusals {
		if strings.ContainsAny(r, "\x1b\n") || strings.Contains(r, "a b") || strings.Contains(r, "red") {
			t.Fatalf("refusal echoes a raw name: %q", r)
		}
	}
}

func TestLoadConfigTreeRefusesBadProjectDirName(t *testing.T) {
	id := ident.New(ident.Project).String() // an id-shaped name: checkProjectName refuses it
	root := writeTree(t, map[string]string{
		"projects/" + id + "/roles/ic.yml": "kind: role\nversion: 1\n",
	})
	tr, _ := LoadConfigTree(root)
	if !slices.Equal(tr.Refusals, []string{"projects/" + id + ": not a valid project name"}) || len(tr.Roles) != 0 {
		t.Fatalf("refusals %v roles %v", tr.Refusals, tr.Roles)
	}
}

func TestLoadKitsRefusesSymlinkedAncestor(t *testing.T) {
	root := writeTree(t, map[string]string{
		"elsewhere/kit.yml": "kind: studio\n",
		"kits/app/kit.yml":  "kind: studio\nbase: {kit: ../web}\n",
	})
	if err := os.Symlink("../elsewhere", filepath.Join(root, "kits", "web")); err != nil {
		t.Skip(err)
	}
	tr, _ := LoadConfigTree(root)
	joined := strings.Join(tr.Refusals, "\n")
	for _, want := range []string{
		"kits/app/kit.yml: kits/web: not a directory", // ReadDir never follows the symlinked dir
		"kits/web: unexpected file (a kit is kits/<name>/kit.yml or kits/<name>.source.yml)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(tr.Kits) != 0 {
		t.Fatalf("kits %+v", tr.Kits)
	}
}

func TestLoadConfigTreeKitSources(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"kits/app.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n",
	})
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	ref := tr.KitSources["app"]
	if ref.Path != "kits/app.source.yml" || ref.Source != (gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main"}) ||
		ref.Commit != "" || len(tr.Kits) != 0 || tr.KitsFetched {
		t.Fatalf("%+v kits=%v fetched=%v", ref, tr.Kits, tr.KitsFetched)
	}
}

func TestLoadConfigTreeKitSourceRefusals(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"kits/web/kit.yml":      "kind: studio\n",
		"kits/web.source.yml":   "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//web@main\n",
		"kits/nogit.source.yml": "kind: kit-source\nversion: 1\n",
		"kits/other.source.yml": "kind: kit-source\nversion: 1\nname: else\ngit: https://github.com/acme/kits@main\n",
		"kits/extra.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits@main\nref: main\n",
		"kits/x.source.yaml":    "kind: kit-source\n",
	})
	want := []string{
		`kits/extra.source.yml: unknown field "ref"`,
		"kits/nogit.source.yml: git is required (<repo>[//<dir>]@<ref>)",
		`kits/other.source.yml: name "else" does not match the file name "other"`,
		"kits/web.source.yml: kit web is also kits/web/kit.yml",
		"kits/x.source.yaml: use .yml",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
	if _, ok := tr.KitSources["web"]; ok {
		t.Fatal("a refused duplicate is kept as a kit source")
	}
}

func TestResolveKitEntriesPathParentsStayInTheirGroup(t *testing.T) {
	set := []kitSetEntry{
		{Name: "web", Dir: "kits/web", Kit: studio.StudioKit{Kind: "studio"}, files: kittreetest.Files("kit.yml", "kind: studio\n")},
		{Name: "web2", Group: "g", Dir: "kits/web", Kit: studio.StudioKit{Kind: "studio"}, files: kittreetest.Files("kit.yml", "kind: studio\n# 2\n")}, // the same dir in another checkout
		{Name: "app", Group: "g", Dir: "kits/app", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../web"}}, files: kittreetest.Files("kit.yml", "kind: studio\nbase: {kit: ../web}\n")},
	}
	out, err := resolveKitEntries(NewMemStore(), set, nil)
	if err != nil || out[2].Kit.Base.Kit != "web2@v1" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestOrderKitEntriesAcrossGroups(t *testing.T) {
	in := []kitSetEntry{
		{Name: "app", Group: "g", Dir: "app", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../web"}}},
		{Name: "web", Group: "g", Dir: "web", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "base"}}},
		{Name: "base", Dir: "kits/base", Kit: studio.StudioKit{Kind: "studio"}},
		{Name: "x", Dir: "kits/x", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "y"}}},
		{Name: "y", Dir: "kits/y", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "x"}}},
	}
	out, cycle := orderKitEntries(in)
	var names []string
	for _, e := range out {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"base", "web", "app"}) || !slices.Equal(cycle, []string{"x", "y"}) {
		t.Fatalf("order %v cycle %v", names, cycle)
	}
}

// A config repo written by an older Jam (kit.yml with base.context-dir and a
// context/ dir beside it) is refused with a fixed hint to rewrite it with
// config write.
func TestLoadKitsHintsConfigWriteForOldContextField(t *testing.T) {
	root := writeTree(t, map[string]string{
		"kits/app/kit.yml":            "kind: studio\nbase:\n  context-dir: context\n",
		"kits/app/context/Dockerfile": "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n",
		"kits/files/kit.yml":          "kind: studio\nbase:\n  context-files: {dockerfile: x}\n",
		"kits/bad/kit.yml":            "kind: studio\nworkers: {}\n",
	})
	const hint = "; a config repo written by an older Jam: run at-jam config write <repo> and commit"
	tr, _ := LoadConfigTree(root)
	joined := strings.Join(tr.Refusals, "\n")
	for _, want := range []string{
		"kits/app/kit.yml: base.context-dir is no longer supported; put a Dockerfile next to kit.yml" + hint,
		"kits/files/kit.yml: base.context-files is no longer supported; put a Dockerfile next to kit.yml" + hint,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, hint) != 2 {
		t.Errorf("the hint is only for the context fields:\n%s", joined)
	}
}
