package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/relay"
	"github.com/aethons-tools/cove/internal/tracker"
	"github.com/aethons-tools/cove/internal/tracker/trackertest"
)

func TestOpenTrackerPicksTheBlock(t *testing.T) {
	gh := trackertest.NewGitHub(t, "acme/board", "jam-bot")
	trk, err := openTracker(&requisitionerConfig{GitHub: &kit.GitHubTracker{Repo: "acme/board", States: trackertest.GitHubStates()}}, trackertest.Token, gh.HTTPClient())
	if _, ok := trk.(*githubissues.Client); err != nil || !ok {
		t.Fatalf("github block → %T, %v", trk, err)
	}
	lin := trackertest.NewLinear(t, "ACME", "Jam")
	trk, err = openTracker(&requisitionerConfig{Linear: &kit.LinearTracker{Team: "ACME", States: trackertest.LinearStates()}}, trackertest.Token, lin.HTTPClient())
	if _, ok := trk.(*linear.Client); err != nil || !ok {
		t.Fatalf("linear block → %T, %v", trk, err)
	}
	if trk, err := openTracker(&requisitionerConfig{GitHub: &kit.GitHubTracker{Repo: "bad"}}, "tok", nil); err == nil || trk != nil {
		t.Fatalf("a failed open = %v (%T); want a nil interface and an error", err, trk)
	}
}

func TestTicketerOnBothTrackers(t *testing.T) {
	ctx := context.Background()
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			err := trackerTicketer{c.Tracker}.Report(ctx, jam.Instance{Unit: c.Key},
				jam.TicketReport{State: "in-review", Summary: "PR is up", PR: "https://github.com/acme/board/pull/9"})
			if err != nil || c.State() != "in-review" || !slices.Contains(c.Posted(), "**in-review** — PR is up\n\nPR: https://github.com/acme/board/pull/9") {
				t.Fatalf("report: state %q, posted %q, %v", c.State(), c.Posted(), err)
			}
			if err := (trackerTicketer{c.Tracker}).BlockUnfinished(ctx, jam.Instance{Unit: c.Key}, "wrapped up"); err != nil || c.State() != "blocked" {
				t.Fatalf("block: state %q, %v", c.State(), err)
			}
		})
	}
	gh := trackertest.Both(t)[1]
	if err := (trackerTicketer{gh.Tracker}).Report(ctx, jam.Instance{Unit: "#1"}, jam.TicketReport{State: "done"}); err == nil || !strings.Contains(err.Error(), "github ticket key must be owner/repo#<n>") {
		t.Fatalf("report on a bare #1 = %v", err)
	}
}

type limitedFeed struct {
	calls int
	err   error
}

func (f *limitedFeed) CommentFeed(context.Context, time.Time, int) ([]tracker.FeedComment, error) {
	f.calls++
	return nil, f.err
}

// Review Focus 2: after a rate limit the surface makes no call until
// Retry-After has passed, and its cursor holds.
func TestTrackerSurfaceWaitsOutRateLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f := &limitedFeed{err: &tracker.RateLimitError{Service: "github", RetryAfter: time.Minute}}
	s := &trackerSurface{service: "github", feed: f, started: now, now: func() time.Time { return now }}
	cur := now.Format(time.RFC3339Nano)
	if _, next, err := s.Poll(ctx, "acme", cur); err == nil || next != cur {
		t.Fatalf("rate-limited poll = %q, %v; want the cursor and the error", next, err)
	}
	now = now.Add(30 * time.Second)
	if evs, next, err := s.Poll(ctx, "acme", cur); err != nil || len(evs) != 0 || next != cur || f.calls != 1 {
		t.Fatalf("poll inside the wait = %v %q %v, calls %d; want a quiet no-op", evs, next, err, f.calls)
	}
	now, f.err = now.Add(31*time.Second), nil
	if _, _, err := s.Poll(ctx, "acme", cur); err != nil || f.calls != 2 {
		t.Fatalf("poll after the wait: %v, calls %d", err, f.calls)
	}
	if s.Service() != "github" || (&trackerSurface{}).Service() != "linear" {
		t.Fatal("Service() is the tracker kind, linear by default")
	}
}

