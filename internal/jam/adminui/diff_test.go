package adminui

import (
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	b := "a\nb\nc\nd\nE\nf\ng\nh\ni\nj\nk\n"
	got, tooLarge := lineDiff(a, b)
	if tooLarge {
		t.Fatal("small diff reported too large")
	}
	var sb strings.Builder
	for _, l := range got {
		sb.WriteString(l.Op + l.Text + "\n")
	}
	want := "…1 unchanged line(s)\n b\n c\n d\n-e\n+E\n f\n g\n h\n i\n j\n+k\n"
	if sb.String() != want {
		t.Fatalf("diff =\n%s\nwant\n%s", sb.String(), want)
	}
	if got, _ := lineDiff("same\n", "same\n"); len(got) != 1 || got[0].Op != "…" {
		t.Errorf("identical inputs = %+v, want one collapsed run", got)
	}
}
