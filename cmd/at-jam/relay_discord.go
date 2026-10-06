package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/relay"
	"github.com/aethons-tools/cove/internal/switchboard"
)

// discordClient is the narrow slice of *switchboard.RESTClient the surface
// uses; a fake in tests, the real client (over an injected transport) in
// wiring.
type discordClient interface {
	PostID(ctx context.Context, channel, content string) (string, error)
	Poll(ctx context.Context, cursors map[string]string) ([]switchboard.Message, map[string]string, error)
}

// discordSurface is the concrete relay.Surface over Discord: egress posts
// AND records a receipt (discord message id → sender actor ref + squawk id) so a later
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
// receipt mapping the created message's id to the sender's actor ref and the
// squawk's id, so a reply lands back on the right cove as a reply to that
// squawk. A receipt-write failure is swallowed
// (warn only): the post already happened, so returning an error here would
// make the engine retry and DOUBLE-POST. An empty id (PostID returns "" on
// an empty response body — a known behavior) is never recorded, since an
// empty key would be ambiguous across every such post.
func (s *discordSurface) Deliver(ctx context.Context, d relay.Delivery, m intercom.Squawk) (string, error) {
	id, err := s.dial(nil).PostID(ctx, d.Address, d.BodyPrefix+deliveredBody(m, intercom.FlavorDiscord))
	if err != nil {
		return "", fmt.Errorf("discord deliver: post to %q: %w", d.Address, err)
	}
	if id != "" {
		if err := s.receipts.Record(id, string(m.From), m.ID, string(m.Channel)); err != nil {
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
func (s *discordSurface) Poll(ctx context.Context, project, since string) ([]relay.Event, string, error) {
	channels := s.channelsFor(project)
	if len(channels) == 0 {
		return nil, since, nil
	}
	cursors := decodeCursors(since)
	msgs, next, err := s.dial(channels).Poll(ctx, cursors)
	if err != nil {
		return nil, since, err
	}
	events := make([]relay.Event, 0, len(msgs))
	for _, m := range msgs {
		events = append(events, relay.Event{
			ForeignID:      m.ID,
			Surface:        m.Channel,
			Author:         m.Author,
			AuthorID:       m.AuthorID,
			AuthorBot:      m.AuthorBot,
			Body:           m.Content,
			ReplyToForeign: m.ReferencedID,           // "" if not a reply
			ContentType:    intercom.ContentMarkdown, // Discord message content is (Discord-flavored) markdown
		})
	}
	return events, encodeCursors(next), nil
}

func (s *discordSurface) Close() error { return nil }

// decodeCursors parses an opaque per-channel cursor string (as produced by
// encodeCursors) back into a map. "" and a torn/invalid string both tolerate
// to an empty map rather than erroring — cursors are best-effort bookkeeping,
// never a correctness requirement (relay ingress is idempotent regardless).
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

// discordPolledChannels returns the distinct Discord channels the discord
// relay polls for a project: each member's inbox and each room's channel on
// a discord connection. Without the rooms, a reply in a room's channel would
// never be seen.
func discordPolledChannels(store jam.Store, project string) []string {
	p, ok := store.GetProject(project)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, m := range jam.MembersOf(store, p.ID) {
		if inbox, ok := m.Inbox("discord"); ok {
			add(inbox)
		}
	}
	for _, ch := range store.ListChannels(p.ID, jam.SourceRoom) {
		for _, b := range ch.Bindings {
			if c, ok := store.GetConnection(b.ConnectionID); ok && c.Kind == "discord" {
				add(b.Ref)
			}
		}
	}
	return out
}

// receipt is what Jam remembers about one message it posted to Discord: the
// channel it was in (a reply joins that conversation), the posted squawk's id
// (the reply's ReplyTo) and its author. A receipt from before the channel log
// has no Channel (and maybe no Message): its reply goes to the author
// session's default channel while that session lives.
type receipt struct {
	Actor   string `json:"actor"`
	Message string `json:"message,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// UnmarshalJSON accepts both the current object form and the legacy bare
// actor-id string (the old map[discord-msg-id]actorID file format).
func (r *receipt) UnmarshalJSON(data []byte) error {
	var actor string
	if err := json.Unmarshal(data, &actor); err == nil {
		*r = receipt{Actor: actor}
		return nil
	}
	type plain receipt
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = receipt(p)
	return nil
}

// fileReceipts is a small file-backed store: a JSON
// map[discord-msg-id]receipt, mutex-guarded, loaded at open, saved on every
// Record. Values are plain comparable structs (unlike fileMarkers'
// EgressMark), so there's no deep-copy concern on read or write.
type fileReceipts struct {
	path string
	mu   sync.Mutex
	m    map[string]receipt // discord-msg-id → receipt
}

// newFileReceipts loads path (tolerating a missing or corrupt/torn file —
// either starts empty rather than failing). A file in the legacy
// map[discord-msg-id]actorID format loads as receipts with no message id.
func newFileReceipts(path string) (*fileReceipts, error) {
	r := &fileReceipts{path: path, m: map[string]receipt{}}
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
	var m map[string]receipt
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		// torn/corrupt file: tolerate, start empty.
		return r, nil
	}
	r.m = m
	return r, nil
}

// Record associates discordMsgID with the posted squawk (its author, id and
// channel), and persists the store.
func (r *fileReceipts) Record(discordMsgID, author, messageID, channel string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[discordMsgID] = receipt{Actor: author, Message: messageID, Channel: channel}
	data, err := json.MarshalIndent(r.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o600)
}

// Lookup returns the receipt recorded for discordMsgID, if any.
func (r *fileReceipts) Lookup(discordMsgID string) (receipt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.m[discordMsgID]
	return a, ok
}
