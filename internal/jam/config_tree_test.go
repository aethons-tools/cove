package jam

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
		"kits/stray.yml: unexpected file (kits are directories: kits/<name>/kit.yml)",
		"kits/web.source.yml: kit source references are not supported yet",
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
		{Name: "base", Dir: "kits/base", Kit: studio.StudioKit{Kind: "studio"}},
		{Name: "a", Dir: "kits/a", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}},
		{Name: "b", Dir: "kits/b", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}},
	}
	out, err := resolveKitEntries(st, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[1].Kit.Base.Kit != "base@v1" || out[2].Kit.Base.Kit != "base@v1" {
		t.Fatalf("siblings not both pinned to base: %+v", out)
	}
}
