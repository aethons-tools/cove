package colimacfg

import (
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nx\nc\nd\n"))
	want := "  a\n- b\n+ x\n  c\n+ d\n"
	if got != want {
		t.Fatalf("Diff:\n%s\nwant:\n%s", got, want)
	}
	long := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	got = Diff([]byte(long), []byte(strings.Replace(strings.Replace(long, "2\n", "two\n", 1), "11\n", "eleven\n", 1)))
	want = "  1\n- 2\n+ two\n  3\n  4\n  5\n@@\n  8\n  9\n  10\n- 11\n+ eleven\n  12\n"
	if got != want {
		t.Fatalf("hunked Diff:\n%s\nwant:\n%s", got, want)
	}
	if got := Diff(nil, []byte("a\n")); got != "+ a\n" {
		t.Fatalf("Diff from empty = %q", got)
	}
}
