package agentrun

import (
	"strings"
	"testing"
)

type gotLine struct {
	s       string
	dropped uint64
}

func collect() (*lineSplitter, *[]gotLine) {
	var out []gotLine
	s := &lineSplitter{max: 8, emit: func(b []byte, d uint64) { out = append(out, gotLine{string(b), d}) }}
	return s, &out
}

func TestLineSplitterSplitsAcrossWrites(t *testing.T) {
	s, out := collect()
	s.Write([]byte("ab"))
	s.Write([]byte("c\nde\n\nf"))
	s.Flush()
	want := []gotLine{{"abc", 0}, {"de", 0}, {"f", 0}}
	if len(*out) != len(want) {
		t.Fatalf("got %+v", *out)
	}
	for i := range want {
		if (*out)[i] != want[i] {
			t.Fatalf("line %d: got %+v want %+v", i, (*out)[i], want[i])
		}
	}
}

func TestLineSplitterTruncatesAndCounts(t *testing.T) {
	s, out := collect()
	s.Write([]byte(strings.Repeat("x", 5)))
	s.Write([]byte(strings.Repeat("y", 10) + "\n"))
	if len(*out) != 1 || (*out)[0].s != "xxxxxyyy" || (*out)[0].dropped != 7 {
		t.Fatalf("got %+v", *out)
	}
}

func TestLineSplitterStripsCR(t *testing.T) {
	s, out := collect()
	s.Write([]byte("ok\r\n"))
	if (*out)[0].s != "ok" {
		t.Fatalf("got %q", (*out)[0].s)
	}
}

func TestLineSplitterWriteNeverErrors(t *testing.T) {
	s, _ := collect()
	n, err := s.Write([]byte(strings.Repeat("z", 100)))
	if n != 100 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
