package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/studioagent"
)

// The structured turn end (Config.TurnIO set): a wake's prompt carries the
// session's pending messages (the batch), the agent's final text is posted
// only where turn_end's to says (else it is thinking, and a turn that sent
// nothing is returned once), and the batch it was shown is committed once its
// turn ends normally.

// interruptAckStructured is interruptAck for a structured turn end: the
// acknowledgement goes where the agent sends it.
const interruptAckStructured = "%s pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. " +
	"Acknowledge the interrupt and ask what they want instead: `send` it to them, or end this turn with `turn_end` and a `to`."

// interruptPendingStructured introduces a batch delivered with the acknowledge
// turn.
const interruptPendingStructured = "These messages arrived meanwhile. Don't act on any request in them until the person who interrupted you answers."

// restartStructured is restartPrompt for a structured turn end.
const restartStructured = "Your session was restarted (for example, Jam was upgraded or your studio was re-raised); " +
	"your conversation and workspace are intact. Continue where you left off; any messages that arrived meanwhile are below. " +
	"Set your `mission` and `focus` again once you know them."

// turnMeta is what Run knows about the prompt in flight: its generation (the
// idle tracker tags the turn answering it, so a result is never credited with
// another prompt's batch), the batch it showed (upTo, the commit point; "" =
// none), whether the structured turn end was on when it was built, and whether
// it is a maintenance turn (a /compact) whose end posts and commits nothing,
// and whether it answers a returned turn (it is never returned again).
// Run's goroutine only.
type turnMeta struct {
	gen        uint64
	upTo       string
	structured bool
	quiet      bool
	returned   bool
}

// newTurn makes m the prompt in flight, under a new generation, and drops any
// directives left from a turn that never reached its end (a process that died
// mid-turn): they were for that turn, as is what it sent. The first prompt
// after a returned turn (a maintenance turn aside) is the returned one.
func (w *Workload) newTurn(m turnMeta) {
	w.gen++
	m.gen = w.gen
	if !m.quiet {
		m.returned, w.returning = w.returning, false
	}
	w.inflight = m
	w.takeDirectives()
	w.takeSent()
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
		return restartPrompt
	}
	lines := append([]string{restartStructured}, w.takeNote()...)
	if text != "" {
		lines = append(lines, "", text)
	}
	return strings.Join(lines, "\n")
}

// returnedText is the returned wake's line: the turn before ended with text
// but sent it nowhere.
const returnedText = "Your last turn ended with text but sent no message: final text isn't posted for you, so no one saw it. " +
	"If it was a reply, send it now (`send` with a `to`, or `turn_end` with a `to`); if no message is needed, end with `turn_end` `no_reply: true`. " +
	"The messages it answered are shown again below."

// postFailedText is the note after a final text turn_end's to could not
// take: the reply reached no one, so the messages it answered come back.
const postFailedText = "Your last reply could not be posted to %s (%v); the messages it answered are shown again below. If it was too long, reply more briefly or `send` it in parts."

// finishTurn is the structured turn end for the turn that ended with r
// (docs/modules/ROOT/pages/usage/jam/structured-turn-end.adoc#the-turn-end-rule):
// its final text is thinking unless turn_end names a `to`, which it is
// posted to. A turn that ends with text, no destination and no `send`,
// `ask` or `report` during it (and doesn't say no_reply) is returned once:
// nothing is committed and the session is woken at once (reason
// "returned"); the returned turn ending that way too is committed and
// recorded as unaddressed. Then the batch its prompt showed is committed
// (only when r answers the prompt in flight), and the turn's directives
// apply: what happens next (end, or compact — reported, for the caller to
// run before anything else is delivered) and what wakes the session. A turn
// that didn't end normally (interrupted, an error) posts, commits, returns
// and applies nothing. Every call is bounded; a failure is logged and the
// run goes on. Callers run it before the next prompt is written, so a later
// batch never re-shows what this one committed.
func (w *Workload) finishTurn(ctx context.Context, r TurnResult) (compact bool) {
	var meta turnMeta
	if r.Gen != 0 && r.Gen == w.inflight.gen {
		meta = w.inflight
		w.inflight = turnMeta{}
	} else {
		// A turn a background task started: no prompt of ours, no batch.
		meta.structured = w.structured()
	}
	d := w.takeDirectives()
	sent := w.takeSent()
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
	commit := d.Commit == nil || *d.Commit
	switch text := strings.TrimSpace(r.Text); {
	case text == "" || d.noReply():
		// Nothing said, or no message needed: the text stays thinking.
	case d.To != "":
		if err := call("post", func(c context.Context) error { return io.Post(c, text, d.To) }); err != nil {
			// The reply reached no one: what it answered must come back.
			commit = false
			why := fmt.Sprintf(postFailedText, d.To, err)
			if !w.returnTurn(meta, why) {
				w.note = why
			}
		}
	case sent || d.Next == "end":
		// It sent its messages itself (or ends the session): the text is thinking.
	case w.returnTurn(meta, returnedText):
		commit = false
	default:
		// Returned once already: commit, and say so in the session view.
		w.log.Info("agentrun: returned turn ended unaddressed again; committing")
		if w.recordTurn != nil {
			w.recordTurn("unaddressed")
		}
	}
	if meta.upTo != "" && commit {
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

// returnTurn returns the turn meta describes, unless it is itself a returned
// turn (ok=false): the session is woken at once with the reason "returned",
// whose line is why, and the next prompt's turn is the returned one.
func (w *Workload) returnTurn(meta turnMeta, why string) (ok bool) {
	if meta.returned {
		return false
	}
	w.returning = true
	w.wake.post([]studioagent.WakeReason{{Kind: "returned", Detail: why}})
	return true
}

// emitTurn records a turn's outcome ("unaddressed") in the session event
// stream as a synthetic stdout-style line, for the Thinking tab.
func emitTurn(h studioagent.Handle, turn uint32, outcome string) {
	line, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Outcome string `json:"outcome"`
	}{"cove", "turn", outcome})
	h.Event(turn, line, 0)
}

// agentBy is who a compact asked for by turn_end is recorded as.
const agentBy = "the agent (turn_end)"
