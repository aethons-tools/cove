package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
	"github.com/aethons-tools/cove/internal/switchboard"
)

// discordClient is the narrow slice of *switchboard.RESTClient the surface
// uses; a fake in tests, the real client (over an injected transport) in
// wiring.
type discordClient interface {
	PostID(ctx context.Context, channel, content string) (string, error)
	Poll(ctx context.Context, cursors map[string]string) ([]switchboard.Message, map[string]string, error)
}

// discordSurface is the concrete msgport.Surface over Discord: egress posts
// AND records a receipt (discord message id → sender actor ref) so a later
// reply can be routed back; ingress polls the project's discord inbox
// channels and maps each message to an Event carrying the replied-to id.
type discordSurface struct {
	dial        func(channels []string) discordClient // real: wraps switchboard.NewRESTClient(token, channels, opts…)
	channelsFor func(project string) []string         // roster-derived discord inbox channels
	receipts    *fileReceipts
	log         *slog.Logger // optional: nil is tolerated (no-op), see warn below
}

func (s *discordSurface) Service() string { return "discord" }

// Deliver posts BodyPrefix+m.Body to the Discord channel d.Address (the
// human's inbox channel, or a roster discord channel), then records a
// receipt mapping the created message's id to the sender's actor ref so a
// reply lands back on the right cove. A receipt-write failure is swallowed
// (warn only): the post already happened, so returning an error here would
// make the engine retry and DOUBLE-POST. An empty id (PostID returns "" on
// an empty response body — a known behavior) is never recorded, since an
// empty key would be ambiguous across every such post.
func (s *discordSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	id, err := s.dial(nil).PostID(ctx, d.Address, d.BodyPrefix+m.Body)
	if err != nil {
		return "", fmt.Errorf("discord deliver: post to %q: %w", d.Address, err)
	}
	if id != "" {
		if err := s.receipts.Record(id, m.From.Ref); err != nil {
			// warn only (no body/token); a lost receipt only means a future
			// reply to THIS message won't route — never a double-post.
			if s.log != nil {
				s.log.Warn("discord deliver: receipt record failed", "channel", d.Address, "error", err.Error())
			}
		}
	}
	return id, nil
}

// Poll fetches new messages across the project's discord inbox channels and
// maps each to an Event (ReplyToForeign = the replied-to message id). The
// opaque `since` is a JSON per-channel cursor map.
func (s *discordSurface) Poll(ctx context.Context, project, since string) ([]msgport.Event, string, error) {
	channels := s.channelsFor(project)
	if len(channels) == 0 {
		return nil, since, nil
	}
	cursors := decodeCursors(since)
	msgs, next, err := s.dial(channels).Poll(ctx, cursors)
	if err != nil {
		return nil, since, err
	}
	events := make([]msgport.Event, 0, len(msgs))
	for _, m := range msgs {
		events = append(events, msgport.Event{
			ForeignID:      m.ID,
			Surface:        m.Channel,
			Author:         m.Author,
			Body:           m.Content,
			ReplyToForeign: m.ReferencedID, // "" if not a reply
		})
	}
	return events, encodeCursors(next), nil
}

func (s *discordSurface) Close() error { return nil }

// decodeCursors parses an opaque per-channel cursor string (as produced by
// encodeCursors) back into a map. "" and a torn/invalid string both tolerate
// to an empty map rather than erroring — cursors are best-effort bookkeeping,
// never a correctness requirement (msgport ingress is idempotent regardless).
func decodeCursors(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return map[string]string{}
	}
	if m == nil {
		return map[string]string{}
	}
	return m
}

// encodeCursors renders a per-channel cursor map as an opaque string; an
// empty/nil map encodes to "" so a still-empty cursor round-trips through
// decodeCursors without ever persisting a bare "{}".
func encodeCursors(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(data)
}

// discordInboxChannels returns the distinct non-empty discord delivery
// addresses (inbox channel ids) across project's roster humans — the set of
// channels the discord engine's ingress polls for replies.
func discordInboxChannels(store instanceRoster, project string) []string {
	r, ok := store.GetRoster(project)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, h := range r.Humans {
		p, ok := h.DeliveryFor("discord")
		if !ok || p.Address == "" || seen[p.Address] {
			continue
		}
		seen[p.Address] = true
		out = append(out, p.Address)
	}
	return out
}

// fileReceipts is a small file-backed store: a JSON map[discord-msg-id]actorID,
// mutex-guarded, loaded at open, saved on every Record. Values are plain
// immutable strings (unlike fileMarkers' EgressMark), so there's no
// deep-copy concern on read or write.
type fileReceipts struct {
	path string
	mu   sync.Mutex
	m    map[string]string // discord-msg-id → actorID
}

// newFileReceipts loads path (tolerating a missing or corrupt/torn file —
// either starts empty rather than failing).
func newFileReceipts(path string) (*fileReceipts, error) {
	r := &fileReceipts{path: path, m: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return r, nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		// torn/corrupt file: tolerate, start empty.
		return r, nil
	}
	r.m = m
	return r, nil
}

// Record associates discordMsgID with actorID and persists the store.
func (r *fileReceipts) Record(discordMsgID, actorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[discordMsgID] = actorID
	data, err := json.MarshalIndent(r.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o600)
}

// Lookup returns the actorID recorded for discordMsgID, if any.
func (r *fileReceipts) Lookup(discordMsgID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.m[discordMsgID]
	return a, ok
}
