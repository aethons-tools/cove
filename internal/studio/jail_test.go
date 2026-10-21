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
	// An invalid ignore file: were it read, the refusal would be its parse error.
	jailWrite(t, outside, "bad.dockerignore", "!\n")
	ob := filepath.Base(outside)
	const inside = ": does not resolve to a path inside the checkout"
	type tc struct {
		setup func(root string)
		sk    StudioKit
		dir   string
		want  string // the exact refusal
	}
	ctx := func(d string) StudioKit { return StudioKit{Kind: Kind, Base: Base{ContextDir: d}} }
	note := func(f string) StudioKit { return StudioKit{Kind: Kind, Notes: []KitNote{{Name: "n.md", File: f}}} }
	mk := func(r string) { _ = os.MkdirAll(filepath.Join(r, "web", "img"), 0o755) }
	link := func(target, rel string) func(string) {
		return func(r string) { mk(r); _ = os.Symlink(target, filepath.Join(r, rel)) }
	}
	cases := map[string]tc{
		"context-dir up":               {mk, ctx("../../" + ob + "/ctx"), "web", `base.context-dir "../../` + ob + `/ctx"` + inside},
		"context-dir absolute":         {mk, ctx(filepath.Join(outside, "ctx")), "web", "base.context-dir: must be a relative path"},
		"context-dir symlink":          {link(filepath.Join(outside, "ctx"), "web/ln"), ctx("ln"), "web", `base.context-dir "ln"` + inside},
		"context-dir dangling symlink": {link(filepath.Join(outside, "missing"), "web/ln"), ctx("ln"), "web", `base.context-dir "ln"` + inside},
		"dockerignore symlink": {func(r string) {
			link(filepath.Join(outside, "bad.dockerignore"), "web/img/.dockerignore")(r)
			jailWrite(t, r, "web/img/Dockerfile", "FROM ${COVE_BASE_IMAGE}\n")
		}, ctx("img"), "web", `base.context-dir "img": .dockerignore is not a regular file`},
		"dockerfile symlink":          {link(filepath.Join(outside, "ctx", "Dockerfile"), "web/img/Dockerfile"), ctx("img"), "web", `base.context-dir "img": Dockerfile is not a regular file`},
		"dockerfile dangling symlink": {link(filepath.Join(outside, "missing"), "web/img/Dockerfile"), ctx("img"), "web", `base.context-dir "img": Dockerfile is not a regular file`},
		"note symlink":                {link(filepath.Join(outside, "secret.txt"), "web/n.md"), note("n.md"), "web", `note "n.md" file "n.md"` + inside},
		"note absolute":               {mk, note(filepath.Join(outside, "secret.txt")), "web", `note "n.md": file must be a relative path inside the kit directory`},
		"note up":                     {mk, note("../../" + ob + "/secret.txt"), "web", `note "n.md": file must be a relative path inside the kit directory`},
		"note dir is a symlink":       {link(outside, "web/d"), note("d/secret.txt"), "web", `note "n.md" file "d/secret.txt"` + inside},
		"dir symlink":                 {func(r string) { _ = os.Symlink(outside, filepath.Join(r, "web")) }, note("secret.txt"), "web", `kit dir "web"` + inside},
		"dir up":                      {mk, note("secret.txt"), "../web", `kit dir "../web"` + inside},
		"dir absolute":                {mk, note("secret.txt"), outside, "kit dir: must be a relative path"},
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
			if err.Error() != c.want {
				t.Fatalf("refusal = %q, want %q", err, c.want)
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

func TestResolveInRootCapsNoteBytes(t *testing.T) {
	root := t.TempDir()
	half := strings.Repeat("x", maxNoteFileBytes/2+1)
	jailWrite(t, root, "a.md", half)
	jailWrite(t, root, "b.md", half)
	sk := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "a.md", File: "a.md"}, {Name: "b.md", File: "b.md"}}}
	err := sk.ResolveInRoot(root, ".")
	want := `notes: note files exceed the 1048576-byte total cap (at note "b.md")`
	if err == nil || err.Error() != want {
		t.Fatalf("refusal = %v, want %q", err, want)
	}
	if sk.Notes[0].Body != "" {
		t.Fatal("kit mutated on refusal")
	}
	one := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "a.md", File: "a.md"}}}
	if err := one.ResolveInRoot(root, "."); err != nil {
		t.Fatalf("one note under the cap refused: %v", err)
	}
}
