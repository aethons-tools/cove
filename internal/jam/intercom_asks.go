package jam

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Structured asks (docs/superpowers/specs/2026-10-21-intercom-asks-design.md):
// a session posts a one-of-N question into a channel; a person in it answers.
// The log folds every event (intercom.FoldAsk); this file is the policy —
// where a session may ask, who may answer — and keeps the asker's open-asks
// count current (Needs you, escalation).

// AskInput is a session's ask: its question, options, and who may answer
// ("user:<name|id>"; none = anyone in the channel).
type AskInput struct {
	Question string
	Options  []intercom.AskOption
	Of       []string
}

// SetOnAsksChanged sets the hook told which session's asks changed (serve:
// Supervisor.SetOpenAsks with OpenAskCount, recounted under its lock so
// concurrent changes never leave a stale count).
func (ic *Intercom) SetOnAsksChanged(f func(session ident.ID)) { ic.onAsks = f }

// OpenAskCount is session's open asks in live channels.
func (ic *Intercom) OpenAskCount(session ident.ID) int {
	n := 0
	for _, a := range ic.AsksOf(session, intercom.AskOpen) {
		if ch, ok := ic.store.GetChannel(a.Channel); ok && ch.Status == StatusLive {
			n++
		}
	}
	return n
}

// Ask posts p's ask to `to` ("" = its home channel), planned like a send:
// ErrSendDenied, ErrSendUnresolved and ErrRemoved as for Plan; intercom
// ErrInvalid for a malformed ask.
func (ic *Intercom) Ask(p Poster, to string, in AskInput, now time.Time) (intercom.Ask, Channel, error) {
	pl, err := ic.Plan(p, to, now)
	if err != nil {
		return intercom.Ask{}, Channel{}, err
	}
	of, err := ic.resolveOf(pl.Channel, in.Of)
	if err != nil {
		return intercom.Ask{}, Channel{}, err
	}
	ev := intercom.AskEvent{Event: intercom.AskEventAsk, Question: in.Question, Options: in.Options, Of: of}
	m, err := ic.Post(pl, intercom.Squawk{From: p.ID, Body: intercom.AskBody(ev, intercom.Ask{}, ""), ContentType: intercom.ContentPlain, Parts: []intercom.Part{intercom.AskPart(ev)}})
	if err != nil {
		return intercom.Ask{}, Channel{}, err
	}
	a, _ := ic.lg.GetAsk(m.ID)
	ic.asksChanged(p.ID)
	return a, pl.Channel, nil
}

// resolveOf resolves "user:<name|id>" refs to members of ch's project.
func (ic *Intercom) resolveOf(ch Channel, refs []string) ([]ident.ID, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	project, ok := ic.projectOf(ch)
	if !ok {
		return nil, ErrSendDenied
	}
	var out []ident.ID
	for _, r := range refs {
		kind, ref, _ := strings.Cut(r, ":")
		if (kind != "user" && kind != "human") || ref == "" {
			return nil, fmt.Errorf("%w: of %q (want user:<name>)", intercom.ErrInvalid, r)
		}
		u, ok := ic.memberUser(project, ref)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrSendUnresolved, r)
		}
		if !slices.Contains(out, u.ID) {
			out = append(out, u.ID)
		}
	}
	return out, nil
}

// AmendAsk rewords p's open ask (empty question/options keep theirs).
func (ic *Intercom) AmendAsk(p Poster, askID, question string, options []intercom.AskOption) (intercom.Ask, error) {
	return ic.askerEvent(p, intercom.AskEvent{Event: intercom.AskEventAmend, AskID: askID, Question: question, Options: options})
}

// WithdrawAsk closes p's open ask unanswered.
func (ic *Intercom) WithdrawAsk(p Poster, askID, reason string) (intercom.Ask, error) {
	return ic.askerEvent(p, intercom.AskEvent{Event: intercom.AskEventWithdraw, AskID: askID, Reason: reason})
}

func (ic *Intercom) askerEvent(p Poster, ev intercom.AskEvent) (intercom.Ask, error) {
	a, ok := ic.lg.GetAsk(ev.AskID)
	if !ok {
		return intercom.Ask{}, fmt.Errorf("%w: %q", intercom.ErrAskNotFound, ev.AskID)
	}
	if a.Asker != p.ID {
		return intercom.Ask{}, fmt.Errorf("%w: not your ask", intercom.ErrAskForbidden)
	}
	return ic.postAskEvent(a, p.ID, ev, "")
}

// AnswerAsk records person user's answer to the ask's version: they must be a
// current member of its live channel (ErrSendDenied), and the log refuses a
// closed ask, a changed version, or someone outside its Of.
func (ic *Intercom) AnswerAsk(user ident.ID, askID string, version int, choice, comment string) (intercom.Ask, error) {
	a, ok := ic.lg.GetAsk(askID)
	if !ok {
		return intercom.Ask{}, fmt.Errorf("%w: %q", intercom.ErrAskNotFound, askID)
	}
	ch, ok := ic.store.GetChannel(a.Channel)
	if !ok || ch.Status != StatusLive || user.Kind() != ident.User || !ic.isMemberOf(ch, user) {
		return intercom.Ask{}, ErrSendDenied
	}
	ev := intercom.AskEvent{Event: intercom.AskEventAnswer, AskID: askID, Version: version, Choice: choice, Comment: comment}
	return ic.postAskEvent(a, user, ev, ic.label(user))
}

