package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jailWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveInRootPacksAndReadsInside(t *testing.T) {
	root := t.TempDir()
	jailWrite(t, root, "shared/image/Dockerfile", "FROM ${COVE_BASE_IMAGE}\n")
	jailWrite(t, root, "web/docs/guide.md", "hello")
	sk := StudioKit{Kind: Kind, Base: Base{ContextDir: "../shared/image"}, Notes: []KitNote{{Name: "guide.md", File: "docs/guide.md"}}}
	if err := sk.ResolveInRoot(root, "web"); err != nil {
		t.Fatalf("ResolveInRoot: %v", err)
	}
	if sk.Base.Context == "" || sk.Base.ContextDir != "" || sk.Notes[0].Body != "hello" || sk.Notes[0].File != "" {
		t.Fatalf("not resolved: %+v", sk)
	}
}

func TestResolveInRootRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	jailWrite(t, outside, "secret.txt", "top secret")
	jailWrite(t, outside, "ctx/Dockerfile", "FROM ${COVE_BASE_IMAGE}\n")
	type tc struct {
		setup func(root string)
		sk    StudioKit
		dir   string
	}
	ctx := func(d string) StudioKit { return StudioKit{Kind: Kind, Base: Base{ContextDir: d}} }
	note := func(f string) StudioKit { return StudioKit{Kind: Kind, Notes: []KitNote{{Name: "n.md", File: f}}} }
	mk := func(r string) { _ = os.MkdirAll(filepath.Join(r, "web"), 0o755) }
	cases := map[string]tc{
		"context-dir up":       {setup: mk, sk: ctx("../../" + filepath.Base(outside) + "/ctx"), dir: "web"},
		"context-dir absolute": {setup: mk, sk: ctx(filepath.Join(outside, "ctx")), dir: "web"},
		"context-dir symlink": {setup: func(r string) {
			mk(r)
			_ = os.Symlink(filepath.Join(outside, "ctx"), filepath.Join(r, "web", "img"))
		}, sk: ctx("img"), dir: "web"},
		"context-dir dangling symlink": {setup: func(r string) {
			mk(r)
			_ = os.Symlink(filepath.Join(outside, "missing"), filepath.Join(r, "web", "img"))
		}, sk: ctx("img"), dir: "web"},
		"dockerignore symlink": {setup: func(r string) {
			jailWrite(t, r, "web/img/Dockerfile", "FROM ${COVE_BASE_IMAGE}\n")
			_ = os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "img", ".dockerignore"))
		}, sk: ctx("img"), dir: "web"},
		"note symlink": {setup: func(r string) {
			mk(r)
			_ = os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "n.md"))
		}, sk: note("n.md"), dir: "web"},
		"note absolute": {setup: mk, sk: note(filepath.Join(outside, "secret.txt")), dir: "web"},
		"note up":       {setup: mk, sk: note("../../" + filepath.Base(outside) + "/secret.txt"), dir: "web"},
		"note dir is a symlink": {setup: func(r string) {
			mk(r)
			_ = os.Symlink(outside, filepath.Join(r, "web", "d"))
		}, sk: note("d/secret.txt"), dir: "web"},
		"dir symlink":  {setup: func(r string) { _ = os.Symlink(outside, filepath.Join(r, "web")) }, sk: note("secret.txt"), dir: "web"},
		"dir up":       {setup: mk, sk: note("secret.txt"), dir: "../" + filepath.Base(outside)},
		"dir absolute": {setup: mk, sk: note("secret.txt"), dir: outside},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			c.setup(root)
			sk := c.sk
			err := sk.ResolveInRoot(root, c.dir)
			if err == nil {
				t.Fatal("escape accepted")
			}
			if strings.Contains(err.Error(), "top secret") || strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks outside content/path: %v", err)
			}
			for _, n := range sk.Notes {
				if n.Body != "" {
					t.Fatalf("note body set despite refusal: %q", n.Body)
				}
			}
		})
	}
}

func TestResolveInRootPackErrorIsRootRelative(t *testing.T) {
	root := t.TempDir()
	jailWrite(t, root, "web/img/README", "no dockerfile")
	sk := StudioKit{Kind: Kind, Base: Base{ContextDir: "img"}}
	err := sk.ResolveInRoot(root, "web")
	if err == nil || !strings.Contains(err.Error(), "Dockerfile") || strings.Contains(err.Error(), root) {
		t.Fatalf("want a root-relative missing-Dockerfile error, got %v", err)
	}
	if sk.Base.ContextDir != "img" {
		t.Fatalf("kit mutated on refusal: %+v", sk.Base)
	}
}
