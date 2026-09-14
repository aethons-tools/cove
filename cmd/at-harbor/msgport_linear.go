package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
)

// commentFeeder is the narrow slice of *linear.Client that linearSurface
// polls — kept as an interface so Poll is testable without a live client.
type commentFeeder interface {
	CommentFeed(ctx context.Context, since time.Time, limit int) ([]linear.FeedComment, error)
}

// commentPoster is the narrow slice of *linear.Client that linearSurface
// delivers through: identifier→id resolution plus the comment post.
type commentPoster interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}

// linearSurface is the concrete msgport.Surface over Linear's team-scoped
// comments feed. Egress delivery goes through poster; whether it's actually
// reached is gated by msgport.Config.EgressEnabled (off until Slice 3 cuts
// egress over).
type linearSurface struct {
	feed    commentFeeder
	poster  commentPoster
	started time.Time // baseline for an empty cursor (shadow forward, not all history)
}

func (s *linearSurface) Service() string { return "linear" }

// Poll fetches comments created after `since` (or s.started when since is
// empty/unset), mapping each linear.FeedComment into a msgport.Event. next
// is the max CreatedAt seen, formatted RFC3339Nano; when no comments are
// returned, next is the incoming since unchanged.
func (s *linearSurface) Poll(ctx context.Context, project, since string) (events []msgport.Event, next string, err error) {
	from := s.started
	if since != "" {
		from, err = time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return nil, since, fmt.Errorf("linear surface: parse cursor %q: %w", since, err)
		}
	}
	cs, err := s.feed.CommentFeed(ctx, from, 100)
	if err != nil {
		return nil, since, err
	}
	next = since
	var max time.Time
	events = make([]msgport.Event, 0, len(cs))
	for _, c := range cs {
		events = append(events, msgport.Event{
			ForeignID:      c.ID,
			Surface:        c.IssueIdentifier,
			Author:         c.Author,
			Body:           c.Body,
			ReplyToForeign: c.ParentID,
			At:             c.CreatedAt,
		})
		if c.CreatedAt.After(max) {
			max = c.CreatedAt
		}
	}
	if !max.IsZero() {
		next = max.Format(time.RFC3339Nano)
	}
	return events, next, nil
}

// Deliver resolves d.Address (a ticket identifier) to an internal id and posts
// d.BodyPrefix+m.Body. Linear returns no comment id, so foreignID is ""; the
// engine's EgressMark provides exactly-once (no idempotency footer — it would
// break byte-parity with the pre-cutover direct-post path).
func (s *linearSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	if s.poster == nil {
		return "", fmt.Errorf("linear deliver: no poster configured")
	}
	issueID, err := s.poster.IssueByIdentifier(ctx, d.Address)
	if err != nil {
		return "", fmt.Errorf("linear deliver: resolve %q: %w", d.Address, err)
	}
	if err := s.poster.PostComment(ctx, issueID, d.BodyPrefix+m.Body); err != nil {
		return "", fmt.Errorf("linear deliver: post to %q: %w", d.Address, err)
	}
	return "", nil
}

func (s *linearSurface) Close() error { return nil }

// instanceRoster is the slice of harbor.Store that the Linear directory reads:
// live instances (own-ticket / sender resolution) and the project roster
// (human handles, channel refs). *harbor.FileStore satisfies it.
type instanceRoster interface {
	ListInstances() []harbor.Instance
	GetRoster(project string) (harbor.Roster, bool)
}

// directory is the concrete msgport.Directory mapping the Linear feed into
// the actor model, over harbor.Store (narrowed to instanceRoster).
type directory struct {
	store        instanceRoster
	project      string
	selfIdentity string // harbor's Linear viewer displayName (self-post filter)
}

func (d *directory) Projects(service string) []string { return []string{d.project} }

// Route drops any comment authored by harbor's own Linear identity (a
// cove's brokered outbound, echoed back on the feed), then maps the ticket
// the comment landed on to either a live cove's own ticket (Instance.Unit
// match → actor) or a configured channel (Channel.Ref match → channel).
// Unroutable events return ok=false so the cursor still advances without
// appending anything.
func (d *directory) Route(service, project string, e msgport.Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool) {
	if e.Author == d.selfIdentity {
		return msglog.Target{}, nil, "", false
	}
	from = msglog.Target{Kind: "human", Ref: e.Author}
	for _, inst := range d.store.ListInstances() {
		if inst.Unit == e.Surface {
			to = []msglog.Target{{Kind: "actor", Ref: inst.ActorID}}
			break
		}
	}
	if to == nil {
		if r, ok2 := d.store.GetRoster(project); ok2 {
			for _, ch := range r.Channels {
				if ch.Ref == e.Surface {
					to = []msglog.Target{{Kind: "channel", Ref: ch.Name}}
					break
				}
			}
		}
	}
	if to == nil {
		return msglog.Target{}, nil, "", false
	}
	if e.ReplyToForeign != "" {
		replyTo = "in:" + service + ":" + e.ReplyToForeign
	}
	return from, to, replyTo, true
}