// F12: on the comment-derived cursor (Linear's path) an old comment the
// feed hands back (GitHub's since is by update time) never moves the cursor
// back, and a bot author is carried onto the event for the router.
func TestTrackerSurfaceCursorNeverMovesBack(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ff := &fakeFeeder{cs: []linear.FeedComment{
		{ID: "0", CreatedAt: since.Add(-time.Hour), IssueIdentifier: "acme/board#1", AuthorBot: true},
	}}
	s := &trackerSurface{service: "github", feed: ff}
	cur := since.Format(time.RFC3339Nano)
	evs, next, err := s.Poll(context.Background(), "acme", cur)
	if err != nil || next != cur || len(evs) != 1 || !evs[0].AuthorBot {
		t.Fatalf("poll = %+v %q %v; want the event, cursor %q, bot flagged", evs, next, err, cur)
	}
}

// cursorFeed is a feed that places its own cursor (tracker.CursorFeed).
type cursorFeed struct {
	cs    []tracker.FeedComment
	next  time.Time
	since time.Time
	plain int // CommentFeed calls (must be none)
}

func (f *cursorFeed) CommentFeed(context.Context, time.Time, int) ([]tracker.FeedComment, error) {
	f.plain++
	return f.cs, nil
}

func (f *cursorFeed) CommentFeedCursor(_ context.Context, since time.Time, _ int) ([]tracker.FeedComment, time.Time, error) {
	f.since = since
	return f.cs, f.next, nil
}

// A feed with its own cursor (GitHub's, which passes the comments it
// skipped) sets the surface's next; it never moves the cursor back.
func TestTrackerSurfaceUsesTheFeedCursor(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cur := since.Format(time.RFC3339Nano)
	f := &cursorFeed{cs: []tracker.FeedComment{{ID: "1", CreatedAt: since.Add(time.Second), IssueIdentifier: "acme/board#1"}}, next: since.Add(10 * time.Second)}
	s := &trackerSurface{service: "github", feed: f}
	evs, next, err := s.Poll(context.Background(), "acme", cur)
	if err != nil || len(evs) != 1 || next != since.Add(10*time.Second).Format(time.RFC3339Nano) || f.plain != 0 || !f.since.Equal(since) {
		t.Fatalf("poll = %+v %q %v (plain calls %d); want the feed's cursor", evs, next, err, f.plain)
	}
	f.cs, f.next = nil, since.Add(-time.Minute)
	if _, next, err := s.Poll(context.Background(), "acme", cur); err != nil || next != cur {
		t.Fatalf("an earlier feed cursor = %q, %v; want %q held", next, err, cur)
	}
}

// F4: a rate-limited delivery makes no call until Retry-After has passed and
// says so in a fixed error; the poll waits it out too (one token, one limit).
func TestTrackerSurfaceDeliverWaitsOutRateLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fp := &fakePoster{idByID: map[string]string{"acme/board#1": "1"}, postErr: &tracker.RateLimitError{Service: "github", RetryAfter: time.Minute}}
	f := &limitedFeed{}
	s := &trackerSurface{service: "github", feed: f, poster: fp, now: func() time.Time { return now }}
	d, m := relay.Delivery{Service: "github", Address: "acme/board#1"}, intercom.Squawk{Body: "x"}
	var rl *tracker.RateLimitError
	if _, err := s.Deliver(ctx, d, m); !errors.As(err, &rl) {
		t.Fatalf("rate-limited deliver = %v; want the RateLimitError", err)
	}
	fp.postErr = nil
	now = now.Add(30 * time.Second)
	if _, err := s.Deliver(ctx, d, m); err == nil || err.Error() != "github deliver: rate limited until 2026-10-26T12:01:00Z" || len(fp.posts) != 0 {
		t.Fatalf("deliver inside the wait = %v, posts %d; want the fixed error and no call", err, len(fp.posts))
	}
	if _, _, err := s.Poll(ctx, "acme", now.Format(time.RFC3339Nano)); err != nil || f.calls != 0 {
		t.Fatalf("poll inside the wait: %v, calls %d", err, f.calls)
	}
	now = now.Add(31 * time.Second)
	if _, err := s.Deliver(ctx, d, m); err != nil || len(fp.posts) != 1 {
		t.Fatalf("deliver after the wait = %v, posts %d", err, len(fp.posts))
	}
}

