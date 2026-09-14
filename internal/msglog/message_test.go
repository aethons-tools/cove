package msglog

import (
	"sort"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := map[string]Reach{"actor": Internal, "human": External, "channel": External}
	for kind, want := range cases {
		if got := Classify(Target{Kind: kind, Ref: "x"}); got != want {
			t.Errorf("Classify(%s)=%v want %v", kind, got, want)
		}
	}
}

func TestTargetString(t *testing.T) {
	if got := (Target{Kind: "actor", Ref: "cove-1"}).String(); got != "actor:cove-1" {
		t.Fatalf("String()=%q", got)
	}
}

func TestNewIDTimeSortable(t *testing.T) {
	a := newID(time.Unix(0, 100))
	b := newID(time.Unix(0, 200))
	ids := []string{b, a}
	sort.Strings(ids)
	if ids[0] != a || ids[1] != b {
		t.Fatalf("IDs not time-sortable: %v", ids)
	}
	if a == newID(time.Unix(0, 100)) {
		t.Fatal("IDs at the same instant must still differ (random suffix)")
	}
}

func TestMessageValidate(t *testing.T) {
	good := Message{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"}
	if err := good.validate(); err != nil {
		t.Fatalf("good message rejected: %v", err)
	}
	bad := []Message{
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}},             // empty body
		{From: Target{Kind: "actor", Ref: "a"}, Body: "hi"},                                          // empty To
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "bogus", Ref: "b"}}, Body: "hi"}, // bad To kind
		{From: Target{Kind: "", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"},      // bad From
		{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: ""}}, Body: "hi"},  // empty To ref
	}
	for i, m := range bad {
		if err := m.validate(); err == nil {
			t.Errorf("bad message %d accepted", i)
		}
	}
}
