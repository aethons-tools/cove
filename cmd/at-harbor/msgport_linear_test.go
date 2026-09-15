package main

import (
	"context"
	"fmt"
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
	// Projects + Resolve on an empty/invalid target
	if got := d.Projects("linear"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("Projects = %v", got)
	}
	if _, ok := d.Resolve("linear", "acme", msglog.Target{}, msglog.Target{}); ok {
		t.Fatal("Resolve of an empty target must be unresolved")
	}
}

// fakeStore is a minimal instanceRoster: canned instances, rosters and
// projects, so Resolve/Deliver tests don't need a real *harbor.FileStore.
type fakeStore struct {
	insts    []harbor.Instance
	roster   map[string]harbor.Roster
	projects map[string]harbor.Project
}

func (f *fakeStore) ListInstances() []harbor.Instance { return f.insts }
func (f *fakeStore) GetRoster(p string) (harbor.Roster, bool) {
	r, ok := f.roster[p]
	return r, ok
}

// GetProject returns the fake project record, if any was seeded. Tests that
// never populate projects get ok=false, which (for Resolve's purposes)
// behaves like a zero Project — ChatService=="" — preserving the Linear-only
// path.
func (f *fakeStore) GetProject(name string) (harbor.Project, bool) {
	p, ok := f.projects[name]
	return p, ok
}

// newRosterStore builds a fakeStore with a single Instance and the project's
// Roster preloaded (no ChatService set — Linear-only routing).
func newRosterStore(t *testing.T, project string, inst harbor.Instance, roster harbor.Roster) *fakeStore {
	t.Helper()
	return &fakeStore{
		insts:    []harbor.Instance{inst},
		roster:   map[string]harbor.Roster{project: roster},
		projects: map[string]harbor.Project{project: {Name: project, Roster: roster}},
	}
}

// tgt is a tiny msglog.Target builder for readable Resolve test cases.
func tgt(kind, ref string) msglog.Target { return msglog.Target{Kind: kind, Ref: ref} }

// fakePoster is a fake commentPoster: canned identifier→id resolution and
// recorded posts, so Deliver is testable without a live Linear client.
type fakePoster struct {
	posts               []struct{ issueID, body string }
	idByID              map[string]string // identifier -> internal id
	postErr, resolveErr error
}

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

// TestEgressGoldenParity is the byte-parity gate: the egress rendering path
// (directory.Resolve + linearSurface.Deliver) must post exactly the same
// (issueID, body) pairs the pre-cutover direct-post handlePost produced.
func TestEgressGoldenParity(t *testing.T) {
	st := newRosterStore(t, "acme",
		harbor.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		harbor.Roster{
			Humans:   []harbor.Human{{Name: "alice", Handle: "alice.h"}},
			Channels: []harbor.Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-9"}},
		})
	poster := &fakePoster{idByID: map[string]string{"ACME-7": "iss_7", "ACME-9": "iss_9"}}
	dir := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}
	surf := &linearSurface{poster: poster}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}

	cases := []struct {
		name    string
		to      msglog.Target
		body    string
		wantID  string
		wantBod string
	}{
		{"own", msglog.Target{Kind: "channel", Ref: "ACME-7"}, "hi", "iss_7", "hi"},
		{"human", msglog.Target{Kind: "human", Ref: "alice"}, "ping", "iss_7", "@alice.h ping"},
		{"channel", msglog.Target{Kind: "channel", Ref: "eng-help"}, "heads up", "iss_9", "heads up"},
	}
	for _, c := range cases {
		d, ok := dir.Resolve("linear", "acme", c.to, from)
		if !ok {
			t.Fatalf("%s: Resolve ok=false", c.name)
		}
		if _, err := surf.Deliver(context.Background(), d, msglog.Message{From: from, To: []msglog.Target{c.to}, Body: c.body, Project: "acme"}); err != nil {
			t.Fatalf("%s: Deliver: %v", c.name, err)
		}
	}
	want := []struct{ issueID, body string }{
		{"iss_7", "hi"}, {"iss_7", "@alice.h ping"}, {"iss_9", "heads up"},
	}
	if len(poster.posts) != len(want) {
		t.Fatalf("posts = %+v, want %+v", poster.posts, want)
	}
	for i, w := range want {
		if poster.posts[i] != w {
			t.Fatalf("post %d = %+v, want %+v", i, poster.posts[i], w)
		}
	}
}

