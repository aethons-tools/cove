package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
)

// discordPoster is the narrow slice of *switchboard.RESTClient discordSurface
// delivers through: post content to a Discord channel (as the bot).
type discordPoster interface {
	Post(ctx context.Context, channel, content string) error
}

// discordSurface is the concrete msgport.Surface over Discord egress. Ingress
// (Poll) is an inert stub this slice — see Poll.
type discordSurface struct {
	poster discordPoster
}

func (s *discordSurface) Service() string { return "discord" }

// Deliver posts BodyPrefix+m.Body to the Discord channel d.Address (the human's
// inbox channel, or a roster discord channel). Discord's create-message returns
// a message object but the RESTClient.Post wrapper doesn't surface an id, so
// foreignID is "" (exactly-once is the EgressMark's job; no idempotency footer).
func (s *discordSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	if err := s.poster.Post(ctx, d.Address, d.BodyPrefix+m.Body); err != nil {
		return "", fmt.Errorf("discord deliver: post to %q: %w", d.Address, err)
	}
	return "", nil
}

// Poll is an inert stub this slice — Discord ingress is 5c. Returning no events
// (not an error) keeps the engine's ingress loop a harmless no-op.
func (s *discordSurface) Poll(ctx context.Context, project, since string) ([]msgport.Event, string, error) {
	return nil, since, nil
}

func (s *discordSurface) Close() error { return nil }

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
