package gitsource

import (
	"errors"
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

// escapeRefusals is the exact refusal for each TestLoadRefusesEscapes case:
// ReadDir refuses a symlink before anything reads through it.
var escapeRefusals = map[string]string{
	"note file up":       `note "n.md": file must be a relative path inside the kit directory`,
	"note file absolute": `note "n.md": file must be a relative path inside the kit directory`,
	"note file symlink":  "web/n.md: symlinks are not allowed",
	"kit.yml symlink":    "web/kit.yml: symlinks are not allowed",
	"dir symlink":        "web: not a directory",
	"kit.yml missing":    "web: kit.yml missing at the root",
}

func TestLoadRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o600)
	for name, setup := range map[string]func(root string){
		"note file up": func(r string) {
			writeKit(t, r, "web", "notes:\n  - name: n.md\n    file: ../../"+filepath.Base(outside)+"/secret.txt\n")
		},
		"note file absolute": func(r string) {
			writeKit(t, r, "web", "notes:\n  - name: n.md\n    file: "+filepath.Join(outside, "secret.txt")+"\n")
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
		"dir symlink": func(r string) { os.Symlink(outside, filepath.Join(r, "web")) },
		"kit.yml missing": func(r string) {
			os.MkdirAll(filepath.Join(r, "web"), 0o755)
			writeFile(t, r, "web/README", "x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(root)
			_, _, _, err := Load(root, "web")
			if err == nil {
				t.Fatal("escape accepted")
			}
			if strings.Contains(err.Error(), "top secret") || strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks outside content/path: %v", err)
			}
			if want := escapeRefusals[name]; err.Error() != want {
				t.Fatalf("refusal = %q, want %q", err, want)
			}
		})
	}
}

func TestLoadResolvesInsideCheckout(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "web", "name: web-dev\nnotes:\n  - name: guide.md\n    read-when: always\n    file: docs/guide.md\n")
	writeFile(t, root, "web/Dockerfile", "FROM ${COVE_BASE_IMAGE}\nRUN true\n")
	writeFile(t, root, "web/docs/guide.md", "hello")
	sk, _, hint, err := Load(root, "web")
	if err != nil || hint != "web-dev" || sk.BuildContext == "" || sk.Notes[0].Body != "hello" {
		t.Fatalf("%v %q %+v", err, hint, sk)
	}
}

func TestLoadReadsTheKitDirectory(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "app", "notes:\n  - {name: r.md, read-when: x, file: docs/r.md}\n")
	writeFile(t, root, "app/docs/r.md", "# R\n")
	writeFile(t, root, "app/Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n")
	writeFile(t, root, "app/.dockerignore", "*.log\n")
	writeFile(t, root, "app/x.log", "noise")
	if err := os.WriteFile(filepath.Join(root, "app/run.sh"), []byte("echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	sk, files, hint, err := Load(root, "app")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	if strings.Join(got, ",") != ".dockerignore,Dockerfile,docs/r.md,kit.yml,run.sh" || hint != "app" || sk.Notes[0].Body != "# R\n" || sk.BuildContext == "" {
		t.Fatalf("files %v hint %q kit %+v", got, hint, sk)
	}
}

func TestLoadRefusesSymlinkInKitDir(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "kits/app", "")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "kits/app/run.sh")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Load(root, "kits/app"); err == nil || err.Error() != "kits/app/run.sh: symlinks are not allowed" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRefusesPackedContextFields(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "app", "base:\n  context-dir: ctx\n")
	if _, _, _, err := Load(root, "app"); err == nil || err.Error() != "base.context-dir is no longer supported; put a Dockerfile next to kit.yml" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadNameHint(t *testing.T) {
	for dir, want := range map[string]string{"kits/web": "web", ".": ""} {
		root := t.TempDir()
		writeKit(t, root, dir, "prompt: hi\n")
		_, _, hint, err := Load(root, dir)
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
			if _, _, _, err := Load(root, "."); err == nil {
				t.Fatal("bad kit accepted")
			}
		})
	}
}

func TestLoadMissingKit(t *testing.T) {
	root := t.TempDir()
	_, _, _, err := Load(root, "nope")
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("want a missing-kit error without the root path, got %v", err)
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "web", "name: web-dev\n")
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	if _, _, hint, err := LoadDir(root, "app/../web"); err != nil || hint != "web-dev" {
		t.Fatalf("hint %q err %v", hint, err)
	}
	for dir, want := range map[string]error{"..": ErrOutsideCheckout, "../x": ErrOutsideCheckout, "/web": ErrOutsideCheckout, "empty": ErrNoKitFile, "nope": ErrNoKitFile} {
		if _, _, _, err := LoadDir(root, dir); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", dir, err, want)
		}
	}
}

func TestLoadDirRefusesUnsafeDir(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "my web", "name: web-dev\n")
	for _, dir := range []string{"my web", "w\x1b[31mX", "a'b"} {
		_, _, _, err := LoadDir(root, dir)
		if err == nil || errors.Is(err, ErrNoKitFile) || strings.Contains(err.Error(), dir) {
			t.Errorf("%q: got %v", dir, err)
		}
	}
}
