package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
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

func newTestStore(t *testing.T) *jam.MemStore {
	t.Helper()
	st := jam.NewMemStore()
	return st
}

func TestDirectoryRoute(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme")
	if err := st.PutInstance(jam.Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}); err != nil {
		t.Fatalf("PutInstance: %v", err)
	}
	if err := st.AddChannel("acme", jam.Channel{Name: "eng", Service: "linear", Ref: "ACME-9"}); err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	d := &directory{store: st, project: "acme", selfIdentity: "jam-bot"}

	// self-post → dropped
	if _, _, _, ok := d.Route("linear", "acme", relay.Event{Author: "jam-bot", Surface: "ACME-42"}); ok {
		t.Fatal("self-authored comment must be dropped")
	}
	// human reply on a cove's ticket → actor
	from, to, _, ok := d.Route("linear", "acme", relay.Event{Author: "Brent", Surface: "ACME-42", ForeignID: "c1"})
	if !ok || from.Kind != "human" || from.Ref != "Brent" || len(to) != 1 || to[0].Kind != "actor" || to[0].Ref != "cove-1" {
		t.Fatalf("cove route: %v %+v %+v", ok, from, to)
	}
	// reply on a channel's thread → channel
	_, to, replyTo, ok := d.Route("linear", "acme", relay.Event{Author: "Brent", Surface: "ACME-9", ReplyToForeign: "p1"})
	if !ok || to[0].Kind != "channel" || to[0].Ref != "eng" || replyTo != "in:linear:p1" {
		t.Fatalf("channel route: %v %+v %q", ok, to, replyTo)
	}
	// unknown issue → unrouted
	if _, _, _, ok := d.Route("linear", "acme", relay.Event{Author: "Brent", Surface: "ACME-999"}); ok {
		t.Fatal("unknown issue must be unrouted")
	}
	// Projects + Resolve on an empty/invalid target
	if got := d.Projects("linear"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("Projects = %v", got)
	}
	if _, ok := d.Resolve("linear", "acme", intercom.Target{}, intercom.Target{}); ok {
		t.Fatal("Resolve of an empty target must be unresolved")
	}
}

// TestRouteDiscord exercises the discord side of the service-aware Route: a
// human's reply to a known receipt maps to the cove that receipt names; a
// non-reply event and a reply to an unknown id both drop.
func TestRouteDiscord(t *testing.T) {
	st := newTestStore(t)
	rec := mustReceipts(t)
	if err := rec.Record("D1", "cove-1", "M1"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dir := &directory{store: st, project: "acme", receipts: rec}

	// reply to a known receipt → routes to the cove, replying to the message
	// the receipt names
	from, to, replyTo, ok := dir.Route("discord", "acme", relay.Event{Author: "alice", ReplyToForeign: "D1", ForeignID: "m2"})
	if !ok || from.Ref != "alice" || len(to) != 1 || to[0] != (intercom.Target{Kind: "actor", Ref: "cove-1"}) || replyTo != "M1" {
		t.Fatalf("routeDiscord reply: from=%+v to=%+v replyTo=%q ok=%v", from, to, replyTo, ok)
	}
	// not a reply → drop
	if _, _, _, ok := dir.Route("discord", "acme", relay.Event{Author: "alice"}); ok {
		t.Fatal("non-reply must drop")
	}
	// reply to unknown id → drop
	if _, _, _, ok := dir.Route("discord", "acme", relay.Event{Author: "alice", ReplyToForeign: "D9"}); ok {
		t.Fatal("unknown-ref must drop")
	}
}

// A reply posted in an inbox channel that is exactly one roster human's
// discord delivery address is attributed to that human (by roster name, not
// the Discord display name); a reply in a shared channel keeps the display
// name.
func TestRouteDiscordAttributesInboxOwner(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme")
	for _, h := range []jam.Human{
		{Name: "alice", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-A"}}},
		{Name: "bob", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "shared"}}},
		{Name: "carol", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "shared"}}},
	} {
		if err := st.AddHuman("acme", h); err != nil {
			t.Fatal(err)
		}
	}
	rec := mustReceipts(t)
	if err := rec.Record("D1", "cove-1", "M1"); err != nil {
		t.Fatal(err)
	}
	dir := &directory{store: st, receipts: rec}

	from, _, _, ok := dir.Route("discord", "acme", relay.Event{Author: "Alice Display", Surface: "inbox-A", ReplyToForeign: "D1", ForeignID: "m2"})
	if !ok || from != (intercom.Target{Kind: "human", Ref: "alice"}) {
		t.Fatalf("inbox reply from = %+v ok=%v, want human:alice", from, ok)
	}
	from, _, _, ok = dir.Route("discord", "acme", relay.Event{Author: "Bob Display", Surface: "shared", ReplyToForeign: "D1", ForeignID: "m3"})
	if !ok || from != (intercom.Target{Kind: "human", Ref: "Bob Display"}) {
		t.Fatalf("shared-channel reply from = %+v ok=%v, want the display name", from, ok)
	}
	// the inbox belongs to alice in acme only: another project's reply there
	// is not attributed to her.
	from, _, _, ok = dir.Route("discord", "other", relay.Event{Author: "Mallory", Surface: "inbox-A", ReplyToForeign: "D1", ForeignID: "m4"})
	if !ok || from != (intercom.Target{Kind: "human", Ref: "Mallory"}) {
		t.Fatalf("other-project reply from = %+v ok=%v", from, ok)
	}
}

