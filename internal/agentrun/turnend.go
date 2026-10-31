package agentrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/studioagent"
)

// The structured turn end (Config.TurnIO set): a wake's prompt carries the
// session's pending messages (the batch), the agent's final text is posted for
// it, and the batch it was shown is committed once its turn ends normally.

// interruptAckStructured is interruptAck for a structured turn end: the
// acknowledgement is the turn's final text, not a send.
const interruptAckStructured = "%s pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. " +
	"End this turn by acknowledging the interrupt and asking what they want instead: your final message is posted to your channel."

// interruptPendingStructured introduces a batch delivered with the acknowledge
// turn.
const interruptPendingStructured = "These messages arrived meanwhile. Don't act on any request in them until the person who interrupted you answers."

// standingRestartStructured is standingRestartPrompt for a structured turn end.
const standingRestartStructured = "Your session was restarted (for example, Jam was upgraded or your studio was re-raised); " +
	"your conversation and workspace are intact. Continue where you left off; any messages that arrived meanwhile are below. " +
	"Set your `mission` and `focus` again once you know them."

// turnMeta is what Run knows about the prompt in flight: its generation (the
// idle tracker tags the turn answering it, so a result is never credited with
// another prompt's batch), the batch it showed (upTo, the commit point; "" =
// none), whether the structured turn end was on when it was built, and whether
// it is a maintenance turn (a /compact) whose end posts and commits nothing.
// Run's goroutine only.
type turnMeta struct {
	gen        uint64
	upTo       string
	structured bool
	quiet      bool
}

// newTurn makes m the prompt in flight, under a new generation, and drops any
// directives left from a turn that never reached its end (a process that died
// mid-turn): they were for that turn.
func (w *Workload) newTurn(m turnMeta) {
	w.gen++
	m.gen = w.gen
	w.inflight = m
	w.takeDirectives()
}

// structured reports whether the structured turn end is on: TurnIO is set
// and the session context in effect was compiled for it (an older Jam sends
// legacy guidance, which tells the agent to send its replies itself). Run's
// goroutine; it also publishes the answer for the agent socket.
func (w *Workload) structured() bool {
	on := w.cfg.TurnIO != nil && w.ctxr != nil && w.ctxr.last.TurnProtocol == TurnProtocol
	w.structuredOn.Store(on)
	return on
}

