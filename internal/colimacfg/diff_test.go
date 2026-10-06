package colimacfg

import "testing"

func TestDiff(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nx\nc\nd\n"))
	want := "  a\n- b\n+ x\n  c\n+ d\n"
	if got != want {
		t.Fatalf("Diff:\n%s\nwant:\n%s", got, want)
	}
	if got := Diff(nil, []byte("a\n")); got != "+ a\n" {
		t.Fatalf("Diff from empty = %q", got)
	}
}
