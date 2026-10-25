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

func TestReadFileInRootReadsInside(t *testing.T) {
	root := t.TempDir()
	jailWrite(t, root, "web/kit.yml", "kind: studio\n")
	_ = os.Symlink("kit.yml", filepath.Join(root, "web", "ln.yml"))
	for _, rel := range []string{"web/kit.yml", "web/ln.yml"} {
		if b, err := ReadFileInRoot(root, rel); err != nil || string(b) != "kind: studio\n" {
			t.Fatalf("%s: %q %v", rel, b, err)
		}
	}
}

func TestReadFileInRootRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	jailWrite(t, outside, "secret.txt", "top secret")
	ob := filepath.Base(outside)
	const missing = ": is missing or resolves outside the checkout"
	link := func(target, rel string) func(string) {
		return func(r string) {
			_ = os.MkdirAll(filepath.Join(r, "web"), 0o755)
			_ = os.Symlink(target, filepath.Join(r, rel))
		}
	}
	for name, c := range map[string]struct {
		setup func(root string)
		rel   string
		want  string
	}{
		"symlink":          {link(filepath.Join(outside, "secret.txt"), "web/n.md"), "web/n.md", `file "web/n.md"` + missing},
		"dangling symlink": {link(filepath.Join(outside, "nope"), "web/n.md"), "web/n.md", `file "web/n.md"` + missing},
		"dir symlink":      {link(outside, "web/d"), "web/d/secret.txt", `file "web/d/secret.txt"` + missing},
		"up":               {func(string) {}, "../" + ob + "/secret.txt", `file "../` + ob + `/secret.txt"` + missing},
		"absolute":         {func(string) {}, filepath.Join(outside, "secret.txt"), "file: must be a relative path"},
		"missing":          {func(string) {}, "nope.md", `file "nope.md"` + missing},
		"directory":        {func(r string) { _ = os.MkdirAll(filepath.Join(r, "d"), 0o755) }, "d", `file "d": not a regular file`},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			c.setup(root)
			_, err := ReadFileInRoot(root, c.rel)
			if err == nil {
				t.Fatal("escape accepted")
			}
			if strings.Contains(err.Error(), "top secret") || (name != "absolute" && strings.Contains(err.Error(), outside)) {
				t.Fatalf("error leaks outside content/path: %v", err)
			}
			if err.Error() != c.want {
				t.Fatalf("refusal = %q, want %q", err, c.want)
			}
		})
	}
}

func TestReadFileInRootCapsSize(t *testing.T) {
	root := t.TempDir()
	jailWrite(t, root, "big.yml", strings.Repeat("x", maxRootFile+1))
	if _, err := ReadFileInRoot(root, "big.yml"); err == nil || err.Error() != `file "big.yml": larger than 4194304 bytes` {
		t.Fatalf("err = %v", err)
	}
}