// Once a human is bound to a Discord user id, a reply is attributed by its
// author id: the bound human from any channel (even a shared one), and never
// someone else posting in the bound human's inbox. A bot is never a roster
// human.
func TestRouteDiscordAttributesByAuthorID(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme")
	for _, h := range []jam.Human{
		{Name: "alice", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-A", UserID: "111"}}},
		{Name: "bob", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "shared", UserID: "222"}}},
		{Name: "carol", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "shared"}}},
		{Name: "dave", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-D"}}},
	} {
		if err := st.AddHuman("acme", h); err != nil {
			t.Fatal(err)
		}
	}
	rec := mustReceipts(t)
	if err := rec.Record("D1", "cove-1", "M1"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	dir := &directory{store: st, receipts: rec, log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	route := func(ev relay.Event) intercom.Target {
		t.Helper()
		ev.ReplyToForeign, ev.ForeignID = "D1", "m-"+ev.AuthorID
		from, _, _, ok := dir.Route("discord", "acme", ev)
		if !ok {
			t.Fatalf("route %+v dropped", ev)
		}
		return from
	}
	if got := route(relay.Event{Author: "Bob D.", AuthorID: "222", Surface: "shared", Body: "release"}); got != (intercom.Target{Kind: "human", Ref: "bob"}) {
		t.Fatalf("bound author in a shared inbox = %+v, want human:bob", got)
	}
	if got := route(relay.Event{Author: "Mallory", AuthorID: "999", Surface: "inbox-A"}); got != (intercom.Target{Kind: "human", Ref: "Mallory"}) {
		t.Fatalf("stranger in bound alice's inbox = %+v, want the display name", got)
	}
	if got := route(relay.Event{Author: "SomeBot", AuthorID: "111", AuthorBot: true, Surface: "inbox-A"}); got != (intercom.Target{Kind: "human", Ref: "SomeBot"}) {
		t.Fatalf("bot = %+v, want the display name", got)
	}
	if got := route(relay.Event{Author: "Dave D.", AuthorID: "444", Surface: "inbox-D"}); got != (intercom.Target{Kind: "human", Ref: "dave"}) {
		t.Fatalf("unbound owner's own inbox = %+v, want human:dave", got)
	}
	if !strings.Contains(logs.String(), "by=id") || !strings.Contains(logs.String(), "by=channel") {
		t.Fatalf("debug log lacks by=id:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "release") {
		t.Fatalf("debug log carries the message body:\n%s", logs.String())
	}
}

// A legacy receipt (no message id) still routes to its cove, with the old
// opaque in:discord:<id> ReplyTo.
func TestRouteDiscordLegacyReceipt(t *testing.T) {
	st := newTestStore(t)
	rec := mustReceipts(t)
	if err := rec.Record("D1", "cove-1", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dir := &directory{store: st, project: "acme", receipts: rec}
	_, to, replyTo, ok := dir.Route("discord", "acme", relay.Event{Author: "alice", ReplyToForeign: "D1", ForeignID: "m2"})
	if !ok || len(to) != 1 || to[0].Ref != "cove-1" || replyTo != "in:discord:D1" {
		t.Fatalf("legacy route: to=%+v replyTo=%q ok=%v", to, replyTo, ok)
	}
}

// A Discord reply to a cove's message joins that message's thread: delivering
// the squawk records its id in the receipt, and the routed reply's ReplyTo is
// that id, so ReadThread(root) returns both.
func TestDiscordReplyJoinsThread(t *testing.T) {
	st := newTestStore(t)
	lg := openTestLog(t)
	rec := mustReceipts(t)
	dir := &directory{store: st, project: "acme", receipts: rec}
	client := &fakeDiscordClient{postID: "D-root"}
	surf := &discordSurface{dial: func([]string) discordClient { return client }, receipts: rec}

	root, err := lg.Append(intercom.Squawk{From: intercom.Target{Kind: "actor", Ref: "cove-1"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}}, Body: "question?", Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surf.Deliver(context.Background(), relay.Delivery{Address: "inbox-A"}, root); err != nil {
		t.Fatal(err)
	}
	from, to, replyTo, ok := dir.Route("discord", "acme", relay.Event{Author: "alice", ReplyToForeign: "D-root", ForeignID: "D-reply", Body: "answer"})
	if !ok {
		t.Fatal("reply not routed")
	}
	if _, err := lg.Append(intercom.Squawk{ID: "in:discord:D-reply", From: from, To: to, Body: "answer", Project: "acme", ReplyTo: replyTo}); err != nil {
		t.Fatal(err)
	}
	th := lg.ReadThread(root.ID)
	if len(th) != 2 || th[0].ID != root.ID || th[1].Body != "answer" {
		t.Fatalf("thread = %+v, want root + reply", th)
	}
}

// fakeStore is a minimal instanceRoster: canned instances, rosters and
// projects, so Resolve/Deliver tests don't need a real *jam.MemStore.
type fakeStore struct {
	insts    []jam.Instance
	roster   map[string]jam.Roster
	projects map[string]jam.Project
}

func (f *fakeStore) ListInstances() []jam.Instance { return f.insts }
func (f *fakeStore) GetRoster(p string) (jam.Roster, bool) {
	r, ok := f.roster[p]
	return r, ok
}

// GetProject returns the fake project record, if any was seeded. Tests that
// never populate projects get ok=false, which (for Resolve's purposes)
// behaves like a zero Project — ChatService=="" — preserving the Linear-only
// path.
func (f *fakeStore) GetProject(name string) (jam.Project, bool) {
	p, ok := f.projects[name]
	return p, ok
}

// GetConnection treats a seeded Project.ChatService as a connection id whose
// kind is the id itself, so a fake project with ChatService "discord" is a
// discord chat service (the real store holds a con_ id there).
func (f *fakeStore) GetConnection(id ident.ID) (jam.Connection, bool) {
	return jam.Connection{ID: id, Kind: string(id), Name: string(id), Status: jam.StatusLive}, true
}

// ListProjects returns the seeded project names (Directory.Projects("discord")).
func (f *fakeStore) ListProjects() []string {
	out := make([]string, 0, len(f.projects))
	for n := range f.projects {
		out = append(out, n)
	}
	return out
}

// newRosterStore builds a fakeStore with a single Instance and the project's
// Roster preloaded (no ChatService set — Linear-only routing).
func newRosterStore(t *testing.T, project string, inst jam.Instance, roster jam.Roster) *fakeStore {
	t.Helper()
	return &fakeStore{
		insts:    []jam.Instance{inst},
		roster:   map[string]jam.Roster{project: roster},
		projects: map[string]jam.Project{project: {Name: project, Roster: roster}},
	}
}

// tgt is a tiny intercom.Target builder for readable Resolve test cases.
func tgt(kind, ref string) intercom.Target { return intercom.Target{Kind: kind, Ref: ref} }

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
		jam.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		jam.Roster{
			Humans:   []jam.Human{{Name: "alice", Handle: "alice.h"}},
			Channels: []jam.Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-9"}},
		})
	poster := &fakePoster{idByID: map[string]string{"ACME-7": "iss_7", "ACME-9": "iss_9"}}
	dir := &directory{store: st, project: "acme", selfIdentity: "jam-bot"}
	surf := &linearSurface{poster: poster}
	from := intercom.Target{Kind: "actor", Ref: "cove-1"}

	cases := []struct {
		name    string
		to      intercom.Target
		body    string
		wantID  string
		wantBod string
	}{
		{"own", intercom.Target{Kind: "channel", Ref: "ACME-7"}, "hi", "iss_7", "hi"},
		{"human", intercom.Target{Kind: "human", Ref: "alice"}, "ping", "iss_7", "@alice.h ping"},
		{"channel", intercom.Target{Kind: "channel", Ref: "eng-help"}, "heads up", "iss_9", "heads up"},
	}
	for _, c := range cases {
		d, ok := dir.Resolve("linear", "acme", c.to, from)
		if !ok {
			t.Fatalf("%s: Resolve ok=false", c.name)
		}
		if _, err := surf.Deliver(context.Background(), d, intercom.Squawk{From: from, To: []intercom.Target{c.to}, Body: c.body, Project: "acme"}); err != nil {
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
	roster := jam.Roster{
		Humans: []jam.Human{
			{Name: "alice", Handle: "alice.h", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-A"}}},
			{Name: "bob", Handle: "bob.h"}, // no discord profile
		},
		Channels: []jam.Channel{
			{Name: "eng", Service: "discord", Ref: "disc-eng"},
			{Name: "tick", Service: "linear", Ref: "ACME-9"},
		},
	}
	st := &fakeStore{
		insts:    []jam.Instance{{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"}},
		roster:   map[string]jam.Roster{"acme": roster},
		projects: map[string]jam.Project{"acme": {Name: "acme", Roster: roster, ChatService: "discord"}},
	}
	from := intercom.Target{Kind: "actor", Ref: "cove-1"}
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
		jam.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}})
	dir := &directory{store: st, project: "acme"}
	from := intercom.Target{Kind: "actor", Ref: "cove-1"}

	if d, ok := dir.Resolve("linear", "acme", tgt("human", "alice"), from); !ok || d.Service != "linear" || d.Address != "ACME-7" || d.BodyPrefix != "@alice.h " {
		t.Fatalf("non-discord project human: %+v %v", d, ok)
	}
	if _, ok := dir.Resolve("discord", "acme", tgt("human", "alice"), from); ok {
		t.Fatal("discord must not own a human on a non-discord project")
	}
}

func TestResolveUnroutableAndNonLinear(t *testing.T) {
	st := newRosterStore(t, "acme",
		jam.Instance{ActorID: "cove-1", Unit: "ACME-7", Project: "acme"},
		jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}})
	dir := &directory{store: st, project: "acme", selfIdentity: "jam-bot"}
	from := intercom.Target{Kind: "actor", Ref: "cove-1"}
	// unknown human
	if _, ok := dir.Resolve("linear", "acme", intercom.Target{Kind: "human", Ref: "nobody"}, from); ok {
		t.Fatal("unknown human should be unresolved")
	}
	// channel that's not a roster name is treated as a direct ticket
	// identifier (own-ticket delivery must not depend on a live instance).
	if d, ok := dir.Resolve("linear", "acme", intercom.Target{Kind: "channel", Ref: "ACME-999"}, from); !ok || d.Address != "ACME-999" {
		t.Fatalf("non-roster channel should resolve to a direct ticket, got %+v ok=%v", d, ok)
	}
	// non-linear service
	if _, ok := dir.Resolve("discord", "acme", intercom.Target{Kind: "human", Ref: "alice"}, from); ok {
		t.Fatal("non-linear service should be unresolved")
	}
	// sender with no instance → human unresolved
	if _, ok := dir.Resolve("linear", "acme", intercom.Target{Kind: "human", Ref: "alice"}, intercom.Target{Kind: "actor", Ref: "ghost"}); ok {
		t.Fatal("human target with no sender instance should be unresolved")
	}
}

// TestResolveOwnTicketSurvivesInstanceGone proves own-ticket delivery no
// longer needs a live Instance: the cove that sent the message may already
// have been torn down (RemoveInstance) by the time the egress loop runs.
func TestResolveOwnTicketSurvivesInstanceGone(t *testing.T) {
	st := &fakeStore{
		insts:  nil, // no live instances — the sending cove is already gone
		roster: map[string]jam.Roster{"acme": {}},
	}
	dir := &directory{store: st, project: "acme", selfIdentity: "jam-bot"}
	to := intercom.Target{Kind: "channel", Ref: "ACME-7"}
	from := intercom.Target{Kind: "actor", Ref: "cove-1"}
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
	from := tgt("actor", "cove-1")
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

// Linear ingress records a comment's author as an account by their Linear
// user id only — never by display name, which anyone can set: a stranger
// naming themselves after a member's handle must not become them. A comment is
// the user's once an operator has linked that account, and only in a project
// the user is a member of.
func TestRouteLinearAttributesByLinkedAccount(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme", "beta")
	if err := st.AddHuman("acme", jam.Human{Name: "alice", Handle: "alice.l"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHuman("beta", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutInstance(jam.Instance{ActorID: "cove-1", Project: "acme", Unit: "ACME-1"}); err != nil {
		t.Fatal(err)
	}
	d := &directory{store: st, accounts: st, project: "acme"}
	route := func(author, id string) intercom.Target {
		t.Helper()
		from, to, _, ok := d.Route("linear", "acme", relay.Event{Surface: "ACME-1", Author: author, AuthorID: id, Body: "hi"})
		if !ok || to[0].Ref != "cove-1" {
			t.Fatalf("route %s = %v, %v", author, to, ok)
		}
		return from
	}
	lin, _ := st.ConnectionOfKind("linear")

	// A stranger using alice's handle as their display name stays a stranger,
	// and alice's handle account is untouched.
	if from := route("alice.l", "lin-evil"); from.Ref != "alice.l" {
		t.Fatalf("spoofed handle attributed as %v", from)
	}
	if a, _ := st.AccountByHandle(lin.ID, "alice.l"); a.ServiceUID != "" {
		t.Fatalf("alice's handle account learned the stranger's uid: %+v", a)
	}
	if a, ok := st.AccountByUID(lin.ID, "lin-evil"); !ok || a.UserID != "" || a.Label != "alice.l" {
		t.Fatalf("stranger's account = %+v, %v", a, ok)
	}

	// Once an operator links alice's real account, her comments are hers,
	// whatever she calls herself on Linear.
	route("Alice", "lin-alice")
	a, _ := st.AccountByUID(lin.ID, "lin-alice")
	alice, _ := st.LookupName(ident.User, "alice")
	if err := st.LinkAccount(a.ID, alice); err != nil {
		t.Fatal(err)
	}
	if from := route("Alice Renamed", "lin-alice"); from != (intercom.Target{Kind: "human", Ref: "alice"}) {
		t.Fatalf("linked author = %v, want alice", from)
	}

	// A linked user who isn't a member of this project names nobody here.
	route("Bob", "lin-bob")
	b, _ := st.AccountByUID(lin.ID, "lin-bob")
	bob, _ := st.LookupName(ident.User, "bob")
	if err := st.LinkAccount(b.ID, bob); err != nil {
		t.Fatal(err)
	}
	if from := route("Bob", "lin-bob"); from.Ref != "Bob" {
		t.Fatalf("non-member linked author = %v, want the display name", from)
	}
}

// An unknown Discord author of a routed reply is recorded as an unlinked
// account; attribution is unchanged (their display name).
func TestRouteDiscordRecordsUnknownAuthors(t *testing.T) {
	st := newTestStore(t)
	mustCreateProject(t, st, "acme")
	rc, err := newFileReceipts(t.TempDir() + "/r.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Record("m1", "cove-1", "sq-1"); err != nil {
		t.Fatal(err)
	}
	d := &directory{store: st, accounts: st, project: "acme", receipts: rc}
	from, _, _, ok := d.Route("discord", "acme", relay.Event{Surface: "chan", Author: "Zed", AuthorID: "999", ReplyToForeign: "m1", Body: "hi"})
	if !ok || from.Ref != "Zed" {
		t.Fatalf("route = %v, %v", from, ok)
	}
	dc, _ := st.ConnectionOfKind("discord")
	if a, ok := st.AccountByUID(dc.ID, "999"); !ok || a.UserID != "" || a.Label != "Zed" {
		t.Fatalf("account = %+v, %v", a, ok)
	}
	// A bot is never recorded.
	d.Route("discord", "acme", relay.Event{Surface: "chan", Author: "Bot", AuthorID: "777", AuthorBot: true, ReplyToForeign: "m1", Body: "x"})
	if _, ok := st.AccountByUID(dc.ID, "777"); ok {
		t.Fatal("a bot must not become an account")
	}
}
