// Package intercomtest is a backend-agnostic conformance suite for intercom.Store,
// run against both the file Log (hermetic) and the Postgres backend (integration).
package intercomtest

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
)

// actor and human build Targets for the conformance cases below.
func actor(r string) intercom.Target { return intercom.Target{Kind: "actor", Ref: r} }
func human(r string) intercom.Target { return intercom.Target{Kind: "human", Ref: r} }

// bodies extracts the Body of each message, for compact test failure output.
func bodies(ms []intercom.Squawk) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Body
	}
	return out
}

func RunConformance(t *testing.T, newStore func(t *testing.T) intercom.Store) {
	t.Run("append_assigns_id_and_at", func(t *testing.T) {
		s := newStore(t)
		got, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}, Body: "hi"})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.ID == "" || got.At.IsZero() {
			t.Fatalf("Append must assign ID and At: %+v", got)
		}
	})

	t.Run("append_validates", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}}); err == nil {
			t.Fatal("empty body must error")
		}
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), Body: "x"}); err == nil {
			t.Fatal("empty To must error")
		}
	})

	t.Run("read_inbox_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("a"), human("b")}, Body: "m1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("c")}, Body: "m2"}); err != nil {
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
		root, _ := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}, Body: "root"})
		if _, err := s.Append(intercom.Squawk{From: human("a"), To: []intercom.Target{actor("c1")}, Body: "reply", ReplyTo: root.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}, Body: "unrelated"}); err != nil {
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
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}, Body: "acme1", Project: "acme", At: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("a")}, Body: "beta1", Project: "beta", At: t0.Add(48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got := s.List(intercom.Filter{Project: "acme"}); len(got) != 1 || got[0].Body != "acme1" {
			t.Fatalf("List(project=acme) = %+v", got)
		}
		if got := s.List(intercom.Filter{}); len(got) != 2 {
			t.Fatalf("List(all) = %d, want 2", len(got))
		}
		win := s.List(intercom.Filter{Since: t0.Add(24 * time.Hour), Until: t0.Add(72 * time.Hour)})
		if len(win) != 1 || win[0].Body != "beta1" {
			t.Fatalf("List(time window) = %+v, want [beta1]", win)
		}
	})

	t.Run("list_and_reads_are_append_order", func(t *testing.T) {
		s := newStore(t)
		for _, b := range []string{"a", "b", "c"} {
			if _, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("h")}, Body: b}); err != nil {
				t.Fatal(err)
			}
		}
		got := s.List(intercom.Filter{})
		if len(got) != 3 || got[0].Body != "a" || got[2].Body != "c" {
			t.Fatalf("List order = %+v, want a,b,c", got)
		}
	})

	t.Run("duplicate_to_target", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(intercom.Squawk{
			From: actor("c1"),
			To:   []intercom.Target{actor("a"), actor("a")},
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
			if _, err := s.Append(intercom.Squawk{ID: id, From: human("a"), To: []intercom.Target{actor("x")}, Body: "b"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(intercom.Squawk{From: actor("x"), To: []intercom.Target{human("a")}, Body: "egress"}); err != nil {
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
		var seqs []int64
		for _, b := range []string{"m1", "m2", "m3"} {
			got, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{human("h")}, Body: b})
			if err != nil {
				t.Fatal(err)
			}
			seqs = append(seqs, got.Seq)
		}
		if tail, ok := s.TailSeq(); !ok || tail != seqs[2] {
			t.Fatalf("TailSeq = %d,%v, want %d,true", tail, ok, seqs[2])
		}
		after := s.ListSince(seqs[0], 0) // everything after m1
		if len(after) != 2 || after[0].Body != "m2" || after[1].Body != "m3" {
			t.Fatalf("ListSince(m1,0) = %+v, want [m2,m3]", after)
		}
		if lim := s.ListSince(0, 2); len(lim) != 2 || lim[0].Body != "m1" {
			t.Fatalf("ListSince(0,2) = %+v, want [m1,m2]", lim)
		}
		if none := s.ListSince(seqs[2], 0); len(none) != 0 {
			t.Fatalf("ListSince(tail,0) = %+v, want empty", none)
		}
	})

	t.Run("tail_empty_log", func(t *testing.T) {
		if seq, ok := newStore(t).TailSeq(); ok || seq != 0 {
			t.Fatalf("TailSeq(empty) = %d,%v, want 0,false", seq, ok)
		}
	})

	t.Run("read_inbox_since", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x")}, Body: "before"})
		m2, _ := s.Append(intercom.Squawk{From: human("a"), To: []intercom.Target{actor("x")}, Body: "after1"})
		_, _ = s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("y")}, Body: "other"})
		got := s.ReadInboxSince(actor("x"), m1.Seq, 0)
		if len(got) != 1 || got[0].Body != "after1" || got[0].ID != m2.ID {
			t.Fatalf("ReadInboxSince(x, m1) = %+v, want [after1]", got)
		}
		if all := s.ReadInboxSince(actor("x"), 0, 0); len(all) != 2 {
			t.Fatalf("ReadInboxSince(x, 0) = %d, want 2", len(all))
		}
		if none := s.ReadInboxSince(actor("z"), 0, 0); len(none) != 0 {
			t.Fatalf("ReadInboxSince(z) = %+v, want empty", none)
		}

		// A multi-recipient message appended after the cursor must be visible via
		// ReadInboxSince to EVERY one of its recipients, with its full To
		// reconstructed — exercising the message_recipients join fan-out through
		// the cursor path.
		m3, err := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x"), actor("y")}, Body: "multi"})
		if err != nil {
			t.Fatal(err)
		}
		wantTo := []intercom.Target{actor("x"), actor("y")}
		gotX := s.ReadInboxSince(actor("x"), m1.Seq, 0)
		if len(gotX) != 2 || gotX[1].Body != "multi" || gotX[1].ID != m3.ID {
			t.Fatalf("ReadInboxSince(x, m1) = %+v, want [after1, multi]", gotX)
		}
		if !reflect.DeepEqual(gotX[1].To, wantTo) {
			t.Fatalf("ReadInboxSince(x) multi To = %+v, want %+v", gotX[1].To, wantTo)
		}
		// actor:y also received "other" (appended after m1, before "multi").
		gotY := s.ReadInboxSince(actor("y"), m1.Seq, 0)
		if len(gotY) != 2 || gotY[1].Body != "multi" || gotY[1].ID != m3.ID {
			t.Fatalf("ReadInboxSince(y, m1) = %+v, want [other, multi]", gotY)
		}
		if !reflect.DeepEqual(gotY[1].To, wantTo) {
			t.Fatalf("ReadInboxSince(y) multi To = %+v, want %+v", gotY[1].To, wantTo)
		}
	})

	t.Run("read_inbox_before", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x")}, Body: "b1"})
		_, _ = s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x")}, Body: "b2"})
		m3, _ := s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x")}, Body: "b3"})
		// from the end: last 2, ascending.
		end := s.ReadInboxBefore(actor("x"), 0, 2)
		if len(end) != 2 || end[0].Body != "b2" || end[1].Body != "b3" {
			t.Fatalf("ReadInboxBefore(x,0,2) = %+v, want [b2,b3]", end)
		}
		// before m3 (exclusive): the nearest-below, ascending.
		before := s.ReadInboxBefore(actor("x"), m3.Seq, 10)
		if len(before) != 2 || before[0].Body != "b1" || before[1].Body != "b2" {
			t.Fatalf("ReadInboxBefore(x,m3,10) = %+v, want [b1,b2]", before)
		}
		// before m1: nothing (exclusive anchor).
		if none := s.ReadInboxBefore(actor("x"), m1.Seq, 10); len(none) != 0 {
			t.Fatalf("ReadInboxBefore(x,m1) = %+v, want empty", none)
		}
	})

	t.Run("read_inbox_before_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Append(intercom.Squawk{From: actor("c1"), To: []intercom.Target{actor("x"), actor("y")}, Body: "shared"})
		if got := s.ReadInboxBefore(actor("x"), 0, 10); len(got) != 1 || len(got[0].To) != 2 {
			t.Fatalf("multi-recipient before(x) = %+v", got)
		}
		if got := s.ReadInboxBefore(actor("y"), 0, 10); len(got) != 1 {
			t.Fatalf("multi-recipient before(y) = %+v", got)
		}
	})

	t.Run("seq_and_resolution", func(t *testing.T) { testSeqAndResolution(t, newStore) })

	t.Run("mixed_namespace_ordering", func(t *testing.T) { testMixedNamespaceOrdering(t, newStore) })
}

