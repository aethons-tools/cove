package intercom

import (
	"testing"
	"time"
)

func seed(t *testing.T) *LegacyLog {
	t.Helper()
	l := NewLegacyMemLog()
	return l
}

func TestReadInboxMultiRecipient(t *testing.T) {
	l := seed(t)
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "actor", Ref: "a"}, {Kind: "human", Ref: "b"}}, Body: "x"})
	if n := len(l.ReadInbox(Target{Kind: "actor", Ref: "a"})); n != 1 {
		t.Fatalf("actor:a inbox=%d want 1", n)
	}
	if n := len(l.ReadInbox(Target{Kind: "human", Ref: "b"})); n != 1 {
		t.Fatalf("human:b inbox=%d want 1", n)
	}
	if n := len(l.ReadInbox(Target{Kind: "actor", Ref: "c"})); n != 0 {
		t.Fatalf("actor:c inbox=%d want 0", n)
	}
}

func TestReadInboxReturnsCopy(t *testing.T) {
	l := seed(t)
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "actor", Ref: "a"}}, Body: "x"})
	got := l.ReadInbox(Target{Kind: "actor", Ref: "a"})
	got[0].To[0].Ref = "mutated" // must not corrupt the store
	if l.ReadInbox(Target{Kind: "actor", Ref: "a"})[0].To[0].Ref != "a" {
		t.Fatal("ReadInbox leaked a mutable reference into the store")
	}
}

func TestReadThread(t *testing.T) {
	l := seed(t)
	root, _ := l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "root"})
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "human", Ref: "b"}, To: []Target{{Kind: "actor", Ref: "s"}}, Body: "reply", ReplyTo: root.ID})
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "unrelated"})
	th := l.ReadThread(root.ID)
	if len(th) != 2 || th[0].Body != "root" || th[1].Body != "reply" {
		t.Fatalf("thread=%+v want [root, reply]", th)
	}
}

func TestListFilter(t *testing.T) {
	l := seed(t)
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "acme", Project: "acme", At: time.Unix(10, 0)})
	_, _ = l.Append(LegacySquawk{From: Target{Kind: "actor", Ref: "s"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "beta", Project: "beta", At: time.Unix(20, 0)})
	if n := len(l.List(LegacyFilter{Project: "acme"})); n != 1 {
		t.Fatalf("project filter=%d want 1", n)
	}
	if n := len(l.List(LegacyFilter{Since: time.Unix(15, 0)})); n != 1 {
		t.Fatalf("since filter=%d want 1", n)
	}
	if n := len(l.List(LegacyFilter{})); n != 2 {
		t.Fatalf("empty filter=%d want 2", n)
	}
}