// F11: Linear's routing is as before — a comment whose author equals the
// viewer (even an empty one) is dropped, and Linear has no bot filter.
func TestRouteLinearSelfAndBotUnchanged(t *testing.T) {
	k := newRelayKit(t)
	if _, err := k.ic.HomeChannel(k.ticket); err != nil {
		t.Fatal(err)
	}
	k.dir.selfIdentity = ""
	if _, ok := k.dir.Route("linear", "acme", relay.Event{ForeignID: "c1", Surface: "ACME-7", AuthorID: "lin-9", Body: "hi"}); ok {
		t.Fatal("linear: an empty author still equals an unknown viewer and is dropped")
	}
	if _, ok := k.dir.Route("linear", "acme", relay.Event{ForeignID: "c2", Surface: "ACME-7", Author: "Robo", AuthorID: "lin-8", AuthorBot: true, Body: "hi"}); !ok {
		t.Fatal("linear: a bot-flagged comment is routed as before")
	}
}

// Review Focus 5 and the spec's relay end-to-end: on each tracker a person's
// reply lands in the ticket channel. Jam's own comment and (on GitHub) a bot's
// are dropped. A session's message goes back onto the issue.
func TestRelayEndToEndBothTrackers(t *testing.T) {
	ctx := context.Background()
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			st := jam.NewMemStore()
			mustCreateProject(t, st, "acme")
			p, _ := st.GetProject("acme")
			conn, err := st.CreateConnection(jam.Connection{Kind: c.Kind, Name: c.Kind})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.PutRole("acme", jam.Role{Name: "impl"}); err != nil {
				t.Fatal(err)
			}
			inst := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: c.Key, Phase: jam.PhaseLive}
			if err := st.PutInstance(inst); err != nil {
				t.Fatal(err)
			}
			trackerFn := func() (ident.ID, bool) { return conn.ID, true }
			lg := intercom.NewMemLog(nil)
			ic := jam.NewIntercom(st, trackerFn, lg, nil, nil)
			if err := ic.SetUp(inst); err != nil {
				t.Fatal(err)
			}
			self, err := c.Tracker.Viewer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			dir := &directory{store: st, ic: ic, project: string(p.ID), selfIdentity: self, tracker: trackerFn, trackerKind: c.Kind}
			surf := &trackerSurface{service: c.Kind, feed: c.Tracker, poster: c.Tracker, started: time.Now().Add(-2 * time.Minute)}

			now := time.Now().UTC().Truncate(time.Second)
			c.Comment("brent", "please rebase", now.Add(-60*time.Second))
			c.Comment(c.Self, "status from jam", now.Add(-50*time.Second))
			if c.GitHub != nil {
				c.GitHub.AddComment(trackertest.GHComment{Issue: 1, Body: "ci passed", Login: "ci[bot]", UserID: 99, Bot: true, CreatedAt: now.Add(-40 * time.Second)})
			}
			evs, _, err := surf.Poll(ctx, string(p.ID), "")
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			for _, ev := range evs {
				r, ok := dir.Route(c.Kind, string(p.ID), ev)
				if !ok {
					continue
				}
				m := intercom.Squawk{ID: "in:" + c.Kind + ":" + ev.ForeignID, From: r.From, Body: ev.Body, At: ev.At, Origin: r.Origin, OriginRef: r.OriginRef}
				if err := dir.Post(r, m); err != nil {
					t.Fatalf("post %q: %v", ev.Body, err)
				}
			}
			ch, ok := st.ChannelByKey(p.ID, jam.SourceTicket, string(conn.ID)+"/"+c.Key)
			if !ok {
				t.Fatal("no ticket channel")
			}
			var bodies []string
			for _, m := range lg.ChannelBefore(ch.ID, math.MaxInt64, 50) {
				bodies = append(bodies, m.Body)
			}
			if !slices.Contains(bodies, "please rebase") || slices.Contains(bodies, "status from jam") || slices.Contains(bodies, "ci passed") {
				t.Fatalf("ticket channel = %q; want only the person's reply", bodies)
			}

			pl, err := ic.Plan(jam.Poster{ID: ident.ID(inst.ActorID), Session: &inst, Actor: &jam.Actor{ID: inst.ActorID, Grants: []jam.Grant{{Project: "acme", Role: "impl"}}}}, "", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			m, err := ic.Post(pl, intercom.Squawk{From: ident.ID(inst.ActorID), Body: "rebased"})
			if err != nil {
				t.Fatal(err)
			}
			ds := dir.Surfaces(c.Kind, m)
			if len(ds) != 1 || ds[0].Service != c.Kind || ds[0].Address != c.Key {
				t.Fatalf("surfaces = %+v; want one %s delivery to %s", ds, c.Kind, c.Key)
			}
			if _, err := surf.Deliver(ctx, ds[0], m); err != nil || !slices.Contains(c.Posted(), "rebased") {
				t.Fatalf("deliver: posted %q, %v", c.Posted(), err)
			}
			if got := dir.Surfaces("linear", m); c.Kind == "github" && len(got) != 0 {
				t.Fatalf("a github ticket is not delivered to linear: %+v", got)
			}
		})
	}
}

