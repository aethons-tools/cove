package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/relay"
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

// GetProject returns the fake project record, if any was seeded. Tests that
// never populate projects get ok=false, which (for Resolve's purposes)
// behaves like a zero Project — ChatService=="" — preserving the Linear-only
// path.

// GetConnection treats a seeded Project.ChatService as a connection id whose
// kind is the id itself, so a fake project with ChatService "discord" is a
// discord chat service (the real store holds a con_ id there).

// ListProjects returns the seeded project names (Directory.Projects("discord")).

func (f *fakePoster) IssueByIdentifier(_ context.Context, identifier string) (string, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	id, ok := f.idByID[identifier]
	if !ok {
		return "", fmt.Errorf("no such identifier %q", identifier)
	}
	return id, nil
}

func (f *fakePoster) PostComment(_ context.Context, issueID, body string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, struct{ issueID, body string }{issueID, body})
	return nil
}

func TestDeliverPropagatesErrors(t *testing.T) {
	m := intercom.Squawk{Body: "x"}
	d := relay.Delivery{Service: "linear", Address: "ACME-7"}
	// resolve error
	s1 := &linearSurface{poster: &fakePoster{resolveErr: fmt.Errorf("boom")}}
	if _, err := s1.Deliver(context.Background(), d, m); err == nil {
		t.Fatal("expected resolve error")
	}
	// post error
	s2 := &linearSurface{poster: &fakePoster{idByID: map[string]string{"ACME-7": "iss_7"}, postErr: fmt.Errorf("nope")}}
	if _, err := s2.Deliver(context.Background(), d, m); err == nil {
		t.Fatal("expected post error")
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

func TestFileMarkersRoundTripAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "markers.json")
	m, err := newFileMarkers(p)
	if err != nil {
		t.Fatalf("newFileMarkers: %v", err)
	}
	if m.has("linear") {
		t.Fatal("fresh markers must not have linear")
	}
	want := relay.EgressMark{LastSeq: 9, Pending: map[string]map[string]bool{"id-10": {"human:a": true}}}
	if err := m.SetEgress("linear", want); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}
	if !m.has("linear") {
		t.Fatal("has(linear) false after SetEgress")
	}
	m2, err := newFileMarkers(p) // reload from disk
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := m2.Egress("linear")
	if got.LastSeq != 9 || !got.Pending["id-10"]["human:a"] {
		t.Fatalf("reloaded mark = %+v, want %+v", got, want)
	}
	if got := m2.Egress("discord"); got.LastSeq != 0 || got.Pending != nil {
		t.Fatalf("unset service must be zero EgressMark, got %+v", got)
	}
}

// TestFileMarkersNeedsSeedOnZeroLastSeq guards the COV-184 upgrade path: a
// pre-COV-184 marker persisted the low-water as LastMsg (a string id). That
// field no longer exists on EgressMark, so loading an old marker file leaves
// a present-but-zero LastSeq entry. needsSeed must treat that the same as
// "no marker at all" so the cmd seed step re-seeds to the current tail
// instead of resuming egress from Seq 0 (which would re-deliver the backlog).
func TestFileMarkersNeedsSeedOnZeroLastSeq(t *testing.T) {
	p := filepath.Join(t.TempDir(), "markers.json")
	m, err := newFileMarkers(p)
	if err != nil {
		t.Fatalf("newFileMarkers: %v", err)
	}
	if !m.needsSeed("linear") {
		t.Fatal("fresh markers: needsSeed(linear) must be true")
	}

	// Simulate an old-format marker file: {"linear": {}} — present key, no
	// LastSeq (as if LastMsg had been dropped by the field rename).
	if err := os.WriteFile(p, []byte(`{"linear": {}}`), 0o600); err != nil {
		t.Fatalf("write old-format marker: %v", err)
	}
	m2, err := newFileMarkers(p)
	if err != nil {
		t.Fatalf("newFileMarkers reload: %v", err)
	}
	if !m2.has("linear") {
		t.Fatal("old-format marker: has(linear) must be true (key present)")
	}
	if !m2.needsSeed("linear") {
		t.Fatal("old-format marker (LastSeq==0): needsSeed(linear) must be true")
	}

	// A properly-seeded marker (nonzero LastSeq) must NOT re-seed.
	if err := m2.SetEgress("linear", relay.EgressMark{LastSeq: 5}); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}
	if m2.needsSeed("linear") {
		t.Fatal("nonzero LastSeq: needsSeed(linear) must be false")
	}
}

func TestFileMarkersMissingFileStartsEmpty(t *testing.T) {
	m, err := newFileMarkers(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("newFileMarkers missing: %v", err)
	}
	if m.has("linear") {
		t.Fatal("missing file must start empty")
	}
}

