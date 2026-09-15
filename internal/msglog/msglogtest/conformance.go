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
}
