package main

import (
	"context"
	"fmt"

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