// scriptedViewer answers Viewer from a script of errors, then ok.
type scriptedViewer struct {
	errs  []error
	self  string
	calls int
}

func (v *scriptedViewer) Viewer(context.Context) (string, error) {
	v.calls++
	if len(v.errs) > 0 {
		err := v.errs[0]
		v.errs = v.errs[1:]
		return "", err
	}
	return v.self, nil
}

// Fix round 1: on GitHub a failed Viewer (other than ErrNoViewer) is retried
// — after a rate limit's RetryAfter, else a backoff — and the relay starts
// only once it succeeds, so Jam's own comment is never routed meanwhile.
func TestTrackerSelfGitHubRetriesUntilKnown(t *testing.T) {
	ctx := context.Background()
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) bool { slept = append(slept, d); return true }

	gh := trackertest.Both(t)[1]
	v := &scriptedViewer{errs: []error{
		errors.New("githubissues: GET https://api.github.com/user: http 500: oops"),
		&tracker.RateLimitError{Service: "github", RetryAfter: 90 * time.Second},
	}, self: gh.Self}
	var started []string
	if self, ok := trackerSelf(ctx, "github", v.Viewer, log, sleep); ok {
		started = append(started, self)
	}
	if !slices.Equal(started, []string{"jam-bot"}) || v.calls != 3 || !slices.Equal(slept, []time.Duration{viewerRetryMin, 90 * time.Second}) {
		t.Fatalf("started %q after %d calls, slept %v; want jam-bot after a backoff and the RetryAfter", started, v.calls, slept)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 || strings.Contains(buf.String(), trackertest.Token) {
		t.Fatalf("want one warn per failed attempt, no token:\n%s", buf.String())
	}
	// With the identity known, Jam's own comment is dropped.
	dir := &directory{selfIdentity: started[0], trackerKind: "github", tracker: func() (ident.ID, bool) { return "", false }}
	if _, ok := dir.Route("github", "acme", relay.Event{ForeignID: "1", Surface: gh.Key, Author: gh.Self, AuthorID: "7"}); ok {
		t.Fatal("Jam's own comment routed")
	}

	// A context ending mid-wait never starts the relay.
	v = &scriptedViewer{errs: []error{errors.New("down")}}
	if _, ok := trackerSelf(ctx, "github", v.Viewer, log, func(context.Context, time.Duration) bool { return false }); ok {
		t.Fatal("ctx ended: the relay must not start")
	}
	// ErrNoViewer: no identity, at once; Linear: one lookup, as before.
	v = &scriptedViewer{errs: []error{fmt.Errorf("x: %w", tracker.ErrNoViewer)}}
	if self, ok := trackerSelf(ctx, "github", v.Viewer, log, sleep); !ok || self != "" || v.calls != 1 {
		t.Fatalf("ErrNoViewer = %q %v after %d calls", self, ok, v.calls)
	}
	v = &scriptedViewer{errs: []error{errors.New("down")}}
	if self, ok := trackerSelf(ctx, "linear", v.Viewer, log, sleep); !ok || self != "" || v.calls != 1 {
		t.Fatalf("linear = %q %v after %d calls; want one lookup", self, ok, v.calls)
	}
}

