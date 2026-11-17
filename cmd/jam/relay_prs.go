package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/tracker"
)

// pullReader is the slice of the GitHub client the PR watcher reads with.
type pullReader interface {
	PullRequest(ctx context.Context, repo string, n int) (githubissues.PullRequest, error)
	PullReviews(ctx context.Context, repo string, n int) ([]githubissues.Review, error)
}

// prWatcher is the GitHub relay's pull request side
// (docs/modules/ROOT/pages/usage/jam/work-units.adoc#pr-watcher): each poll it
// reads every open pull request the units of work record, and posts one
// entry to the unit's work log for each new review and for a merge or a
// close, as the GitHub account that did it. The entry's id names the event,
// so the log refuses it a second time. Every submitted review is offered
// each poll, not those past a mark: a review keeps the id it got when it was
// started, so one left pending can be submitted after a later one. posted
// spares the log the repeats within a serve; after a restart the log's
// refusal is what keeps each once. The store keeps the PR's title and state.
type prWatcher struct {
	store jam.Store
	dir   *directory // who a GitHub account is in a log (directory.author)
	ic    *jam.Intercom
	gh    pullReader
	poll  time.Duration // 0: a minute
	log   *slog.Logger  // nil: no logging

	mu        sync.Mutex
	notBefore time.Time                  // a rate limit's end
	skipped   map[string]bool            // PR URLs already warned unreadable
	posted    map[string]map[string]bool // open PR URL → entry ids in its log
}

// Run polls until ctx ends.
func (w *prWatcher) Run(ctx context.Context) {
	poll := w.poll
	if poll <= 0 {
		poll = time.Minute
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil && w.log != nil {
			w.log.Warn("relay (pull requests): poll failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick reads every open pull request of every live work log once. A PR it
// can't read (another host, a repo the token can't see) is skipped, warned
// once; a rate limit ends the tick and holds the next ones until it passes.
func (w *prWatcher) Tick(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Now().Before(w.notBefore) {
		return nil
	}
	var errs []error
	for _, u := range w.store.ListWorkUnits() {
		ch, ok := w.store.GetChannel(u.Log)
		if !ok || ch.Status != jam.StatusLive {
			continue
		}
		for _, p := range u.PRs {
			if p.State != jam.PROpen {
				continue
			}
			err := w.watch(ctx, ch, p)
			var rl *tracker.RateLimitError
			switch {
			case err == nil:
			case errors.As(err, &rl):
				w.notBefore = time.Now().Add(rl.RetryAfter)
				return err
			case errors.Is(err, githubissues.ErrNoPullRequest), errors.Is(err, errNotGitHubPR):
				w.skip(p.URL, err)
			default:
				errs = append(errs, fmt.Errorf("%s: %w", p.URL, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (w *prWatcher) skip(url string, err error) {
	if w.skipped == nil {
		w.skipped = map[string]bool{}
	}
	if !w.skipped[url] && w.log != nil {
		w.log.Warn("relay (pull requests): not watching a pull request", "pr", url, "err", err.Error())
	}
	w.skipped[url] = true
}

var errNotGitHubPR = errors.New("not a github.com pull request URL")

// githubPR is the repo ("owner/name") and number of a github.com pull
// request URL.
func githubPR(raw string) (repo string, n int, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host != "github.com" {
		return "", 0, errNotGitHubPR
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) < 4 || seg[2] != "pull" {
		return "", 0, errNotGitHubPR
	}
	if n, err = strconv.Atoi(seg[3]); err != nil || n < 1 {
		return "", 0, errNotGitHubPR
	}
	return seg[0] + "/" + seg[1], n, nil
}

// watch posts p's new reviews, then its merge or close, recording each.
func (w *prWatcher) watch(ctx context.Context, ch jam.Channel, p jam.UnitPR) error {
	repo, n, err := githubPR(p.URL)
	if err != nil {
		return err
	}
	pr, err := w.gh.PullRequest(ctx, repo, n)
	if err != nil {
		return err
	}
	reviews, err := w.gh.PullReviews(ctx, repo, n)
	if err != nil {
		return err
	}
	st := jam.PRStatus{Title: pr.Title, State: jam.PROpen}
	what := fmt.Sprintf("pull request [#%d: %s]", n, linkText.Replace(pr.Title))
	key := fmt.Sprintf("pr:%s#%d:", repo, n)
	if w.posted == nil {
		w.posted = map[string]map[string]bool{}
	}
	posted := w.posted[p.URL]
	if posted == nil {
		posted = map[string]bool{}
		w.posted[p.URL] = posted
	}
	for _, r := range reviews {
		id := key + "review:" + strconv.FormatInt(r.ID, 10)
		if posted[id] {
			continue
		}
		if err := w.post(ch, id, r.User, reviewed(r.State)+" "+what+"("+r.HTMLURL+")"); err != nil {
			return err
		}
		posted[id] = true
	}
	var body string
	switch pr.State {
	case githubissues.PullMerged:
		st.State, body = jam.PRMerged, "merged "+what+"("+pr.HTMLURL+")"
	case githubissues.PullClosed:
		st.State, body = jam.PRClosed, "closed "+what+"("+pr.HTMLURL+") without merging"
	}
	if body != "" {
		if err := w.post(ch, key+string(st.State), pr.By, body); err != nil {
			return err
		}
	}
	if st.State != jam.PROpen {
		delete(w.posted, p.URL) // no longer read
	}
	return w.store.SetUnitPR(ch.ID, p.URL, st)
}

// linkText escapes a title for a markdown link's text.
var linkText = strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`)

// reviewed is what a review in state did.
func reviewed(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "requested changes on"
	case "DISMISSED":
		return "reviewed (review since dismissed)"
	default:
		return "commented on"
	}
}

// ghost is GitHub's own stand-in for an account it doesn't name.
var ghost = githubissues.User{Login: "ghost", ID: 10137}

// post appends a Jam note with id to the work log, as by's account (or the
// person it is linked to); the log refusing a duplicate id means it is
// already there.
func (w *prWatcher) post(ch jam.Channel, id string, by githubissues.User, body string) error {
	if by.ID == 0 {
		by = ghost
	}
	from, ok := w.dir.author(tracker.KindGitHub, ch, "", strconv.FormatInt(by.ID, 10), by.Login)
	if !ok {
		return fmt.Errorf("record the author %s", by.Login)
	}
	_, err := w.ic.PostTrusted(ch, intercom.Squawk{ID: id, From: from, Body: body, ContentType: intercom.ContentMarkdown})
	if errors.Is(err, intercom.ErrDuplicateID) {
		return nil
	}
	return err
}

// prReader is the PR watcher's GitHub client: runtime.requisitioner's
// pull-requests connection's (open) when set, else a github tracker's own;
// ok=false: none (a Linear tracker without pull-requests).
func prReader(dc *requisitionerConfig, trk tracker.Tracker, open func(conn string) (pullReader, error)) (pullReader, bool, error) {
	if dc.PullRequests != "" {
		r, err := open(dc.PullRequests)
		return r, err == nil, err
	}
	r, ok := trk.(pullReader)
	return r, ok, nil
}
