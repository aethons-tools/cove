// Package msglogtest is a backend-agnostic conformance suite for msglog.Store,
// run against both the file Log (hermetic) and the Postgres backend (integration).
package msglogtest

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func RunConformance(t *testing.T, newStore func(t *testing.T) msglog.Store) {
	actor := func(r string) msglog.Target { return msglog.Target{Kind: "actor", Ref: r} }
	human := func(r string) msglog.Target { return msglog.Target{Kind: "human", Ref: r} }

	t.Run("append_assigns_id_and_at", func(t *testing.T) {
		s := newStore(t)
		got, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "hi"})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.ID == "" || got.At.IsZero() {
			t.Fatalf("Append must assign ID and At: %+v", got)
		}
	})

	t.Run("append_validates", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}}); err == nil {
			t.Fatal("empty body must error")
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), Body: "x"}); err == nil {
			t.Fatal("empty To must error")
		}
	})

	t.Run("read_inbox_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("a"), human("b")}, Body: "m1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("c")}, Body: "m2"}); err != nil {
			t.Fatal(err)
		}
		if got := s.ReadInbox(actor("a")); len(got) != 1 || got[0].Body != "m1" {
			t.Fatalf("ReadInbox(actor:a) = %+v, want [m1]", got)
		}
		if got := s.ReadInbox(human("b")); len(got) != 1 || got[0].Body != "m1" {
			t.Fatalf("ReadInbox(human:b) = %+v, want [m1]", got)
		}
		if got := s.ReadInbox(actor("z")); len(got) != 0 {
			t.Fatalf("ReadInbox(actor:z) = %+v, want none", got)
		}
		// The returned message reconstructs its full To set.
		if got := s.ReadInbox(actor("a")); len(got[0].To) != 2 {
			t.Fatalf("reconstructed To = %+v, want 2 targets", got[0].To)
		}
	})

	t.Run("read_thread_root_and_direct_replies", func(t *testing.T) {
		s := newStore(t)
		root, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "root"})
		if _, err := s.Append(msglog.Message{From: human("a"), To: []msglog.Target{actor("c1")}, Body: "reply", ReplyTo: root.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "unrelated"}); err != nil {
			t.Fatal(err)
		}
		got := s.ReadThread(root.ID)
		if len(got) != 2 || got[0].Body != "root" || got[1].Body != "reply" {
			t.Fatalf("ReadThread = %+v, want [root, reply] in order", got)
		}
	})

	t.Run("list_filter_project_and_time", func(t *testing.T) {
		s := newStore(t)
		t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "acme1", Project: "acme", At: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "beta1", Project: "beta", At: t0.Add(48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got := s.List(msglog.Filter{Project: "acme"}); len(got) != 1 || got[0].Body != "acme1" {
			t.Fatalf("List(project=acme) = %+v", got)
		}
		if got := s.List(msglog.Filter{}); len(got) != 2 {
			t.Fatalf("List(all) = %d, want 2", len(got))
		}
		win := s.List(msglog.Filter{Since: t0.Add(24 * time.Hour), Until: t0.Add(72 * time.Hour)})
		if len(win) != 1 || win[0].Body != "beta1" {
			t.Fatalf("List(time window) = %+v, want [beta1]", win)
		}
	})

	t.Run("list_and_reads_are_append_order", func(t *testing.T) {
		s := newStore(t)
		for _, b := range []string{"a", "b", "c"} {
			if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("h")}, Body: b}); err != nil {
				t.Fatal(err)
			}
		}
		got := s.List(msglog.Filter{})
		if len(got) != 3 || got[0].Body != "a" || got[2].Body != "c" {
			t.Fatalf("List order = %+v, want a,b,c", got)
		}
	})

	t.Run("duplicate_to_target", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(msglog.Message{
			From: actor("c1"),
			To:   []msglog.Target{actor("a"), actor("a")},
			Body: "dup",
		}); err != nil {
			t.Fatalf("Append with duplicate To target: %v", err)
		}
		got := s.ReadInbox(actor("a"))
		if len(got) != 1 || got[0].Body != "dup" {
			t.Fatalf("ReadInbox(actor:a) = %+v, want exactly one message [dup]", got)
		}
	})

	t.Run("seen_ids_by_prefix", func(t *testing.T) {
		s := newStore(t)
		for _, id := range []string{"in:linear:c1", "in:linear:c2", "in:discord:c3"} {
			if _, err := s.Append(msglog.Message{ID: id, From: human("a"), To: []msglog.Target{actor("x")}, Body: "b"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(msglog.Message{From: actor("x"), To: []msglog.Target{human("a")}, Body: "egress"}); err != nil {
			t.Fatal(err)
		}
		got := s.SeenIDs("in:linear:")
		want := []string{"in:linear:c1", "in:linear:c2"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("SeenIDs(in:linear:) = %v, want %v (prefix matches in append order)", got, want)
		}
	})

	t.Run("list_since_and_tail", func(t *testing.T) {
		s := newStore(t)
		var ids []string
		for _, b := range []string{"m1", "m2", "m3"} {
			got, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("h")}, Body: b})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, got.ID)
		}
		if tail, ok := s.TailID(); !ok || tail != ids[2] {
			t.Fatalf("TailID = %q,%v, want %q,true", tail, ok, ids[2])
		}
		after := s.ListSince(ids[0], 0) // everything after m1
		if len(after) != 2 || after[0].Body != "m2" || after[1].Body != "m3" {
			t.Fatalf("ListSince(m1,0) = %+v, want [m2,m3]", after)
		}
		if lim := s.ListSince("", 2); len(lim) != 2 || lim[0].Body != "m1" {
			t.Fatalf("ListSince(\"\",2) = %+v, want [m1,m2]", lim)
		}
		if none := s.ListSince(ids[2], 0); len(none) != 0 {
			t.Fatalf("ListSince(tail,0) = %+v, want empty", none)
		}
	})

	t.Run("tail_empty_log", func(t *testing.T) {
		if id, ok := newStore(t).TailID(); ok || id != "" {
			t.Fatalf("TailID(empty) = %q,%v, want \"\",false", id, ok)
		}
	})

	t.Run("read_inbox_since", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "before"})
		m2, _ := s.Append(msglog.Message{From: human("a"), To: []msglog.Target{actor("x")}, Body: "after1"})
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("y")}, Body: "other"})
		got := s.ReadInboxSince(actor("x"), m1.ID, 0)
		if len(got) != 1 || got[0].Body != "after1" || got[0].ID != m2.ID {
			t.Fatalf("ReadInboxSince(x, m1) = %+v, want [after1]", got)
		}
		if all := s.ReadInboxSince(actor("x"), "", 0); len(all) != 2 {
			t.Fatalf("ReadInboxSince(x, \"\") = %d, want 2", len(all))
		}
		if none := s.ReadInboxSince(actor("z"), "", 0); len(none) != 0 {
			t.Fatalf("ReadInboxSince(z) = %+v, want empty", none)
		}

		// A multi-recipient message appended after the cursor must be visible via
		// ReadInboxSince to EVERY one of its recipients, with its full To
		// reconstructed — exercising the message_recipients join fan-out through
		// the cursor path.
		m3, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x"), actor("y")}, Body: "multi"})
		if err != nil {
			t.Fatal(err)
		}
		wantTo := []msglog.Target{actor("x"), actor("y")}
		gotX := s.ReadInboxSince(actor("x"), m1.ID, 0)
		if len(gotX) != 2 || gotX[1].Body != "multi" || gotX[1].ID != m3.ID {
			t.Fatalf("ReadInboxSince(x, m1) = %+v, want [after1, multi]", gotX)
		}
		if !reflect.DeepEqual(gotX[1].To, wantTo) {
			t.Fatalf("ReadInboxSince(x) multi To = %+v, want %+v", gotX[1].To, wantTo)
		}
		// actor:y also received "other" (appended after m1, before "multi").
		gotY := s.ReadInboxSince(actor("y"), m1.ID, 0)
		if len(gotY) != 2 || gotY[1].Body != "multi" || gotY[1].ID != m3.ID {
			t.Fatalf("ReadInboxSince(y, m1) = %+v, want [other, multi]", gotY)
		}
		if !reflect.DeepEqual(gotY[1].To, wantTo) {
			t.Fatalf("ReadInboxSince(y) multi To = %+v, want %+v", gotY[1].To, wantTo)
		}
	})

	t.Run("read_inbox_before", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b1"})
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b2"})
		m3, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b3"})
		// from the end: last 2, ascending.
		end := s.ReadInboxBefore(actor("x"), "", 2)
		if len(end) != 2 || end[0].Body != "b2" || end[1].Body != "b3" {
			t.Fatalf("ReadInboxBefore(x,\"\",2) = %+v, want [b2,b3]", end)
		}
		// before m3 (exclusive): the nearest-below, ascending.
		before := s.ReadInboxBefore(actor("x"), m3.ID, 10)
		if len(before) != 2 || before[0].Body != "b1" || before[1].Body != "b2" {
			t.Fatalf("ReadInboxBefore(x,m3,10) = %+v, want [b1,b2]", before)
		}
		// before m1: nothing (exclusive anchor).
		if none := s.ReadInboxBefore(actor("x"), m1.ID, 10); len(none) != 0 {
			t.Fatalf("ReadInboxBefore(x,m1) = %+v, want empty", none)
		}
	})

	t.Run("read_inbox_before_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x"), actor("y")}, Body: "shared"})
		if got := s.ReadInboxBefore(actor("x"), "", 10); len(got) != 1 || len(got[0].To) != 2 {
			t.Fatalf("multi-recipient before(x) = %+v", got)
		}
		if got := s.ReadInboxBefore(actor("y"), "", 10); len(got) != 1 {
			t.Fatalf("multi-recipient before(y) = %+v", got)
		}
	})

	t.Run("seq_and_resolution", func(t *testing.T) { testSeqAndResolution(t, newStore) })
}