// TestResolveDiscordRouting exercises the service-aware Resolve: a project
// on Discord for human DMs, a human with no discord profile (Linear
// fallback), a roster channel owned by discord, and a roster channel still
// owned by linear (COV-179: the discord engine must not claim it).
func TestResolveDiscordRouting(t *testing.T) {
	roster := harbor.Roster{
		Humans: []harbor.Human{
			{Name: "alice", Handle: "alice.h", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "inbox-A"}}},
			{Name: "bob", Handle: "bob.h"}, // no discord profile
		},
		Channels: []harbor.Channel{
			{Name: "eng", Service: "discord", Ref: "disc-eng"},
			{Name: "tick", Service: "linear", Ref: "ACME-9"},
		},
	}
	st := &fakeStore{
		insts:    []harbor.Instance{{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"}},
		roster:   map[string]harbor.Roster{"acme": roster},
		projects: map[string]harbor.Project{"acme": {Name: "acme", Roster: roster, ChatService: "discord"}},
	}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}
	dir := &directory{store: st, project: "acme"}

	// discord human via discord engine
	if d, ok := dir.Resolve("discord", "acme", tgt("human", "alice"), from); !ok || d.Service != "discord" || d.Address != "inbox-A" || d.BodyPrefix != "cove-1: " {
		t.Fatalf("discord human: %+v %v", d, ok)
	}
	// linear engine does NOT own the discord human
	if _, ok := dir.Resolve("linear", "acme", tgt("human", "alice"), from); ok {
		t.Fatal("linear must not own a discord-routed human")
	}
	// fallback: bob has no discord profile → Linear @mention (linear engine)
	if d, ok := dir.Resolve("linear", "acme", tgt("human", "bob"), from); !ok || d.Service != "linear" || d.Address != "ACME-7" || d.BodyPrefix != "@bob.h " {
		t.Fatalf("fallback human: %+v %v", d, ok)
	}
	if _, ok := dir.Resolve("discord", "acme", tgt("human", "bob"), from); ok {
		t.Fatal("discord must not own a profileless human (linear fallback owns it)")
	}
	// discord channel
	if d, ok := dir.Resolve("discord", "acme", tgt("channel", "eng"), from); !ok || d.Address != "disc-eng" || d.BodyPrefix != "cove-1: " {
		t.Fatalf("discord channel: %+v %v", d, ok)
	}
	// linear channel — discord engine must NOT own it (COV-179)
	if _, ok := dir.Resolve("discord", "acme", tgt("channel", "tick"), from); ok {
		t.Fatal("discord must not own a linear channel")
	}
	if d, ok := dir.Resolve("linear", "acme", tgt("channel", "tick"), from); !ok || d.Address != "ACME-9" || d.BodyPrefix != "" {
		t.Fatalf("linear channel: %+v %v", d, ok)
	}
	// own-ticket (not a roster channel) → linear raw
	if d, ok := dir.Resolve("linear", "acme", tgt("channel", "ACME-7"), from); !ok || d.Address != "ACME-7" || d.BodyPrefix != "" {
		t.Fatalf("own ticket: %+v %v", d, ok)
	}
}

// TestResolveNonDiscordProjectFallsBackToLinear proves a project that never
// opted into Discord (ChatService=="") always resolves humans via Linear,
// and the discord engine never owns any of its targets.
func TestResolveNonDiscordProjectFallsBackToLinear(t *testing.T) {
	st := newRosterStore(t, "acme",
		harbor.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}})
	dir := &directory{store: st, project: "acme"}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}

	if d, ok := dir.Resolve("linear", "acme", tgt("human", "alice"), from); !ok || d.Service != "linear" || d.Address != "ACME-7" || d.BodyPrefix != "@alice.h " {
		t.Fatalf("non-discord project human: %+v %v", d, ok)
	}
	if _, ok := dir.Resolve("discord", "acme", tgt("human", "alice"), from); ok {
		t.Fatal("discord must not own a human on a non-discord project")
	}
}

func TestResolveUnroutableAndNonLinear(t *testing.T) {
	st := newRosterStore(t, "acme",
		harbor.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}})
	dir := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}
	// unknown human
	if _, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "human", Ref: "nobody"}, from); ok {
		t.Fatal("unknown human should be unresolved")
	}
	// channel that's not a roster name is treated as a direct ticket
	// identifier (own-ticket delivery must not depend on a live instance).
	if d, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "channel", Ref: "ACME-999"}, from); !ok || d.Address != "ACME-999" {
		t.Fatalf("non-roster channel should resolve to a direct ticket, got %+v ok=%v", d, ok)
	}
	// non-linear service
	if _, ok := dir.Resolve("discord", "acme", msglog.Target{Kind: "human", Ref: "alice"}, from); ok {
		t.Fatal("non-linear service should be unresolved")
	}
	// sender with no instance → human unresolved
	if _, ok := dir.Resolve("linear", "acme", msglog.Target{Kind: "human", Ref: "alice"}, msglog.Target{Kind: "actor", Ref: "ghost"}); ok {
		t.Fatal("human target with no sender instance should be unresolved")
	}
}

// TestResolveOwnTicketSurvivesInstanceGone proves own-ticket delivery no
// longer needs a live Instance: the cove that sent the message may already
// have been torn down (RemoveInstance) by the time the egress loop runs.
func TestResolveOwnTicketSurvivesInstanceGone(t *testing.T) {
	st := &fakeStore{
		insts:  nil, // no live instances — the sending cove is already gone
		roster: map[string]harbor.Roster{"acme": {}},
	}
	dir := &directory{store: st, project: "acme", selfIdentity: "harbor-bot"}
	to := msglog.Target{Kind: "channel", Ref: "ACME-7"}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}
	d, ok := dir.Resolve("linear", "acme", to, from)
	if !ok {
		t.Fatal("own-ticket resolve must succeed even with no live instances")
	}
	if d.Address != "ACME-7" {
		t.Fatalf("Address = %q, want ACME-7", d.Address)
	}
	if d.BodyPrefix != "" {
		t.Fatalf("BodyPrefix = %q, want empty", d.BodyPrefix)
	}
}

func TestDeliverPropagatesErrors(t *testing.T) {
	m := msglog.Message{Body: "x"}
	d := msgport.Delivery{Service: "linear", Address: "ACME-7"}
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
	want := msgport.EgressMark{LastMsg: "id-9", Pending: map[string]map[string]bool{"id-10": {"human:a": true}}}
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
	if got.LastMsg != "id-9" || !got.Pending["id-10"]["human:a"] {
		t.Fatalf("reloaded mark = %+v, want %+v", got, want)
	}
	if got := m2.Egress("discord"); got.LastMsg != "" || got.Pending != nil {
		t.Fatalf("unset service must be zero EgressMark, got %+v", got)
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
// harbor runs two msgport engines (linear + discord) sharing one
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

	if err := m.SetEgress("linear", msgport.EgressMark{
		LastMsg: "a",
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
	orig := msgport.EgressMark{
		LastMsg: "b",
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
