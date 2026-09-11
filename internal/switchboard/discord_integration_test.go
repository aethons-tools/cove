//go:build integration

package switchboard

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestDiscordRoundTripLive exercises the REAL Discord REST adapter — Seed, Post,
// Poll — against a live bot + channel, the half hermetic httptest can't cover
// (real auth, and whether the bot can actually read message content, i.e. the
// Message Content intent). It seeds to "now", posts a unique marker, then polls
// and asserts the marker comes back — a genuine post→poll round-trip.
//
// Requires a bot token with access to the channel (View Channel, Send Messages,
// Read Message History, Message Content intent) and the channel id.
//
//	Run: SWITCHBOARD_IT_DISCORD_TOKEN=… SWITCHBOARD_IT_CHANNEL=… \
//	       go test -tags integration ./internal/switchboard/ -run TestDiscordRoundTripLive -v
func TestDiscordRoundTripLive(t *testing.T) {
	token := os.Getenv("SWITCHBOARD_IT_DISCORD_TOKEN")
	channel := os.Getenv("SWITCHBOARD_IT_CHANNEL")
	if token == "" || channel == "" {
		t.Skip("set SWITCHBOARD_IT_DISCORD_TOKEN and SWITCHBOARD_IT_CHANNEL to run the live Discord round-trip")
	}

	c := NewRESTClient(token, []string{channel})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed cursors at "now" so the poll below only sees messages we post next.
	cursors, err := c.Seed(ctx)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}

	marker := fmt.Sprintf("at-switchboard integration round-trip %d", time.Now().UnixNano())
	if err := c.Post(ctx, channel, marker); err != nil {
		t.Fatalf("Post: %v", err)
	}

	// Poll for the marker, retrying briefly for propagation.
	for attempt := 0; attempt < 5; attempt++ {
		msgs, next, err := c.Poll(ctx, cursors)
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		cursors = next
		for _, m := range msgs {
			if m.Content == marker {
				return // round-trip confirmed: posted content came back readable
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("ctx done before the marker returned: %v", ctx.Err())
		case <-time.After(1 * time.Second):
		}
	}
	t.Fatalf("posted marker never came back from Poll — check the bot's Read Message History + Message Content intent")
}