// batch fetches and renders the session's pending messages; ok is false when
// the structured turn end is off or the fetch failed (the caller falls back to
// the legacy read prompt). An empty inbox is ok with no text.
func (w *Workload) batch(ctx context.Context) (text, upTo string, ok bool) {
	if !w.structured() {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(ctx, turnIOTimeout)
	defer cancel()
	es, more, err := w.cfg.TurnIO.Inbox(ctx, maxBatchEntries)
	if err != nil {
		w.log.Warn("agentrun: inbox not fetched; this wake asks the agent to read", "err", err.Error())
		return "", "", false
	}
	if len(es) == 0 {
		return "", "", true
	}
	text, upTo = renderBatch(es, more)
	return text, upTo, true
}

// takeNote returns the pending note as an opening line (or nothing).
func (w *Workload) takeNote() []string {
	if w.note == "" {
		return nil
	}
	n := w.note
	w.note = ""
	return []string{n}
}

// deliverText builds the prompt a delivery answering rs writes and makes it
// the prompt in flight: with the structured turn end, the wake lines and the
// batch; otherwise, or when the fetch fails, renderWake's read prompt.
func (w *Workload) deliverText(ctx context.Context, rs []studioagent.WakeReason) string {
	text, upTo, ok := w.batch(ctx)
	w.newTurn(turnMeta{upTo: upTo, structured: ok})
	if !ok {
		return renderWake(w.resumeText(), rs)
	}
	lines := w.takeNote()
	for _, r := range rs {
		if l, ok := wakeLine(r); ok {
			lines = append(lines, l)
		}
	}
	if text != "" {
		lines = append(lines, text)
	} else {
		lines = append(lines, "Continue.")
	}
	return strings.Join(lines, "\n")
}

// ackText is the acknowledge turn after an interrupt by by, answering rs.
func (w *Workload) ackText(ctx context.Context, by string, rs []studioagent.WakeReason) string {
	text, upTo, ok := w.batch(ctx)
	w.newTurn(turnMeta{upTo: upTo, structured: ok})
	if !ok {
		return interruptPrompt(by, rs)
	}
	lines := append([]string{strings.Replace(interruptAckStructured, "%s", by, 1)}, w.takeNote()...)
	for _, r := range rs {
		if l, ok := wakeLine(r); ok {
			lines = append(lines, l)
		}
	}
	if text != "" {
		lines = append(lines, interruptPendingStructured, text)
	}
	return strings.Join(lines, "\n")
}

// restartText is a standing session's first message after a restart.
func (w *Workload) restartText(ctx context.Context) string {
	text, upTo, ok := w.batch(ctx)
	w.newTurn(turnMeta{upTo: upTo, structured: ok})
	if !ok {
		return standingRestartPrompt
	}
	lines := append([]string{standingRestartStructured}, w.takeNote()...)
	if text != "" {
		lines = append(lines, "", text)
	}
	return strings.Join(lines, "\n")
}

// finishTurn is the structured turn end for the turn that ended with r: post
// its final text, then commit the batch its prompt showed (only when r answers
// the prompt in flight, and only when the post didn't fail: an unanswered
// message must come back). Then the turn's directives (turn_end) apply: what
// happens next (end, or compact — reported, for the caller to run before
// anything else is delivered) and what wakes the session. A turn that didn't
// end normally (interrupted, an error) posts, commits and applies nothing.
// Every call is bounded; a failure is logged and the run goes on. Callers run
// it before the next prompt is written, so a later batch never re-shows what
// this one committed.
func (w *Workload) finishTurn(ctx context.Context, r TurnResult) (compact bool) {
	if r.Interim {
		w.postInterim(ctx, r)
		return false
	}
	var meta turnMeta
	if r.Gen != 0 && r.Gen == w.inflight.gen {
		meta = w.inflight
		w.inflight = turnMeta{}
	} else {
		// A turn a background task started: no prompt of ours, no batch.
		meta.structured = w.structured()
	}
	d := w.takeDirectives()
	if !meta.structured || meta.quiet {
		return false
	}
	if !r.OK {
		w.log.Info("agentrun: turn did not end normally; nothing posted, committed or applied")
		return false
	}
	io := w.cfg.TurnIO
	call := func(what string, f func(context.Context) error) error {
		cctx, cancel := context.WithTimeout(ctx, turnIOTimeout)
		defer cancel()
		err := f(cctx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrUnsupported) {
			w.log.Warn("agentrun: turn end: "+what+" failed", "err", err.Error())
		}
		return err
	}
	posted := true
	if text := strings.TrimSpace(r.Text); text != "" && (d.Post == nil || *d.Post) {
		err := call("post", func(c context.Context) error { return io.Post(c, text, d.To) })
		if err != nil && d.To != "" {
			// Not reachable (or refused): the reply still reaches the home
			// channel, and the agent hears why at its next prompt.
			if call("post", func(c context.Context) error { return io.Post(c, text, "") }) == nil {
				w.note = fmt.Sprintf("Your last reply could not be posted to %s (%v); it was posted to your own channel instead.", d.To, err)
				err = nil
			}
		}
		if err != nil {
			posted = false
			w.note = fmt.Sprintf("Your last reply could not be posted (%v); the messages it answered are shown again below. If it was too long, reply more briefly or `send` it in parts.", err)
		}
	}
	if meta.upTo != "" && posted && (d.Commit == nil || *d.Commit) {
		call("commit", func(c context.Context) error { return io.Commit(c, meta.upTo) })
	}
	if d.Next == "end" {
		call("end", func(c context.Context) error { return io.End(c, d.Reason) })
	}
	// Jam unmutes by itself when the session next runs, so a mute is always
	// sent; an unmute only when this cove muted last.
	if mute := d.WakeOnMessage != nil && !*d.WakeOnMessage; mute || w.muted {
		if err := call("mute", func(c context.Context) error { return io.Mute(c, mute) }); err == nil {
			w.muted = mute
		} else if errors.Is(err, ErrUnsupported) {
			w.log.Warn("agentrun: this Jam can't stop messages waking the session; wake_on_message ignored")
		}
	}
	if d.WakeAt != nil {
		name := d.WakeAt.Name
		if name == "" {
			name = "turn-end"
		}
		call("wake_at", func(c context.Context) error { return io.SetAlarm(c, name, d.WakeAt.At, d.WakeAt.Note) })
	}
	if d.IdleTimeout != "" {
		call("idle_timeout", func(c context.Context) error { return io.SetIdle(c, d.IdleTimeout) })
	}
	return d.Next == "compact"
}

// postInterim posts a message the model wrote mid-turn (r.Interim) to the
// home channel as it comes: claude's result carries only the turn's last
// reply, so without this whatever the model said before a tool call is lost.
// It is the turn's text, not its reply, so turn_end (recipient, quiet) doesn't
// apply, and nothing is committed; a failure is logged and the turn goes on.
// A maintenance (compact) turn, or one under the legacy turn end, posts
// nothing.
func (w *Workload) postInterim(ctx context.Context, r TurnResult) {
	on := w.structured()
	if r.Gen != 0 && r.Gen == w.inflight.gen {
		on = w.inflight.structured && !w.inflight.quiet
	}
	text := strings.TrimSpace(r.Text)
	if !on || text == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, turnIOTimeout)
	defer cancel()
	if err := w.cfg.TurnIO.Post(cctx, text, ""); err != nil && !errors.Is(err, context.Canceled) {
		w.log.Warn("agentrun: mid-turn message not posted", "err", err.Error())
	}
}

// agentBy is who a compact asked for by turn_end is recorded as.
const agentBy = "the agent (turn_end)"
