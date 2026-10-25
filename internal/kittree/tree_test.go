package kittree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// withLimits shrinks the tree limits for one test.
func withLimits(t *testing.T, entries int, total, file int64) {
	t.Helper()
	e, to, f := MaxEntries, MaxTotal, MaxFile
	MaxEntries, MaxTotal, MaxFile = entries, total, file
	t.Cleanup(func() { MaxEntries, MaxTotal, MaxFile = e, to, f })
}

func kitFiles(extra ...File) []File {
	return append([]File{{Path: KitFile, Mode: 0o644, Data: []byte("kind: studio\n")}}, extra...)
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestBuildSortsNormalizesAndHashes(t *testing.T) {
	tr, blobs, err := Build([]File{
		{Path: "run.sh", Mode: 0o700, Data: []byte("echo hi\n")},
		{Path: KitFile, Mode: 0o600, Data: []byte("kind: studio\n")},
		{Path: "sub/data.txt", Mode: 0o664, Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Path: KitFile, Mode: 0o644, SHA256: hexSum([]byte("kind: studio\n")), Size: 13},
		{Path: "run.sh", Mode: 0o755, SHA256: hexSum([]byte("echo hi\n")), Size: 8},
		{Path: "sub/data.txt", Mode: 0o644, SHA256: hexSum([]byte("x")), Size: 1},
	}
	if fmt.Sprint(tr.Entries) != fmt.Sprint(want) {
		t.Fatalf("entries = %+v\nwant %+v", tr.Entries, want)
	}
	if len(blobs) != 3 || string(blobs[hexSum([]byte("x"))]) != "x" {
		t.Fatalf("blobs = %v", blobs)
	}
	if tr.Size() != 22 {
		t.Fatalf("size = %d", tr.Size())
	}
}

func TestHashEncoding(t *testing.T) {
	tr, _, err := Build([]File{{Path: KitFile, Data: []byte("a")}, {Path: "x.sh", Mode: 0o755, Data: []byte("b")}})
	if err != nil {
		t.Fatal(err)
	}
	enc := "kit.yml\x00644\x00" + hexSum([]byte("a")) + "\n" + "x.sh\x00755\x00" + hexSum([]byte("b")) + "\n"
	if got := tr.Hash(); got != hexSum([]byte(enc)) {
		t.Fatalf("hash = %s, want sha256 of %q", got, enc)
	}
	same, _, _ := Build([]File{{Path: "x.sh", Mode: 0o711, Data: []byte("b")}, {Path: KitFile, Mode: 0o600, Data: []byte("a")}})
	if same.Hash() != tr.Hash() {
		t.Fatal("identical directories must share one tree hash")
	}
	other, _, _ := Build([]File{{Path: KitFile, Data: []byte("a")}, {Path: "x.sh", Mode: 0o644, Data: []byte("b")}})
	if other.Hash() == tr.Hash() {
		t.Fatal("a mode change must change the hash")
	}
}

func TestBuildRefusals(t *testing.T) {
	big := make([]File, 0, 2001)
	big = append(big, kitFiles()...)
	for i := 1; i < 2001; i++ {
		big = append(big, File{Path: fmt.Sprintf("f%04d", i), Data: []byte("x")})
	}
	for name, tc := range map[string]struct {
		files []File
		want  string
	}{
		"no kit.yml":     {[]File{{Path: "Dockerfile", Data: []byte("FROM x")}}, "kit.yml missing at the root"},
		"nested kit.yml": {[]File{{Path: "a/kit.yml", Data: []byte("x")}}, "kit.yml missing at the root"},
		"duplicate":      {kitFiles(File{Path: "a", Data: []byte("1")}, File{Path: "a", Data: []byte("2")}), "a: appears twice"},
		"file and dir":   {kitFiles(File{Path: "a", Data: []byte("1")}, File{Path: "a/b", Data: []byte("2")}), "a: is both a file and a directory"},
		"2001 files":     {big, "2001 files (limit 2000)"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Build(tc.files); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildRefusesUnsafePathsWithoutEchoing(t *testing.T) {
	hostile := map[string]bool{"bad\x1bname": true, "rtl\u202e.sh": true, "\xff": true}
	for _, p := range []string{"", ".", "/abs", "a/../b", "a//b", "a/", "./a", "bad\x1bname", "rtl\u202e.sh", "\xff", "ls\u2028x", "ps\u2029x", "un\U000e0001x"} {
		_, _, err := Build(kitFiles(File{Path: p, Data: []byte("x")}))
		if err == nil || !strings.HasPrefix(err.Error(), "a file has an unsafe path") {
			t.Fatalf("%q: err = %v", p, err)
		}
		if hostile[p] && strings.Contains(err.Error(), p) {
			t.Fatalf("%q: the error echoes the name: %v", p, err)
		}
	}
	if _, _, err := Build(kitFiles(File{Path: "with space/ok.txt", Data: []byte("x")})); err != nil {
		t.Fatalf("spaces are allowed: %v", err)
	}
}

func TestBuildSizeLimits(t *testing.T) {
	withLimits(t, 2000, 32, 16)
	if _, _, err := Build(kitFiles(File{Path: "big", Data: make([]byte, 17)})); err == nil || err.Error() != "big: more than 16 bytes (limit 16 bytes per file)" {
		t.Fatalf("per file: %v", err)
	}
	if _, _, err := Build(kitFiles(File{Path: "a", Data: make([]byte, 16)}, File{Path: "b", Data: make([]byte, 16)})); err == nil || err.Error() != "more than 32 bytes in total (limit 32 bytes)" {
		t.Fatalf("total: %v", err)
	}
	if SizeText(4<<20) != "4 MiB" || SizeText(16) != "16 bytes" {
		t.Fatal(SizeText(4<<20), SizeText(16))
	}
}

func TestContextHash(t *testing.T) {
	plain, _, _ := Build(kitFiles())
	if plain.ContextHash() != "" {
		t.Fatal("no Dockerfile: no build context")
	}
	a, _, _ := Build(kitFiles(File{Path: Dockerfile, Data: []byte("FROM x\n")}))
	if a.ContextHash() != a.Without(KitFile).Hash() || a.ContextHash() == "" {
		t.Fatal("the context is the tree minus kit.yml")
	}
	b, _, _ := Build([]File{{Path: KitFile, Data: []byte("kind: studio\nprompt: other\n")}, {Path: Dockerfile, Data: []byte("FROM x\n")}})
	if b.ContextHash() != a.ContextHash() {
		t.Fatal("editing kit.yml must not change the context")
	}
	c, _, _ := Build(kitFiles(File{Path: Dockerfile, Data: []byte("FROM y\n")}))
	if c.ContextHash() == a.ContextHash() {
		t.Fatal("editing the Dockerfile must change the context")
	}
}

func TestValidateRejectsTamperedTrees(t *testing.T) {
	good, _, _ := Build(kitFiles(File{Path: "z", Data: []byte("1")}))
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	swap := Tree{Entries: []Entry{good.Entries[1], good.Entries[0]}}
	mode := Tree{Entries: append([]Entry(nil), good.Entries...)}
	mode.Entries[1].Mode = 0o600
	sum := Tree{Entries: append([]Entry(nil), good.Entries...)}
	sum.Entries[1].SHA256 = "zz"
	upper := Tree{Entries: append([]Entry(nil), good.Entries...)}
	upper.Entries[1].SHA256 = strings.ToUpper(upper.Entries[1].SHA256)
	noKit := Tree{Entries: good.Entries[1:]}
	for name, tr := range map[string]Tree{"order": swap, "mode": mode, "sha": sum, "sha case": upper, "kit.yml": noKit} {
		if tr.Validate() == nil {
			t.Fatalf("%s: tampered tree validated", name)
		}
	}
	if e, ok := good.Lookup("z"); !ok || e.Size != 1 {
		t.Fatal("Lookup")
	}
	if _, ok := good.Lookup("y"); ok {
		t.Fatal("Lookup of a missing path")
	}
}

func TestBuildFilesNeedsNoKitYML(t *testing.T) {
	tr, blobs, err := BuildFiles([]File{{Path: "run.sh", Mode: 0o700, Data: []byte("echo\n")}})
	if err != nil || len(tr.Entries) != 1 || tr.Entries[0].Mode != 0o755 || len(blobs) != 1 {
		t.Fatalf("BuildFiles = %+v, %d blobs, %v", tr, len(blobs), err)
	}
	if err := tr.ValidateFiles(); err != nil {
		t.Fatalf("ValidateFiles = %v", err)
	}
	if err := tr.Validate(); err == nil || err.Error() != "kit.yml missing at the root" {
		t.Fatalf("Validate = %v, want the kit.yml refusal", err)
	}
	if _, _, err := Build([]File{{Path: "run.sh", Data: []byte("x")}}); err == nil || err.Error() != "kit.yml missing at the root" {
		t.Fatalf("Build = %v, want the kit.yml refusal", err)
	}
	empty, _, err := BuildFiles(nil)
	if err != nil || len(empty.Entries) != 0 || empty.ValidateFiles() != nil {
		t.Fatalf("an empty draft tree = %+v %v", empty, err)
	}
}

func TestBuildFilesKeepsTheFileRules(t *testing.T) {
	withLimits(t, 3, 6, 4)
	for name, tc := range map[string]struct {
		files []File
		want  string
	}{
		"unsafe path":     {[]File{{Path: "a\nb"}}, errUnsafePath.Error()},
		"too many":        {[]File{{Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"}}, "4 files (limit 3)"},
		"file too large":  {[]File{{Path: "a", Data: []byte("12345")}}, "a: more than 4 bytes (limit 4 bytes per file)"},
		"total too large": {[]File{{Path: "a", Data: []byte("1234")}, {Path: "b", Data: []byte("1234")}}, "more than 6 bytes in total (limit 6 bytes)"},
		"twice":           {[]File{{Path: "a"}, {Path: "a"}}, "a: appears twice"},
		"file and dir":    {[]File{{Path: "a"}, {Path: "a/b"}}, "a: is both a file and a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := BuildFiles(tc.files); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTreeWith(t *testing.T) {
	tr, _, err := BuildFiles([]File{{Path: "b", Data: []byte("1")}, {Path: "d", Data: []byte("2")}})
	if err != nil {
		t.Fatal(err)
	}
	got := tr.With(Entry{Path: "c", Mode: 0o644, SHA256: hexSum([]byte("3")), Size: 1})
	if paths := fmt.Sprint(entryPaths(got)); paths != "[b c d]" || len(tr.Entries) != 2 {
		t.Fatalf("With added = %s (original %d entries)", paths, len(tr.Entries))
	}
	repl := got.With(Entry{Path: "b", Mode: 0o755, SHA256: hexSum([]byte("x")), Size: 1})
	if e, _ := repl.Lookup("b"); e.Mode != 0o755 || len(repl.Entries) != 3 {
		t.Fatalf("With replaced = %+v", repl)
	}
	if e, _ := got.Lookup("b"); e.Mode != 0o644 {
		t.Fatal("With mutated its receiver")
	}
}

func entryPaths(t Tree) []string {
	var out []string
	for _, e := range t.Entries {
		out = append(out, e.Path)
	}
	return out
}
