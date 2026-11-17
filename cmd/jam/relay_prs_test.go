package main

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/tracker"
	"github.com/aethons-tools/cove/internal/tracker/trackertest"
)

// prKit is relayKit's ticket session on ACME-7 with a pull request reported
// for its unit, on a fake GitHub (repo acme/app).
type prKit struct {
	*relayKit
	gh  *trackertest.GitHub
	log jam.Channel
	w   *prWatcher
}

const prURL = "https://github.com/acme/app/pull/5"

func newPRKit(t *testing.T) *prKit {
	t.Helper()
	k := &prKit{relayKit: newRelayKit(t), gh: trackertest.NewGitHub(t, "acme/app", "jam-bot")}
	var err error
	if k.log, err = k.ticketChannel(); err != nil {
		t.Fatal(err)
	}
	if err := k.st.ReportUnit(k.log.ID, jam.ReportInReview, prURL, time.Now()); err != nil {
		t.Fatal(err)
	}
	k.gh.AddPull(trackertest.GHPull{Number: 5, Title: "Fix the thing"})
	k.w = k.watcher()
	return k
}

// watcher is a new PR watcher over the kit's store: a fresh one is a restart.
func (k *prKit) watcher() *prWatcher {
	k.t.Helper()
	gh, err := githubissues.NewReader(trackertest.Token, k.gh.HTTPClient())
	if err != nil {
		k.t.Fatal(err)
	}
	return &prWatcher{store: k.st, dir: k.dir, ic: k.ic, gh: gh}
}

// entries is the work log's PR entries, oldest first.
func (k *prKit) entries() []intercom.Squawk {
	var out []intercom.Squawk
	for _, m := range k.lg.ChannelBefore(k.log.ID, math.MaxInt64, 100) {
		if strings.HasPrefix(m.ID, "pr:") {
			out = append(out, m)
		}
	}
	return out
}

func (k *prKit) tick() {
	k.t.Helper()
	if err := k.w.Tick(context.Background()); err != nil {
		k.t.Fatalf("Tick: %v", err)
	}
}

func (k *prKit) pr() jam.UnitPR {
	u, _ := k.st.WorkUnit(k.log.ID)
	return u.PRs[0]
}

func TestPRWatcherPostsReviewsAndMergeOnce(t *testing.T) {
	k := newPRKit(t)
	ann, bot := trackertest.GHUser{Login: "ann", ID: 7}, trackertest.GHUser{Login: "review-bot[bot]", ID: 9, Bot: true}
	k.gh.AddReview(trackertest.GHReview{Pull: 5, State: "COMMENTED", User: bot})
	k.gh.AddReview(trackertest.GHReview{Pull: 5, State: "CHANGES_REQUESTED", User: ann})
	k.gh.AddReview(trackertest.GHReview{Pull: 5, State: "PENDING", User: ann})
	k.tick()

	es := k.entries()
	if len(es) != 2 {
		t.Fatalf("entries = %+v, want the two submitted reviews", es)
	}
	for i, want := range []string{"commented on pull request [#5: Fix the thing](https://github.com/acme/app/pull/5#pullrequestreview-", "requested changes on pull request [#5: Fix the thing](https://github.com/acme/app/pull/5#pullrequestreview-"} {
		if !strings.HasPrefix(es[i].Body, want) {
			t.Fatalf("entry %d = %q, want it to start %q", i, es[i].Body, want)
		}
		if isSession(es[i].From) || jam.IsNotice(es[i].ID) {
			t.Fatalf("entry %d is from %s (%s): want the reviewer's account, which wakes the worker", i, es[i].From, es[i].ID)
		}
	}
	if got := k.ic.PartyOf(es[1].From).Label; got != "ann" {
		t.Fatalf("changes requested by %q, want ann", got)
	}
	// A Jam note: mirrored to the issue as the reviewer.
	wantSurfaces(t, "review entry", k.dir.Surfaces("linear", es[1]), map[string]string{"linear:ACME-7": "ann: "})
	if p := k.pr(); p.Title != "Fix the thing" || p.State != jam.PROpen {
		t.Fatalf("PR = %+v, want its title recorded", p)
	}

	// A re-poll, and a restart, post nothing new.
	k.tick()
	k.w = k.watcher()
	k.tick()
	if n := len(k.entries()); n != 2 {
		t.Fatalf("after a re-poll and a restart: %d entries, want 2", n)
	}

	k.gh.AddReview(trackertest.GHReview{Pull: 5, State: "APPROVED", User: bot})
	k.gh.AddPull(trackertest.GHPull{Number: 5, Title: "Fix the thing", Merged: true, MergedBy: ann, ClosedBy: ann})
	k.tick()
	es = k.entries()
	if len(es) != 4 || !strings.HasPrefix(es[2].Body, "approved pull request [#5") || es[3].Body != "merged pull request [#5: Fix the thing](https://github.com/acme/app/pull/5)" {
		t.Fatalf("entries = %+v, want the approval then the merge", es)
	}
	if got := k.ic.PartyOf(es[3].From).Label; got != "ann" {
		t.Fatalf("merged by %q, want ann", got)
	}
	if p := k.pr(); p.State != jam.PRMerged {
		t.Fatalf("PR = %+v, want merged", p)
	}

	// A crash after posting but before recording: the store still says open.
	// The log already holds each entry: no repeat.
	if err := k.st.SetUnitPR(k.log.ID, prURL, jam.PRStatus{Title: "Fix the thing", State: jam.PROpen}); err != nil {
		t.Fatal(err)
	}
	k.w = k.watcher()
	k.tick()
	if n := len(k.entries()); n != 4 {
		t.Fatalf("after a crash before recording: %d entries, want 4", n)
	}
	if p := k.pr(); p.State != jam.PRMerged {
		t.Fatalf("PR = %+v, want it recorded again", p)
	}
}

