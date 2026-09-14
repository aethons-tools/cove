package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
)

// fakeFeeder is a fake commentFeeder recording the `since` it was called
// with and returning a canned comment slice.
type fakeFeeder struct {
	cs    []linear.FeedComment
	since time.Time
}

func (f *fakeFeeder) CommentFeed(ctx context.Context, since time.Time, limit int) ([]linear.FeedComment, error) {
	f.since = since
	return f.cs, nil
}

func TestLinearSurfacePollMapsFeed(t *testing.T) {
	started := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	ff := &fakeFeeder{cs: []linear.FeedComment{
		{ID: "c1", Body: "hold", CreatedAt: started.Add(time.Hour), Author: "Brent", IssueIdentifier: "ACME-42", ParentID: ""},
		{ID: "c2", Body: "?", CreatedAt: started.Add(2 * time.Hour), Author: "Brent", IssueIdentifier: "ACME-42", ParentID: "c1"},
	}}
	s := &linearSurface{feed: ff, started: started}
	evs, next, err := s.Poll(context.Background(), "acme", "")
	if err != nil || len(evs) != 2 {
		t.Fatalf("poll: %v %+v", err, evs)
	}
	if evs[0].ForeignID != "c1" || evs[0].Surface != "ACME-42" || evs[0].Author != "Brent" || evs[1].ReplyToForeign != "c1" {
		t.Fatalf("event map: %+v", evs)
	}
	if ff.since != started { // empty cursor → started baseline
		t.Fatalf("empty cursor must poll from started, got %v", ff.since)
	}
	if next == "" {
		t.Fatal("next cursor must advance")
	}
	wantNext := started.Add(2 * time.Hour).Format(time.RFC3339Nano)
	if next != wantNext {
		t.Fatalf("next = %q, want %q", next, wantNext)
	}
	// Deliver must never succeed (egress off)
	if _, err := s.Deliver(context.Background(), msgport.Delivery{}, msglog.Message{}); err == nil {
		t.Fatal("Deliver must error (egress not enabled)")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.Service() != "linear" {
		t.Fatalf("Service() = %q", s.Service())
	}
}

func TestLinearSurfacePollSetSinceParses(t *testing.T) {
	started := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	since := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	ff := &fakeFeeder{}
	s := &linearSurface{feed: ff, started: started}
	if _, _, err := s.Poll(context.Background(), "acme", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !ff.since.Equal(since) {
		t.Fatalf("since = %v, want %v", ff.since, since)
	}
}

func newTestStore(t *testing.T) *harbor.FileStore {
	t.Helper()
	st, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

func TestDirectoryRoute(t *testing.T) {
	st := newTestStore(t)
	if err := st.PutInstance(harbor.Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}); err != nil {
		t.Fatalf("PutInstance: %v", err)
	}
	if err := st.AddChannel("acme", harbor.Channel{Name: "eng", Service: "linear", Ref: "ACME-9"}); err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	d := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}

	// self-post → dropped
	if _, _, _, ok := d.Route("linear", "acme", msgport.Event{Author: "harbor-bot", Surface: "ACME-42"}); ok {
		t.Fatal("self-authored comment must be dropped")
	}
	// human reply on a cove's ticket → actor
	from, to, _, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-42", ForeignID: "c1"})
	if !ok || from.Kind != "human" || from.Ref != "Brent" || len(to) != 1 || to[0].Kind != "actor" || to[0].Ref != "cove-1" {
		t.Fatalf("cove route: %v %+v %+v", ok, from, to)
	}
	// reply on a channel's thread → channel
	_, to, replyTo, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-9", ReplyToForeign: "p1"})
	if !ok || to[0].Kind != "channel" || to[0].Ref != "eng" || replyTo != "in:linear:p1" {
		t.Fatalf("channel route: %v %+v %q", ok, to, replyTo)
	}
	// unknown issue → unrouted
	if _, _, _, ok := d.Route("linear", "acme", msgport.Event{Author: "Brent", Surface: "ACME-999"}); ok {
		t.Fatal("unknown issue must be unrouted")
	}
	// Projects + Resolve stub
	if got := d.Projects("linear"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("Projects = %v", got)
	}
	if _, ok := d.Resolve("linear", "acme", msglog.Target{}, msglog.Target{}); ok {
		t.Fatal("Resolve must be a stub returning ok=false")
	}
}

func TestFileCursorsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cursors.json")
	c, err := newFileCursors(p)
	if err != nil {
		t.Fatalf("newFileCursors: %v", err)
	}
	if got := c.Ingress("linear", "acme"); got != "" {
		t.Fatalf("Ingress before set = %q, want empty", got)
	}
	if err := c.SetIngress("linear", "acme", "2026-09-14T10:00:00Z"); err != nil {
		t.Fatalf("SetIngress: %v", err)
	}
	c2, err := newFileCursors(p) // reload
	if err != nil {
		t.Fatalf("newFileCursors reload: %v", err)
	}
	if c2.Ingress("linear", "acme") != "2026-09-14T10:00:00Z" {
		t.Fatal("cursor must persist across reload")
	}
}

func TestFileCursorsToleratesMissingOrTornFile(t *testing.T) {
	dir := t.TempDir()
	// missing file
	p := filepath.Join(dir, "nope.json")
	c, err := newFileCursors(p)
	if err != nil {
		t.Fatalf("newFileCursors missing: %v", err)
	}
	if got := c.Ingress("linear", "acme"); got != "" {
		t.Fatalf("Ingress on missing file = %q", got)
	}
}

func TestNoopMarkers(t *testing.T) {
	var m noopMarkers
	if got := m.Egress("linear"); got.LastMsg != "" || got.Pending != nil {
		t.Fatalf("Egress not zero: %+v", got)
	}
	if err := m.SetEgress("linear", msgport.EgressMark{}); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}
}
