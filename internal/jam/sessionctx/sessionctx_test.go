package sessionctx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func compileKit(core string, leaves ...Leaf) Bundle {
	return Compile(Inputs{Session: SessionFacts{Kind: KindStanding, Name: "s", Project: "p", Role: "r", Kit: "web@v3"}, Kit: Layer{Core: core, Leaves: leaves}})
}

func TestCompileHeaderAndOrder(t *testing.T) {
	b := compileKit("KITCORE")
	if !strings.HasPrefix(b.Core, "# Session context\n") {
		t.Fatalf("core must open with the header:\n%s", b.Core)
	}
	if !strings.Contains(b.Core, Precedence) {
		t.Fatal("core must state precedence verbatim")
	}
	kit := strings.Index(b.Core, "## Kit — web@v3")
	if kit < 0 || !strings.Contains(b.Core[kit:], "KITCORE") {
		t.Fatalf("kit section missing:\n%s", b.Core)
	}
	if bp := strings.Index(b.Core, "## Boilerplate"); bp >= 0 && bp > kit {
		t.Fatal("boilerplate must precede kit")
	}
}

func TestCompileOmitsEmptyLayer(t *testing.T) {
	b := compileKit("   \n")
	if strings.Contains(b.Core, "## Kit") {
		t.Fatalf("empty kit layer must emit nothing:\n%s", b.Core)
	}
	if _, ok := b.Layers[LayerKit]; ok {
		t.Fatal("empty layer must have no fingerprint entry")
	}
}

func TestCompileLeavesListedAndWritten(t *testing.T) {
	b := compileKit("K", Leaf{Name: "tools.md", ReadWhen: "you need a tool version", Body: "Go 1.27"})
	if !strings.Contains(b.Core, "- kit/tools.md — read when you need a tool version") {
		t.Fatalf("core must list the leaf:\n%s", b.Core)
	}
	f, ok := b.Files["kit/tools.md"]
	if !ok || !strings.Contains(f, "read_when: you need a tool version") || !strings.HasSuffix(strings.TrimSpace(f), "Go 1.27") {
		t.Fatalf("leaf file wrong: %q", f)
	}
	idx := b.Files["INDEX.md"]
	if !strings.Contains(idx, "| kit/tools.md | you need a tool version |") {
		t.Fatalf("INDEX must list every leaf:\n%s", idx)
	}
	for p := range b.Files {
		if p != "INDEX.md" && !strings.Contains(idx, "| "+p+" |") {
			t.Errorf("file %s not in INDEX", p)
		}
	}
}

func TestCompileDropsUnsafeLeafNames(t *testing.T) {
	for _, bad := range []string{"../x.md", "a/b.md", "/etc/x.md", "", ".md", "x.txt", "CORE.md"} {
		b := compileKit("K", Leaf{Name: bad, ReadWhen: "w", Body: "b"})
		for p := range b.Files {
			if strings.HasPrefix(p, LayerKit+"/") {
				t.Errorf("%q: unsafe leaf written as %q", bad, p)
			}
		}
		if len(b.Warnings) == 0 {
			t.Errorf("%q: want a warning", bad)
		}
	}
}

func TestCompileTruncatesOverBudget(t *testing.T) {
	long := strings.Repeat("line of kit text\n", 100) // 1700 bytes > 800
	b := compileKit(long)
	sec := b.Core[strings.Index(b.Core, "## Kit"):]
	if !strings.Contains(sec, "(truncated — see kit/CORE-full.md)") {
		t.Fatalf("want truncation note:\n%s", sec)
	}
	if b.Files["kit/CORE-full.md"] == "" || !strings.Contains(b.Files["kit/CORE-full.md"], strings.TrimSpace(long)) {
		t.Fatal("full core must be kept as a leaf")
	}
	if len(b.Warnings) == 0 {
		t.Fatal("truncation must warn")
	}
}

func TestTruncateNeverSplitsARune(t *testing.T) {
	s := strings.Repeat("é", 1000) // 2000 bytes, no newline
	got := truncateCore(s, 800)
	if !utf8.ValidString(got) || len(got) > 800 {
		t.Fatalf("invalid or over-budget: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestCompileDeterministic(t *testing.T) {
	a := compileKit("K", Leaf{Name: "b.md", ReadWhen: "w", Body: "x"}, Leaf{Name: "a.md", ReadWhen: "w", Body: "y"})
	for range 20 {
		if b := compileKit("K", Leaf{Name: "b.md", ReadWhen: "w", Body: "x"}, Leaf{Name: "a.md", ReadWhen: "w", Body: "y"}); b.Fingerprint != a.Fingerprint || b.Core != a.Core {
			t.Fatal("same inputs must give the same bytes")
		}
	}
	if c := compileKit("K2"); c.Fingerprint == a.Fingerprint {
		t.Fatal("different inputs must change the fingerprint")
	}
}
