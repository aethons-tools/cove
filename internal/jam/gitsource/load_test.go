package gitsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeKit(t *testing.T, root, dir, body string) {
	t.Helper()
	writeFile(t, root, filepath.Join(dir, "kit.yml"), "kind: studio\n"+body)
}

const missingOrOutside = ": is missing or resolves outside the checkout"

const inside = ": does not resolve to a path inside the checkout"

// escapeRefusals is the exact refusal for each TestLoadRefusesEscapes case.
var escapeRefusals = map[string]string{
	"context-dir up":       `base.context-dir "../../OUTSIDE/ctx"` + inside,
	"context-dir absolute": "base.context-dir: must be a relative path",
	"context-dir symlink":  `base.context-dir "img"` + inside,
	"note file symlink":    `note "n.md" file "n.md"` + inside,
	"kit.yml symlink":      `file "web/kit.yml"` + missingOrOutside,
	"dir symlink":          `file "web/kit.yml"` + missingOrOutside,
	"kit.yml missing":      `file "web/kit.yml"` + missingOrOutside,
}

func TestLoadRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o600)
	os.MkdirAll(filepath.Join(outside, "ctx"), 0o755)
	os.WriteFile(filepath.Join(outside, "ctx", "Dockerfile"), []byte("FROM ${COVE_BASE_IMAGE}\n"), 0o644)
	for name, setup := range map[string]func(root string){
		"context-dir up":       func(r string) { writeKit(t, r, "web", "base:\n  context-dir: ../../"+filepath.Base(outside)+"/ctx\n") },
		"context-dir absolute": func(r string) { writeKit(t, r, "web", "base:\n  context-dir: "+filepath.Join(outside, "ctx")+"\n") },
		"context-dir symlink": func(r string) {
			os.MkdirAll(filepath.Join(r, "web"), 0o755)
			os.Symlink(filepath.Join(outside, "ctx"), filepath.Join(r, "web", "img"))
			writeKit(t, r, "web", "base:\n  context-dir: img\n")
		},
		"note file symlink": func(r string) {
			os.MkdirAll(filepath.Join(r, "web"), 0o755)
			os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "n.md"))
			writeKit(t, r, "web", "notes:\n  - name: n.md\n    file: n.md\n")
		},
		"kit.yml symlink": func(r string) {
			os.MkdirAll(filepath.Join(r, "web"), 0o755)
			os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "kit.yml"))
		},
		"dir symlink":     func(r string) { os.Symlink(outside, filepath.Join(r, "web")) },
		"kit.yml missing": func(r string) { os.MkdirAll(filepath.Join(r, "web"), 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(root)
			_, _, err := Load(root, "web")
			if err == nil {
				t.Fatal("escape accepted")
			}
			if strings.Contains(err.Error(), "top secret") || strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks outside content/path: %v", err)
			}
			if want := strings.ReplaceAll(escapeRefusals[name], "OUTSIDE", filepath.Base(outside)); err.Error() != want {
				t.Fatalf("refusal = %q, want %q", err, want)
			}
		})
	}
}

func TestLoadResolvesInsideCheckout(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "web", "name: web-dev\nbase:\n  context-dir: image\nnotes:\n  - name: guide.md\n    read-when: always\n    file: docs/guide.md\n")
	writeFile(t, root, "web/image/Dockerfile", "FROM ${COVE_BASE_IMAGE}\nRUN true\n")
	writeFile(t, root, "web/docs/guide.md", "hello")
	sk, hint, err := Load(root, "web")
	if err != nil || hint != "web-dev" || sk.Base.Context == "" || sk.Notes[0].Body != "hello" {
		t.Fatalf("%v %q %+v", err, hint, sk)
	}
}

func TestLoadNameHint(t *testing.T) {
	for dir, want := range map[string]string{"kits/web": "web", ".": ""} {
		root := t.TempDir()
		writeKit(t, root, dir, "prompt: hi\n")
		_, hint, err := Load(root, dir)
		if err != nil || hint != want {
			t.Fatalf("dir %q: hint %q err %v, want %q", dir, hint, err, want)
		}
	}
}

func TestLoadRefusesBadKit(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": "bogus: 1\n",
		"long prompt":   "prompt: " + strings.Repeat("x", 4096) + "\n",
		"tools note":    "notes:\n  - name: tools.md\n    body: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeKit(t, root, ".", body)
			if _, _, err := Load(root, "."); err == nil {
				t.Fatal("bad kit accepted")
			}
		})
	}
}

func TestLoadMissingKit(t *testing.T) {
	root := t.TempDir()
	_, _, err := Load(root, "nope")
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("want a missing-kit error without the root path, got %v", err)
	}
}