// Fix round 1: a GitHub comment's author is recorded on the tracker
// connection serve resolved, not the first github connection in the store.
func TestRouteRecordsAuthorOnTheTrackerConnection(t *testing.T) {
	st := jam.NewMemStore()
	mustCreateProject(t, st, "acme")
	other, err := st.CreateConnection(jam.Connection{Kind: "github", Name: "github"}) // what ConnectionOfKind("github") picks
	if err != nil {
		t.Fatal(err)
	}
	conn, err := st.CreateConnection(jam.Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("acme", jam.Role{Name: "impl"}); err != nil {
		t.Fatal(err)
	}
	inst := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: "acme/board#1", Phase: jam.PhaseLive}
	if err := st.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	trackerFn := func() (ident.ID, bool) { return conn.ID, true }
	ic := jam.NewIntercom(st, trackerFn, intercom.NewMemLog(nil), nil, nil)
	if err := ic.SetUp(inst); err != nil {
		t.Fatal(err)
	}
	dir := &directory{store: st, ic: ic, selfIdentity: "jam-bot", tracker: trackerFn, trackerKind: "github"}
	if _, ok := dir.Route("github", "acme", relay.Event{ForeignID: "1", Surface: "acme/board#1", Author: "brent", AuthorID: "42", Body: "hi"}); !ok {
		t.Fatal("unrouted")
	}
	if _, ok := st.AccountByUID(conn.ID, "42"); !ok {
		t.Fatal("the author is not recorded on the tracker connection")
	}
	if _, ok := st.AccountByUID(other.ID, "42"); ok {
		t.Fatal("the author is recorded on the other github connection")
	}
}

// githubKit is project acme on a GitHub-tracked Jam (gh-acme, repo
// acme/board) that also has a linear connection: carol holds a linear
// handle (carol.lin) and a github login (carol-gh); a ticket session on
// acme/board#1.
type githubKit struct {
	st     *jam.MemStore
	lg     *intercom.Log
	ic     *jam.Intercom
	dir    *directory
	conn   jam.Connection
	carol  jam.Member
	ticket jam.Instance
}

func newGitHubKit(t *testing.T) *githubKit {
	t.Helper()
	k := &githubKit{st: jam.NewMemStore()}
	st := k.st
	mustCreateProject(t, st, "acme")
	var err error
	if k.conn, err = st.CreateConnection(jam.Connection{Kind: "github", Name: "gh-acme"}); err != nil {
		t.Fatal(err)
	}
	lin, err := st.CreateConnection(jam.Connection{Kind: "linear", Name: "linear"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(st, "acme", jam.Human{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	uid, _ := st.LookupName(ident.User, "carol")
	for conn, handle := range map[ident.ID]string{lin.ID: "carol.lin", k.conn.ID: "carol-gh"} {
		a, err := st.UpsertAccount(jam.Account{ConnectionID: conn, Handle: handle})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkAccount(a.ID, uid); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := st.GetProject("acme")
	k.carol, _ = jam.MemberOf(st, p.ID, uid)
	if err := st.PutRole("acme", jam.Role{Name: "impl", Scope: jam.Scope{Addressing: []string{"user:*", "channel:*"}}}); err != nil {
		t.Fatal(err)
	}
	k.ticket = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: "acme/board#1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting}
	if err := st.PutInstance(k.ticket); err != nil {
		t.Fatal(err)
	}
	trackerFn := func() (ident.ID, bool) { return k.conn.ID, true }
	k.lg = intercom.NewMemLog(nil)
	k.ic = jam.NewIntercom(st, trackerFn, k.lg, nil, nil)
	if err := k.ic.SetUp(k.ticket); err != nil {
		t.Fatal(err)
	}
	k.dir = &directory{store: st, ic: k.ic, project: "acme", selfIdentity: "jam-bot", tracker: trackerFn, trackerKind: "github"}
	return k
}

// Final fix 2: on a GitHub-tracked Jam an escalation @-mentions a member by
// their github login, not their linear handle.
func TestSurfacesEscalationUsesTheTrackerKindsHandle(t *testing.T) {
	k := newGitHubKit(t)
	if err := k.ic.Escalate(context.Background(), k.ticket, 0, "", []jam.Member{k.carol}); err != nil {
		t.Fatal(err)
	}
	m := k.lg.InboxSince(k.carol.User.ID, 0, 0)
	if len(m) != 1 {
		t.Fatalf("carol's inbox = %+v", m)
	}
	got := addresses(k.dir.Surfaces("github", m[0]))
	if want := "@carol-gh "; got["github:acme/board#1"] != want {
		t.Fatalf("github surfaces = %v; want acme/board#1 prefixed %q", got, want)
	}
}