// TestFileMarkersEgressIsDeepCopied guards against the COV-182 data race:
// Jam runs two relay engines (linear + discord) sharing one
// *fileMarkers. If Egress/SetEgress ever hand out or store an EgressMark
// whose Pending map aliases fileMarkers' stored map, one engine's goroutine
// can mutate that map without fm.mu held while the other engine's SetEgress
// concurrently iterates it in json.MarshalIndent — a fatal concurrent map
// iteration/write. Both directions (read-then-mutate, and mutate-after-set)
// must be insulated by a deep copy.
func TestFileMarkersEgressIsDeepCopied(t *testing.T) {
	p := filepath.Join(t.TempDir(), "markers.json")
	m, err := newFileMarkers(p)
	if err != nil {
		t.Fatalf("newFileMarkers: %v", err)
	}

	if err := m.SetEgress("linear", relay.EgressMark{
		LastSeq: 1,
		Pending: map[string]map[string]bool{"m1": {"t1": true}},
	}); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}

	// Read path: mutating the returned mark must not reach the store.
	got := m.Egress("linear")
	got.Pending["m1"]["t1"] = false
	got.Pending["m2"] = map[string]bool{"t2": true}

	got2 := m.Egress("linear")
	if !got2.Pending["m1"]["t1"] {
		t.Fatalf("store mutated via Egress-returned map: got2 = %+v", got2)
	}
	if _, ok := got2.Pending["m2"]; ok {
		t.Fatalf("store gained key added via Egress-returned map: got2 = %+v", got2)
	}

	// Write path: mutating the mark AFTER SetEgress must not reach the store.
	orig := relay.EgressMark{
		LastSeq: 2,
		Pending: map[string]map[string]bool{"m1": {"t1": true}},
	}
	if err := m.SetEgress("x", orig); err != nil {
		t.Fatalf("SetEgress: %v", err)
	}
	orig.Pending["m1"]["t1"] = false

	got3 := m.Egress("x")
	if !got3.Pending["m1"]["t1"] {
		t.Fatalf("store mutated via caller's map after SetEgress: got3 = %+v", got3)
	}
}

// Discord ingress covers every project whose chat service is discord (not just
// a Requisitioner's project), plus the Requisitioner's project for back-compat;
// Linear keeps its single Requisitioner project.
func TestDirectoryProjectsDiscordListsAllDiscordProjects(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme", "beta", "gamma", "delta")
	for p, svc := range map[string]string{"acme": "discord", "beta": "discord", "gamma": ""} {
		if err := st.SetChatService(p, svc); err != nil {
			t.Fatal(err)
		}
	}
	sorted := func(ss []string) []string { out := append([]string(nil), ss...); sort.Strings(out); return out }

	noRequisitioner := &directory{store: st}
	if got := sorted(noRequisitioner.Projects("discord")); !reflect.DeepEqual(got, []string{"acme", "beta"}) {
		t.Fatalf("discord projects (no Requisitioner) = %v, want [acme beta]", got)
	}
	withRequisitioner := &directory{store: st, project: "gamma"}
	if got := sorted(withRequisitioner.Projects("discord")); !reflect.DeepEqual(got, []string{"acme", "beta", "gamma"}) {
		t.Fatalf("discord projects (Requisitioner on gamma) = %v, want [acme beta gamma]", got)
	}
	if got := (&directory{store: st, project: "acme"}).Projects("discord"); len(got) != 2 {
		t.Fatalf("Requisitioner project already discord must not repeat: %v", got)
	}
	if got := withRequisitioner.Projects("linear"); !reflect.DeepEqual(got, []string{"gamma"}) {
		t.Fatalf("linear projects = %v, want [gamma]", got)
	}
}

// TestLinearDeliverEscapesPlainText: a text/plain squawk is escaped so Linear
// shows it literally; markdown (the default) is posted byte-for-byte. The
// prefix (@handle) is ours and never escaped.
func TestLinearDeliverEscapesPlainText(t *testing.T) {
	fp := &fakePoster{idByID: map[string]string{"ACME-1": "issue-1"}}
	s := &linearSurface{poster: fp}
	d := relay.Delivery{Address: "ACME-1", BodyPrefix: "@alice "}
	from := ident.ID("cove-1")
	if _, err := s.Deliver(context.Background(), d, intercom.Squawk{From: from, Body: "**bold**", ContentType: intercom.ContentMarkdown}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deliver(context.Background(), d, intercom.Squawk{From: from, Body: "2 * 3\n- x", ContentType: intercom.ContentPlain}); err != nil {
		t.Fatal(err)
	}
	if len(fp.posts) != 2 || fp.posts[0].body != "@alice **bold**" || fp.posts[1].body != "@alice 2 \\* 3  \n\\- x" {
		t.Fatalf("posts = %+v", fp.posts)
	}
}

// fakePoster is a fake commentPoster: canned identifier→id resolution and
// recorded posts, so Deliver is testable without a live Linear client.
type fakePoster struct {
	posts               []struct{ issueID, body string }
	idByID              map[string]string // identifier -> internal id
	postErr, resolveErr error
}