// postAskEvent appends ev on ask a, from from, to its channel's members.
func (ic *Intercom) postAskEvent(a intercom.Ask, from ident.ID, ev intercom.AskEvent, byLabel string) (intercom.Ask, error) {
	ch, ok := ic.store.GetChannel(a.Channel)
	if !ok {
		return intercom.Ask{}, fmt.Errorf("%w: %q", intercom.ErrAskNotFound, a.ID)
	}
	m := intercom.Squawk{From: from, Body: intercom.AskBody(ev, a, byLabel), ContentType: intercom.ContentPlain, Parts: []intercom.Part{intercom.AskPart(ev)}}
	if ch.Status != StatusLive && ev.Event == intercom.AskEventWithdraw {
		// Closing an ask in a channel archived under it: recorded, heard by
		// no one.
		m.Channel = ch.ID
		if _, err := ic.lg.Append(m, nil); err != nil {
			return intercom.Ask{}, err
		}
	} else if _, err := ic.PostTrusted(ch, m); err != nil {
		return intercom.Ask{}, err
	}
	got, _ := ic.lg.GetAsk(a.ID)
	ic.asksChanged(a.Asker)
	return got, nil
}

// GetAsk is ask id's current state.
func (ic *Intercom) GetAsk(id string) (intercom.Ask, bool) { return ic.lg.GetAsk(id) }

// AsksOf is session's asks in state ("" = any), oldest first.
func (ic *Intercom) AsksOf(session ident.ID, state intercom.AskState) []intercom.Ask {
	return ic.lg.ListAsks(intercom.AskFilter{Asker: session, State: state})
}

// OpenAsksIn is channel ch's open asks, oldest first.
func (ic *Intercom) OpenAsksIn(ch ident.ID) []intercom.Ask {
	return ic.lg.ListAsks(intercom.AskFilter{Channel: ch, State: intercom.AskOpen})
}

// MayAnswer reports whether person user may answer a now: it is open, in a
// live channel they are a member of, and for them (Of) if it names anyone.
func (ic *Intercom) MayAnswer(user ident.ID, a intercom.Ask) bool {
	if a.State != intercom.AskOpen || user.Kind() != ident.User || len(a.Of) > 0 && !slices.Contains(a.Of, user) {
		return false
	}
	ch, ok := ic.store.GetChannel(a.Channel)
	return ok && ch.Status == StatusLive && ic.isMemberOf(ch, user)
}

// asksChanged tells the hook session's asks changed.
func (ic *Intercom) asksChanged(session ident.ID) {
	if ic.onAsks != nil && isSessionID(session) {
		ic.onAsks(session)
	}
}

// ReconcileAsks closes open asks nobody can answer any more — their channel
// was archived (a room removed), or their asker is gone — and recounts every
// live session's open asks. Serve runs it at startup and periodically, so a
// change made outside the intercom never strands a session in Needs you.
func (ic *Intercom) ReconcileAsks() {
	for _, a := range ic.lg.ListAsks(intercom.AskFilter{State: intercom.AskOpen}) {
		reason := ""
		if ch, ok := ic.store.GetChannel(a.Channel); !ok || ch.Status != StatusLive {
			reason = "channel archived"
		}
		if inst, ok := ic.store.GetInstance(string(a.Asker)); !ok || inst.Phase == PhaseGone {
			reason = "session ended"
		}
		if reason == "" {
			continue
		}
		ev := intercom.AskEvent{Event: intercom.AskEventWithdraw, AskID: a.ID, Reason: reason}
		if _, err := ic.postAskEvent(a, a.Asker, ev, ""); err != nil && ic.log != nil {
			ic.log.Warn("intercom: closing a stranded ask failed", "ask", a.ID, "err", err.Error())
		}
	}
	for _, inst := range ic.store.ListInstances() {
		if inst.Phase != PhaseGone && (inst.OpenAsks > 0 || ic.OpenAskCount(ident.ID(inst.ActorID)) > 0) {
			ic.asksChanged(ident.ID(inst.ActorID))
		}
	}
}

// withdrawAsksOf closes session's open asks: it ended.
func (ic *Intercom) withdrawAsksOf(session ident.ID) {
	for _, a := range ic.AsksOf(session, intercom.AskOpen) {
		ev := intercom.AskEvent{Event: intercom.AskEventWithdraw, AskID: a.ID, Reason: "session ended"}
		if _, err := ic.postAskEvent(a, session, ev, ""); err != nil && ic.log != nil {
			ic.log.Warn("intercom: withdrawing an ended session's ask failed", "ask", a.ID, "err", err.Error())
		}
	}
	ic.asksChanged(session)
}
