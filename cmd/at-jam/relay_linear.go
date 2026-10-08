package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/relay"
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

// linearSurface is the concrete relay.Surface over Linear's team-scoped
// comments feed. Egress delivery goes through poster; whether it's actually
// reached is gated by relay.Config.EgressEnabled (off until Slice 3 cuts
// egress over).
type linearSurface struct {
	feed    commentFeeder
	poster  commentPoster
	started time.Time // baseline for an empty cursor (shadow forward, not all history)
}

func (s *linearSurface) Service() string { return "linear" }

// Poll fetches comments created after `since` (or s.started when since is
// empty/unset), mapping each linear.FeedComment into a relay.Event. next
// is the max CreatedAt seen, formatted RFC3339Nano; when no comments are
// returned, next is the incoming since unchanged.
func (s *linearSurface) Poll(ctx context.Context, project, since string) (events []relay.Event, next string, err error) {
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
	events = make([]relay.Event, 0, len(cs))
	for _, c := range cs {
		events = append(events, relay.Event{
			ForeignID:      c.ID,
			Surface:        c.IssueIdentifier,
			Author:         c.Author,
			AuthorID:       c.AuthorID,
			Body:           c.Body,
			ReplyToForeign: c.ParentID,
			At:             c.CreatedAt,
			ContentType:    intercom.ContentMarkdown, // Linear comment bodies are markdown
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

// deliveredBody is m's body as posted to a markdown-rendering surface of
// flavor f: markdown (the default) passes through byte-for-byte; text/plain is
// escaped so the surface shows it literally.
func deliveredBody(m intercom.Squawk, f intercom.Flavor) string {
	if m.ContentType == intercom.ContentPlain {
		return intercom.EscapeMarkdown(m.Body, f)
	}
	return m.Body
}

// Deliver resolves d.Address (a ticket identifier) to an internal id and posts
// d.BodyPrefix+m.Body. Linear returns no comment id, so foreignID is ""; the
// engine's EgressMark provides exactly-once (no idempotency footer — it would
// break byte-parity with the pre-cutover direct-post path).
func (s *linearSurface) Deliver(ctx context.Context, d relay.Delivery, m intercom.Squawk) (string, error) {
	if s.poster == nil {
		return "", fmt.Errorf("linear deliver: no poster configured")
	}
	issueID, err := s.poster.IssueByIdentifier(ctx, d.Address)
	if err != nil {
		return "", fmt.Errorf("linear deliver: resolve %q: %w", d.Address, err)
	}
	if err := s.poster.PostComment(ctx, issueID, d.BodyPrefix+deliveredBody(m, intercom.FlavorCommonMark)); err != nil {
		return "", fmt.Errorf("linear deliver: post to %q: %w", d.Address, err)
	}
	return "", nil
}

func (s *linearSurface) Close() error { return nil }

// fileCursors is a small file-backed relay.Cursors: a JSON
// map["service/<project id>"]→cursor, mutex-guarded, loaded at open, saved on
// every SetIngress. key maps a project reference to the id it is stored
// under (nil: as given).
type fileCursors struct {
	path string
	mu   sync.Mutex
	m    map[string]string
	key  func(string) string
}

// keyedBy sets how projects are keyed, and re-keys entries an older Jam
// stored by project name (once, keeping the old file as <path>.bak).
func (c *fileCursors) keyedBy(key func(string) string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key = key
	rekeyed := map[string]string{}
	changed := false
	for k, v := range c.m { // keys already by id first: they win over a re-keyed name
		if _, project, ok := strings.Cut(k, "/"); !ok || key(project) == project {
			rekeyed[k] = v
		}
	}
	for k, v := range c.m {
		service, project, ok := strings.Cut(k, "/")
		if !ok || key(project) == project {
			continue
		}
		changed = true
		if nk := cursorKey(service, key(project)); rekeyed[nk] == "" {
			rekeyed[nk] = v
		}
	}
	if !changed {
		return nil
	}
	if _, err := os.Stat(c.path + ".bak"); os.IsNotExist(err) { // keep the first
		if old, err := os.ReadFile(c.path); err == nil {
			if err := writeFileAtomic(c.path+".bak", old); err != nil {
				return err
			}
		}
	}
	c.m = rekeyed
	return c.save()
}

// writeFileAtomic writes data to path by a temp file renamed over it, so a
// crash never leaves a torn file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *fileCursors) projectKey(project string) string {
	if c.key == nil {
		return project
	}
	return c.key(project)
}

// save writes the map (atomically). Caller holds mu.
func (c *fileCursors) save() error {
	data, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.path, data)
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
		// torn/corrupt file: fall back to the re-key's backup, else start empty.
		if bak, berr := os.ReadFile(path + ".bak"); berr == nil && json.Unmarshal(bak, &m) == nil {
			c.m = m
		}
		return c, nil
	}
	c.m = m
	return c, nil
}

func cursorKey(service, project string) string { return service + "/" + project }

func (c *fileCursors) Ingress(service, project string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[cursorKey(service, c.projectKey(project))]
}

func (c *fileCursors) SetIngress(service, project, cursor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[cursorKey(service, c.projectKey(project))] = cursor
	return c.save()
}

// fileMarkers is a file-backed relay.Markers: a JSON map[service]EgressMark,
// mutex-guarded, loaded at open, saved on every SetEgress. Nested Pending maps
// round-trip through encoding/json.
type fileMarkers struct {
	path string
	mu   sync.Mutex
	m    map[string]relay.EgressMark
}

// newFileMarkers loads path, tolerating a missing or corrupt/torn file (either
// starts empty rather than failing).
func newFileMarkers(path string) (*fileMarkers, error) {
	fm := &fileMarkers{path: path, m: map[string]relay.EgressMark{}}
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
	var m map[string]relay.EgressMark
	if err := json.Unmarshal(data, &m); err != nil {
		return fm, nil // torn/corrupt: tolerate, start empty
	}
	fm.m = m
	return fm, nil
}

// copyEgressMark deep-copies an EgressMark's nested Pending maps so a caller can
// mutate the returned mark without touching fileMarkers' stored state, and so a
// stored mark can't be mutated by the caller after SetEgress. This is load-
// bearing: Jam runs two relay engines (linear + discord) sharing one
// *fileMarkers, and one engine's SetEgress marshals the whole map while the
// other engine mutates its own Pending — without this copy that's a concurrent
// map iteration/write (fatal).
func copyEgressMark(mk relay.EgressMark) relay.EgressMark {
	if mk.Pending == nil {
		return mk
	}
	p := make(map[string]map[string]bool, len(mk.Pending))
	for id, targets := range mk.Pending {
		tc := make(map[string]bool, len(targets))
		for k, v := range targets {
			tc[k] = v
		}
		p[id] = tc
	}
	mk.Pending = p
	return mk
}

func (fm *fileMarkers) Egress(service string) relay.EgressMark {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return copyEgressMark(fm.m[service])
}

func (fm *fileMarkers) SetEgress(service string, mk relay.EgressMark) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.m[service] = copyEgressMark(mk)
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

// needsSeed reports whether service's egress low-water must be (re-)seeded to
// the current Log tail: either no marker is persisted yet, or one is but its
// LastSeq is zero. The zero case covers the COV-184 upgrade: a pre-COV-184
// marker persisted the low-water as LastMsg (a string id); that field no
// longer exists on EgressMark, so an old marker file unmarshals into a
// present-but-LastSeq==0 entry. Without this check, `has(service)` alone
// would be true and the seed would be skipped, so egress would resume
// ListSince(0) — re-delivering the entire backlog to the Service.
func (fm *fileMarkers) needsSeed(service string) bool {
	return !fm.has(service) || fm.Egress(service).LastSeq == 0
}

// settleCutover moves service's egress mark onto the channel log: legacy
// squawks it had not yet delivered at the cutover (above LastSeq, below
// cutover) are not delivered — the relays render only the channel log — and
// in-flight bookkeeping for legacy ids is dropped. It reports how many legacy
// seqs it skipped (0: the mark was already settled).
func (fm *fileMarkers) settleCutover(service string, cutover int64, seqOf func(id string) (int64, bool)) (int64, error) {
	if !fm.has(service) {
		return 0, nil // unseeded: seeded to the tail elsewhere
	}
	mk := fm.Egress(service)
	var skipped int64
	if mk.LastSeq < cutover-1 {
		skipped, mk.LastSeq = cutover-1-mk.LastSeq, cutover-1
	}
	changed := skipped > 0
	for id := range mk.Pending {
		if seq, ok := seqOf(id); !ok || seq < cutover {
			delete(mk.Pending, id)
			changed = true
		}
	}
	if !changed {
		return 0, nil
	}
	return skipped, fm.SetEgress(service, mk)
}