// Resolve maps an External Log target (+ sender) to a concrete Linear
// Delivery, reproducing the pre-cutover handlePost rendering exactly. Pure:
// roster/instance lookups only, no network (Deliver does the API calls).
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	if service != "linear" {
		return msgport.Delivery{}, false
	}
	var self harbor.Instance
	var haveSelf bool
	if from.Kind == "actor" {
		for _, inst := range d.store.ListInstances() {
			if inst.ActorID == from.Ref {
				self, haveSelf = inst, true
				break
			}
		}
	}
	switch to.Kind {
	case "human":
		if !haveSelf {
			return msgport.Delivery{}, false
		}
		r, ok := d.store.GetRoster(project)
		if !ok {
			return msgport.Delivery{}, false
		}
		for _, h := range r.Humans {
			if h.Name == to.Ref {
				return msgport.Delivery{Service: "linear", Address: self.Unit, BodyPrefix: "@" + h.Handle + " "}, true
			}
		}
		return msgport.Delivery{}, false
	case "channel":
		// A roster channel is addressed by NAME → deliver to its configured
		// thread (Channel.Ref).
		if r, ok := d.store.GetRoster(project); ok {
			for _, ch := range r.Channels {
				if ch.Name == to.Ref {
					return msgport.Delivery{Service: "linear", Address: ch.Ref}, true
				}
			}
		}
		// Otherwise to.Ref is a ticket identifier itself — the cove's own ticket,
		// recorded as channel:<Unit> at send time (self-scoped there). Deliver
		// directly, with NO dependency on a live Instance: an own-ticket report
		// must still be delivered after the cove has been torn down.
		return msgport.Delivery{Service: "linear", Address: to.Ref}, true
	default:
		return msgport.Delivery{}, false
	}
}

// fileCursors is a small file-backed msgport.Cursors: a JSON
// map["service/project"]→cursor, mutex-guarded, loaded at open, saved on
// every SetIngress.
type fileCursors struct {
	path string
	mu   sync.Mutex
	m    map[string]string
}

// newFileCursors loads path (tolerating a missing or corrupt/torn file —
// either starts empty rather than failing).
func newFileCursors(path string) (*fileCursors, error) {
	c := &fileCursors{path: path, m: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return c, nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		// torn/corrupt file: tolerate, start empty.
		return c, nil
	}
	c.m = m
	return c, nil
}

func cursorKey(service, project string) string { return service + "/" + project }

func (c *fileCursors) Ingress(service, project string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[cursorKey(service, project)]
}

func (c *fileCursors) SetIngress(service, project, cursor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[cursorKey(service, project)] = cursor
	data, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0o600)
}

// fileMarkers is a file-backed msgport.Markers: a JSON map[service]EgressMark,
// mutex-guarded, loaded at open, saved on every SetEgress. Nested Pending maps
// round-trip through encoding/json.
type fileMarkers struct {
	path string
	mu   sync.Mutex
	m    map[string]msgport.EgressMark
}

// newFileMarkers loads path, tolerating a missing or corrupt/torn file (either
// starts empty rather than failing).
func newFileMarkers(path string) (*fileMarkers, error) {
	fm := &fileMarkers{path: path, m: map[string]msgport.EgressMark{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fm, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return fm, nil
	}
	var m map[string]msgport.EgressMark
	if err := json.Unmarshal(data, &m); err != nil {
		return fm, nil // torn/corrupt: tolerate, start empty
	}
	fm.m = m
	return fm, nil
}

func (fm *fileMarkers) Egress(service string) msgport.EgressMark {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.m[service]
}

func (fm *fileMarkers) SetEgress(service string, mk msgport.EgressMark) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.m[service] = mk
	data, err := json.MarshalIndent(fm.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fm.path, data, 0o600)
}

// has reports whether service has a persisted mark (the one-time seed guard).
func (fm *fileMarkers) has(service string) bool {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	_, ok := fm.m[service]
	return ok
}