// testMixedNamespaceOrdering is the regression guard for this whole bug class:
// message ids are opaque identifiers, not an ordering key, and ids minted by
// different sources (an internal id, an "in:linear:<uuid>" ingress id, an
// "in:discord:<snowflake>" ingress id) have no consistent lexical relationship
// to append order. Ordering and cursors must use Seq, never lexical id
// comparison, or a message can be silently skipped or misordered.
func testMixedNamespaceOrdering(t *testing.T, newStore func(t *testing.T) intercom.Store) {
	s := newStore(t)
	x := actor("x")
	// append in a deliberate order whose lexical id order differs from append order.
	m1, err := s.Append(intercom.Squawk{ID: "in:linear:zzz", From: human("h"), To: []intercom.Target{x}, Body: "1"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.Append(intercom.Squawk{ID: "in:discord:aaa", From: human("h"), To: []intercom.Target{x}, Body: "2"}) // lexically < m1
	if err != nil {
		t.Fatal(err)
	}
	m3, err := s.Append(intercom.Squawk{From: human("h"), To: []intercom.Target{x}, Body: "3"}) // digit-prefixed internal id
	if err != nil {
		t.Fatal(err)
	}
	got := s.ReadInbox(x)
	if len(got) != 3 || got[0].Body != "1" || got[1].Body != "2" || got[2].Body != "3" {
		t.Fatalf("ReadInbox not in APPEND order: %+v", bodies(got))
	}
	// ReadInboxSince(after m1.Seq) must include m2 even though m2.ID < m1.ID lexically.
	since := s.ReadInboxSince(x, m1.Seq, 0)
	if len(since) != 2 || since[0].Body != "2" || since[1].Body != "3" {
		t.Fatalf("ReadInboxSince(m1.Seq) = %+v, want [2,3] (lexical-id would wrongly drop 2)", bodies(since))
	}
	// ListSince must show the same append-order behavior across the whole log.
	all := s.ListSince(0, 0)
	if len(bodies(all)) < 3 {
		t.Fatalf("ListSince(0,0) = %+v, want at least [1,2,3] in append order", bodies(all))
	}
	_ = m2
	_ = m3
}

// testSeqAndResolution exercises Message.Seq assignment, SeqOf resolution, and
// TailSeq — shared by both backends via RunConformance.
func testSeqAndResolution(t *testing.T, newStore func(t *testing.T) intercom.Store) {
	s := newStore(t)
	a, _ := s.Append(intercom.Squawk{From: actor("c"), To: []intercom.Target{actor("x")}, Body: "a"})
	b, _ := s.Append(intercom.Squawk{From: actor("c"), To: []intercom.Target{actor("x")}, Body: "b"})
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
