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
	// ErrorChannel receives a best-effort notice when a turn or post fails;
	// empty disables it.
	ErrorChannel string
	// Log receives one-line notices about recovered errors; nil discards them.
	Log func(string)
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

// logf formats and forwards a one-line recovery notice to Config.Log, if set.
func (c Config) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}

// Run drives the supervisor loop until the agent returns ActionExit or ctx is
// cancelled. The loop is fail-soft: a turn, post, or poll error is logged,
// announced (best-effort) to Config.ErrorChannel, and recovered by backing
// off and re-entering with an empty inbox — it never returns for those. Only
// ActionExit and ctx cancellation end the loop. The batch for each turn is
// produced by the PREVIOUS action: the first turn seeds cursors and starts
// with an empty batch (no channel history replay); `get` polls once (empty
// ok); `wait` blocks until a poll returns something.
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
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			cfg.logf("switchboard: turn failed, recovering: %v", err)
			if cfg.ErrorChannel != "" {
				_ = d.Post(ctx, cfg.ErrorChannel, "⚠️ I hit an error and skipped a turn: "+err.Error())
			}
			if err := cfg.sleep(ctx); err != nil {
				return err
			}
			batch = nil // re-enter with an empty inbox
			continue
		}
		posted := true
		for _, m := range res.Messages {
			if err := d.Post(ctx, m.Channel, m.Content); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				// A failed delivery must NOT let the loop proceed as if the reply
				// landed (the A1 review's "don't act as delivered" property).
				// Recover by re-entering with an empty inbox rather than acting on
				// the action tied to this turn.
				cfg.logf("switchboard: post to %s failed, recovering: %v", m.Channel, err)
				if cfg.ErrorChannel != "" && cfg.ErrorChannel != m.Channel {
					_ = d.Post(ctx, cfg.ErrorChannel, "⚠️ failed to deliver a reply to "+m.Channel)
				}
				posted = false
				break
			}
		}
		if !posted {
			if err := cfg.sleep(ctx); err != nil {
				return err
			}
			batch = nil
			continue
		}
		switch res.Action {
		case ActionExit:
			return nil
		case ActionGet:
			if batch, cursors, err = pollWithRetry(ctx, cfg, d, cursors); err != nil {
				return err // only ctx cancellation reaches here
			}
		case ActionWait:
			if batch, cursors, err = waitForMessages(ctx, cfg, d, cursors); err != nil {
				return err
			}
		default:
			// Defensive: ParseTurnResult (Task 1) already rejects any action other
			// than exit/wait/get before a TurnResult reaches the loop, so this only
			// fires against a TurnResult built by hand (e.g. a test double). Treat
			// it as recoverable too, consistent with fail-soft: log and wait.
			cfg.logf("switchboard: unhandled action %q, waiting", res.Action)
			if batch, cursors, err = waitForMessages(ctx, cfg, d, cursors); err != nil {
				return err
			}
		}
	}
}

// pollWithRetry polls once, retrying with backoff on error until it succeeds
// or ctx is cancelled. Poll errors are recovered (a transient Discord outage
// must not kill a standing teammate); only ctx cancellation is returned.
func pollWithRetry(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := d.Poll(ctx, cursors)
		if err == nil {
			return batch, nc, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		cfg.logf("switchboard: poll failed, retrying: %v", err)
		if serr := cfg.sleep(ctx); serr != nil {
			return nil, nil, serr
		}
	}
}

// waitForMessages polls (with retry) until a non-empty batch arrives,
// sleeping cfg between empty polls.
func waitForMessages(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := pollWithRetry(ctx, cfg, d, cursors)
		if err != nil {
			return nil, nil, err
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