// testSeqAndResolution exercises Message.Seq assignment, SeqOf resolution, and
// TailSeq — shared by both backends via RunConformance.
func testSeqAndResolution(t *testing.T, newStore func(t *testing.T) msglog.Store) {
	actor := func(r string) msglog.Target { return msglog.Target{Kind: "actor", Ref: r} }
	s := newStore(t)
	a, _ := s.Append(msglog.Message{From: actor("c"), To: []msglog.Target{actor("x")}, Body: "a"})
	b, _ := s.Append(msglog.Message{From: actor("c"), To: []msglog.Target{actor("x")}, Body: "b"})
	if a.Seq <= 0 || b.Seq <= a.Seq {
		t.Fatalf("Seq not monotonic: a=%d b=%d", a.Seq, b.Seq)
	}
	if seq, ok := s.SeqOf(a.ID); !ok || seq != a.Seq {
		t.Fatalf("SeqOf(a) = %d,%v want %d", seq, ok, a.Seq)
	}
	if _, ok := s.SeqOf("nope"); ok {
		t.Fatal("SeqOf(unknown) should miss")
	}
	if tail, ok := s.TailSeq(); !ok || tail != b.Seq {
		t.Fatalf("TailSeq = %d,%v want %d", tail, ok, b.Seq)
	}
}
