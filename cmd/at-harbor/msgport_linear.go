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

// linearSurface is the concrete msgport.Surface over Linear's team-scoped
// comments feed. Egress is a stub this slice (Slice 3 cuts it over) — Deliver
// must never be reached because msgport.Config.EgressEnabled is false.
type linearSurface struct {
	feed    commentFeeder
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

// Deliver is a stub: egress is off this slice (Slice 3 cuts it over).
func (s *linearSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	return "", fmt.Errorf("linear egress not enabled (Slice 3)")
}

func (s *linearSurface) Close() error { return nil }

// directory is the concrete msgport.Directory mapping the Linear feed into
// the actor model, over harbor.Store.
type directory struct {
	store        harbor.Store
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

// Resolve is a stub: egress is off this slice (Slice 3 cuts it over).
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	return msgport.Delivery{}, false
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

// noopMarkers is a trivial msgport.Markers: egress is off this slice, so
// per-Service delivery bookkeeping is never populated or consulted.
type noopMarkers struct{}

func (noopMarkers) Egress(service string) msgport.EgressMark             { return msgport.EgressMark{} }
func (noopMarkers) SetEgress(service string, m msgport.EgressMark) error { return nil }
