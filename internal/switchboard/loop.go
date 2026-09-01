package switchboard

import (
	"context"
	"fmt"
	"time"
)

// Discord is the inbound/outbound Discord transport (REST adapter or a fake).
type Discord interface {
	// Poll returns new messages across the watched channels since the per-channel
	// cursors, plus the advanced cursors (last message id seen per channel).
	Poll(ctx context.Context, cursors map[string]string) (msgs []Message, newCursors map[string]string, err error)
	// Post sends content to a channel.
	Post(ctx context.Context, channel, content string) error
	// Seed positions per-channel cursors at the newest existing message WITHOUT
	// returning those messages, so a fresh conductor doesn't replay channel history
	// on its first turn.
	Seed(ctx context.Context) (cursors map[string]string, err error)
}

// Agent runs exactly one turn: given the rendered inbox, returns the agent's result.
type Agent interface {
	RunTurn(ctx context.Context, input string) (TurnResult, error)
}

// Config tunes the loop. Sleep defaults to a ctx-aware sleep when nil; tests stub it.
type Config struct {
	PollInterval time.Duration
	Sleep        func(context.Context, time.Duration) error
}

func (c Config) sleep(ctx context.Context) error {
	d := c.PollInterval
	if d <= 0 {
		d = 2 * time.Second
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run drives the supervisor loop until the agent returns ActionExit, ctx is
// cancelled, or an adapter errors. The batch for each turn is produced by the
// PREVIOUS action: the first turn seeds cursors and starts with an empty
// batch (no channel history replay); `get` polls once (empty ok); `wait`
// blocks until a poll returns something.
func Run(ctx context.Context, cfg Config, d Discord, a Agent) error {
	// Cold start: seed cursors at "now" so the first turn sees an empty inbox
	// rather than replaying up to 100 lines of channel history as "new".
	cursors, err := d.Seed(ctx)
	if err != nil {
		return fmt.Errorf("switchboard: seed: %w", err)
	}
	var batch []Message
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := a.RunTurn(ctx, RenderInbox(batch))
		if err != nil {
			return fmt.Errorf("switchboard: run turn: %w", err)
		}
		for _, m := range res.Messages {
			if err := d.Post(ctx, m.Channel, m.Content); err != nil {
				return fmt.Errorf("switchboard: post to %s: %w", m.Channel, err)
			}
		}
		switch res.Action {
		case ActionExit:
			return nil
		case ActionGet:
			if batch, cursors, err = d.Poll(ctx, cursors); err != nil {
				return fmt.Errorf("switchboard: get poll: %w", err)
			}
		case ActionWait:
			if batch, cursors, err = waitForMessages(ctx, cfg, d, cursors); err != nil {
				return err
			}
		default:
			// Defensive: ParseTurnResult (Task 1) already rejects any action other
			// than exit/wait/get before a TurnResult reaches the loop, so this only
			// fires against a TurnResult built by hand (e.g. a test double).
			return fmt.Errorf("switchboard: unhandled action %q", res.Action)
		}
	}
}

// waitForMessages polls until a non-empty batch arrives, sleeping cfg between
// empty polls.
func waitForMessages(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := d.Poll(ctx, cursors)
		if err != nil {
			return nil, nil, fmt.Errorf("switchboard: wait poll: %w", err)
		}
		cursors = nc
		if len(batch) > 0 {
			return batch, cursors, nil
		}
		if err := cfg.sleep(ctx); err != nil {
			return nil, nil, err
		}
	}
}