func TestPRWatcherClosed(t *testing.T) {
	k := newPRKit(t)
	k.gh.AddPull(trackertest.GHPull{Number: 5, Title: "Fix [the] thing", Closed: true, ClosedBy: trackertest.GHUser{Login: "bob-gh", ID: 8}})
	k.tick()
	es := k.entries()
	if len(es) != 1 || es[0].Body != `closed pull request [#5: Fix \[the\] thing](https://github.com/acme/app/pull/5) without merging` {
		t.Fatalf("entries = %+v, want the close", es)
	}
	if p := k.pr(); p.State != jam.PRClosed {
		t.Fatalf("PR = %+v, want closed", p)
	}
}

// A PR the token can't read, or that isn't on GitHub, is skipped: the
// others are still watched.
func TestPRWatcherSkipsUnreadable(t *testing.T) {
	k := newPRKit(t)
	for _, u := range []string{"https://github.com/other/repo/pull/1", "https://gitlab.com/acme/app/-/merge_requests/2"} {
		if err := k.st.ReportUnit(k.log.ID, jam.ReportInReview, u, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	k.gh.AddReview(trackertest.GHReview{Pull: 5, State: "APPROVED", User: trackertest.GHUser{Login: "ann", ID: 7}})
	k.tick()
	if es := k.entries(); len(es) != 1 {
		t.Fatalf("entries = %+v, want PR 5's approval", es)
	}
}

func TestPRReaderChoice(t *testing.T) {
	gh, err := githubissues.NewReader("t", nil)
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	open := func(conn string) (pullReader, error) { opened = conn; return gh, nil }
	ghTracker := trackertest.NewGitHub(t, "acme/board", "jam-bot").Client(t)
	if r, ok, err := prReader(&requisitionerConfig{}, ghTracker, open); err != nil || !ok || r != pullReader(ghTracker) || opened != "" {
		t.Fatalf("github tracker: %v %v %v; want its own client", r, ok, err)
	}
	if _, ok, err := prReader(&requisitionerConfig{}, &fakeLinearTracker{}, open); err != nil || ok {
		t.Fatalf("linear tracker without pull-requests: ok=%v %v; want no watcher", ok, err)
	}
	if r, ok, err := prReader(&requisitionerConfig{PullRequests: "gh"}, &fakeLinearTracker{}, open); err != nil || !ok || r != pullReader(gh) || opened != "gh" {
		t.Fatalf("linear tracker with pull-requests: %v %v %v, opened %q", r, ok, err, opened)
	}
}

// fakeLinearTracker is a tracker that reads no pull requests.
type fakeLinearTracker struct{ tracker.Tracker }

// A review keeps the id it got when it was started: one left pending while a
// later review is posted, then submitted, is still posted (review-bot on #745).
func TestPRWatcherPostsAReviewSubmittedLate(t *testing.T) {
	k := newPRKit(t)
	ann, bot := trackertest.GHUser{Login: "ann", ID: 7}, trackertest.GHUser{Login: "review-bot[bot]", ID: 9, Bot: true}
	k.gh.AddReview(trackertest.GHReview{ID: 101, Pull: 5, State: "APPROVED", User: bot})
	k.tick()
	k.gh.AddReview(trackertest.GHReview{ID: 100, Pull: 5, State: "CHANGES_REQUESTED", User: ann})
	k.tick()
	k.w = k.watcher() // and across a restart, still once
	k.tick()
	es := k.entries()
	if len(es) != 2 || es[0].ID != "pr:acme/app#5:review:101" || es[1].ID != "pr:acme/app#5:review:100" || !strings.HasPrefix(es[1].Body, "requested changes") {
		t.Fatalf("entries = %+v, want 101's approval then 100's request for changes", es)
	}
}
