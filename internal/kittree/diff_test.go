package kittree

import (
	"strings"
	"testing"
)

func build(t *testing.T, files ...File) (Tree, map[string][]byte) {
	t.Helper()
	tr, blobs, err := Build(files)
	if err != nil {
		t.Fatal(err)
	}
	return tr, blobs
}

func TestChanges(t *testing.T) {
	a, _ := build(t, File{Path: KitFile, Data: []byte("k")}, File{Path: "gone", Data: []byte("1")}, File{Path: "mod", Data: []byte("a")}, File{Path: "same", Data: []byte("s")}, File{Path: "x.sh", Data: []byte("x")})
	b, _ := build(t, File{Path: KitFile, Data: []byte("k")}, File{Path: "mod", Data: []byte("b")}, File{Path: "new", Data: []byte("2")}, File{Path: "same", Data: []byte("s")}, File{Path: "x.sh", Mode: 0o755, Data: []byte("x")})
	ch, unchanged := Changes(a, b)
	var got []string
	for _, c := range ch {
		got = append(got, c.Kind+" "+c.Path)
	}
	if strings.Join(got, ", ") != "removed gone, modified mod, added new, mode x.sh" || unchanged != 2 {
		t.Fatalf("changes = %v, unchanged = %d", got, unchanged)
	}
}

func TestDiffText(t *testing.T) {
	big := strings.Repeat("y\n", MaxDiffFile)
	a, ab := build(t,
		File{Path: KitFile, Data: []byte("kind: studio\nprompt: a\n")},
		File{Path: "Dockerfile", Data: []byte("FROM a\n")},
		File{Path: "bin", Data: []byte{0, 1}},
		File{Path: "big.txt", Data: []byte(big)},
		File{Path: "old.txt", Data: []byte("o")},
		File{Path: "t.sh", Data: []byte("t")})
	b, bb := build(t,
		File{Path: KitFile, Data: []byte("kind: studio\nprompt: b\n")},
		File{Path: "Dockerfile", Data: []byte("FROM b\n")},
		File{Path: "bin", Data: []byte{0, 2}},
		File{Path: "big.txt", Data: []byte(big + "z\n")},
		File{Path: "run.sh", Mode: 0o755, Data: []byte("echo\n")},
		File{Path: "t.sh", Mode: 0o755, Data: []byte("t")})
	blobs := func(sha string) ([]byte, bool) {
		if v, ok := ab[sha]; ok {
			return v, true
		}
		v, ok := bb[sha]
		return v, ok
	}
	want := `kit.yml:
   kind: studio
  -prompt: a
  +prompt: b
files: 6 changed, 0 unchanged
  modified  Dockerfile  0644  7 B → 7 B
     -FROM a
     +FROM b
  modified  big.txt  0644  131072 B → 131074 B  (too large to diff)
  modified  bin  0644  2 B → 2 B  (binary)
  removed   old.txt  0644  1 B
  added     run.sh  0755  5 B
  mode      t.sh  0644 → 0755
`
	if got := DiffText(a, b, blobs); got != want {
		t.Fatalf("DiffText =\n%s\nwant\n%s", got, want)
	}
	if got := DiffText(a, a, blobs); got != "no changes\n" {
		t.Fatalf("same = %q", got)
	}
}

func TestIsText(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"plain\n", true},
		{"tab\there\r\n", true},
		{"", true},
		{"nul\x00", false},
		{"esc \x1b[31m", false},
		{"bell\x07", false},
		{"del\x7f", false},
		{"c1 \u009b", false},
		{"\xff", false},
	} {
		if got := IsText([]byte(c.in)); got != c.want {
			t.Errorf("IsText(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDiffTextEscapesLines(t *testing.T) {
	a, ab := build(t, File{Path: KitFile, Data: []byte("prompt: \u202ea\n")}, File{Path: "f", Data: []byte("x\r\n")}, File{Path: "e", Data: []byte("a")})
	b, bb := build(t, File{Path: KitFile, Data: []byte("prompt: \u2028b\tc\n")}, File{Path: "f", Data: []byte("y\r\n")}, File{Path: "e", Data: []byte("\x1b]0;pwn\x07")})
	blobs := func(sha string) ([]byte, bool) {
		if v, ok := ab[sha]; ok {
			return v, true
		}
		v, ok := bb[sha]
		return v, ok
	}
	want := "kit.yml:\n" +
		"  -prompt: \\u202ea\n" +
		"  +prompt: \\u2028b\tc\n" +
		"files: 2 changed, 0 unchanged\n" +
		"  modified  e  0644  1 B → 8 B  (binary)\n" +
		"  modified  f  0644  3 B → 3 B\n" +
		"     -x\\r\n" +
		"     +y\\r\n"
	got := DiffText(a, b, blobs)
	if got != want {
		t.Fatalf("DiffText =\n%q\nwant\n%q", got, want)
	}
	if strings.ContainsAny(got, "\x1b\x07\r\u202e\u2028") {
		t.Fatalf("raw control or format character shown: %q", got)
	}
}

func TestDiffTextKitYMLBinary(t *testing.T) {
	a, ab := build(t, File{Path: KitFile, Data: []byte("kind: studio\n")})
	b, bb := build(t, File{Path: KitFile, Data: []byte("kind: \x1b[2Jstudio\n")})
	blobs := func(sha string) ([]byte, bool) {
		if v, ok := ab[sha]; ok {
			return v, true
		}
		v, ok := bb[sha]
		return v, ok
	}
	if got := DiffText(a, b, blobs); got != "kit.yml:\n  (binary)\n" {
		t.Fatalf("DiffText = %q", got)
	}
}
