package intercomtest

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Fixture is a fresh channel log for one case: Store is the log under test;
// Legacy is the legacy log it continues — Store was opened over it after
// LegacyCount squawks were appended there (the cutover).
type Fixture struct {
	Store       intercom.Store
	Legacy      intercom.LegacyStore
	LegacyCount int
}

var (
	chA   = ident.ID("chn_01j9q3aaaaaaaaaaaaaaaaaaaa")
	chB   = ident.ID("chn_01j9q3bbbbbbbbbbbbbbbbbbbb")
	alice = ident.ID("usr_01j9q3aaaaaaaaaaaaaaaaaaaa")
	bob   = ident.ID("usr_01j9q3bbbbbbbbbbbbbbbbbbbb")
	ses   = ident.ID("ses_01j9q3aaaaaaaaaaaaaaaaaaaa")
)

func squawkBodies(ms []intercom.Squawk) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Body
	}
	return out
}

// RunConformance exercises intercom.Store. newLog opens a log whose legacy
// log holds legacy squawks (none when 0) before the cutover.
func RunConformance(t *testing.T, newLog func(t *testing.T, legacy int) Fixture) {
	post := func(t *testing.T, s intercom.Store, ch, from ident.ID, body string, audience ...ident.ID) intercom.Squawk {
		t.Helper()
		m, err := s.Append(intercom.Squawk{Channel: ch, From: from, Body: body}, audience)
		if err != nil {
			t.Fatalf("Append %q: %v", body, err)
		}
		return m
	}

	t.Run("append_assigns_and_validates", func(t *testing.T) {
		s := newLog(t, 0).Store
		m := post(t, s, chA, ses, "hi", alice)
		if m.ID == "" || m.At.IsZero() || m.Seq == 0 || m.ContentType != intercom.ContentMarkdown {
			t.Fatalf("appended = %+v", m)
		}
		for _, bad := range []intercom.Squawk{
			{From: ses, Body: "no channel"},
			{Channel: chA, Body: "no author"},
			{Channel: chA, From: ses},
			{Channel: chA, From: ses, Body: "x", ContentType: "text/html"},
		} {
			if _, err := s.Append(bad, nil); err == nil {
				t.Errorf("Append(%+v) must fail", bad)
			}
		}
		if tail, ok := s.TailSeq(); !ok || tail != m.Seq {
			t.Fatalf("TailSeq = %d, %v", tail, ok)
		}
	})

	t.Run("round_trips_every_field", func(t *testing.T) {
		s := newLog(t, 0).Store
		in := intercom.Squawk{Channel: chA, From: alice, Body: "b", ReplyTo: "r1", ContentType: intercom.ContentPlain,
			Origin: "con_01j9q3aaaaaaaaaaaaaaaaaaaa", OriginRef: "ACME-1"}
		m, err := s.Append(in, []ident.ID{ses})
		if err != nil {
			t.Fatal(err)
		}
		got := s.ChannelSince(chA, 0, 0)
		if len(got) != 1 || got[0].At.Sub(m.At).Abs() > time.Millisecond { // a store may keep microseconds
			t.Fatalf("read back %+v", got)
		}
		got[0].At = m.At
		if got[0] != m {
			t.Fatalf("read back %#v, appended %#v", got[0], m)
		}
	})

	t.Run("inbox_is_deliveries", func(t *testing.T) {
		s := newLog(t, 0).Store
		post(t, s, chA, ses, "to alice", alice)
		post(t, s, chA, alice, "to ses", ses)
		post(t, s, chB, ses, "to both", alice, bob)
		post(t, s, chB, bob, "to no one")
		if got := squawkBodies(s.InboxSince(alice, 0, 0)); !slices.Equal(got, []string{"to alice", "to both"}) {
			t.Fatalf("alice's inbox = %q", got)
		}
		if got := squawkBodies(s.InboxSince(ses, 0, 0)); !slices.Equal(got, []string{"to ses"}) {
			t.Fatalf("ses's inbox = %q", got)
		}
		stats := s.InboxChannels(alice)
		if len(stats) != 2 || stats[0].Channel != chA || stats[1].Channel != chB || stats[1].LastSeq <= stats[0].LastSeq {
			t.Fatalf("InboxChannels = %+v", stats)
		}
	})

	t.Run("paging_both_ways", func(t *testing.T) {
		s := newLog(t, 0).Store
		var seqs []int64
		for _, b := range []string{"1", "2", "3", "4", "5"} {
			seqs = append(seqs, post(t, s, chA, ses, b, alice).Seq)
		}
		post(t, s, chB, ses, "elsewhere", alice)
		cases := []struct {
			name string
			got  []intercom.Squawk
			want []string
		}{
			{"inbox since", s.InboxSince(alice, seqs[1], 2), []string{"3", "4"}},
			{"inbox before", s.InboxBefore(alice, seqs[3], 2), []string{"2", "3"}},
			{"inbox from the end", s.InboxBefore(alice, 0, 2), []string{"5", "elsewhere"}},
			{"channel since", s.ChannelSince(chA, seqs[2], 0), []string{"4", "5"}},
			{"channel before", s.ChannelBefore(chA, seqs[2], 0), []string{"1", "2"}},
			{"channel from the end", s.ChannelBefore(chA, 0, 1), []string{"5"}},
			{"list since", s.ListSince(seqs[3], 0), []string{"5", "elsewhere"}},
		}
		for _, c := range cases {
			if got := squawkBodies(c.got); !slices.Equal(got, c.want) {
				t.Errorf("%s = %q, want %q", c.name, got, c.want)
			}
		}
	})

	t.Run("threads", func(t *testing.T) {
		s := newLog(t, 0).Store
		root := post(t, s, chA, ses, "root", alice)
		if _, err := s.Append(intercom.Squawk{Channel: chA, From: alice, Body: "reply", ReplyTo: root.ID}, []ident.ID{ses}); err != nil {
			t.Fatal(err)
		}
		post(t, s, chA, ses, "other", alice)
		if got := squawkBodies(s.ReadThread(root.ID)); !slices.Equal(got, []string{"root", "reply"}) {
			t.Fatalf("thread = %q", got)
		}
	})

	t.Run("ingress_ids_dedupe", func(t *testing.T) {
		s := newLog(t, 0).Store
		in := intercom.Squawk{ID: "in:linear:abc", Channel: chA, From: alice, Body: "from linear"}
		if _, err := s.Append(in, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(in, nil); !errors.Is(err, intercom.ErrDuplicateID) {
			t.Fatalf("an id is appended once: %v, want ErrDuplicateID", err)
		}
		if _, err := s.Append(intercom.Squawk{Channel: chA, From: alice}, nil); !errors.Is(err, intercom.ErrInvalid) {
			t.Fatalf("an empty body: %v, want ErrInvalid", err)
		}
		if got := s.SeenIDs("in:linear:"); !slices.Equal(got, []string{"in:linear:abc"}) {
			t.Fatalf("SeenIDs = %q", got)
		}
		if seq, ok := s.SeqOf("in:linear:abc"); !ok || seq == 0 {
			t.Fatalf("SeqOf = %d, %v", seq, ok)
		}
	})

	t.Run("continues_the_legacy_log", func(t *testing.T) {
		f := newLog(t, 3)
		legacy := f.Legacy.ListSince(0, 0)
		if len(legacy) != 3 {
			t.Fatalf("legacy log = %d squawks", len(legacy))
		}
		last := legacy[2]
		if cut := f.Store.CutoverSeq(); cut <= last.Seq {
			t.Fatalf("CutoverSeq = %d, want above the legacy tail %d", cut, last.Seq)
		}
		if tail, ok := f.Store.TailSeq(); !ok || tail != last.Seq {
			t.Fatalf("TailSeq over an empty new log = %d, %v; want the legacy tail %d", tail, ok, last.Seq)
		}
		m := post(t, f.Store, chA, ses, "first", alice)
		if m.Seq < f.Store.CutoverSeq() {
			t.Fatalf("first seq %d, cutover %d", m.Seq, f.Store.CutoverSeq())
		}
		if _, err := f.Store.Append(intercom.Squawk{ID: last.ID, Channel: chA, From: alice, Body: "x"}, nil); !errors.Is(err, intercom.ErrDuplicateID) {
			t.Fatalf("a legacy id appended again: %v, want ErrDuplicateID", err)
		}
		if seq, ok := f.Store.SeqOf(last.ID); !ok || seq != last.Seq {
			t.Fatalf("SeqOf(legacy id) = %d, %v", seq, ok)
		}
		if got := f.Store.SeenIDs(""); len(got) != 4 {
			t.Fatalf("SeenIDs spans both logs: %q", got)
		}
		if got := f.Store.ListSince(0, 0); len(got) != 1 {
			t.Fatalf("ListSince reads the new log only: %+v", got)
		}
	})
}
