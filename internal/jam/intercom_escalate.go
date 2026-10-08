package jam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Escalation as call-in (intercom slice 4): a tier's people are called into
// the waiting session's home channel and the escalation notice is posted
// there; the relays render it wherever the channel goes (on a ticket's issue
// with the tier's @-handles). See
// docs/superpowers/specs/2026-10-07-intercom-slice4-escalation-call-in-design.md.

const escalationPrefix = "escalate:"

// IsEscalationNotice reports whether a squawk id is an escalation notice.
func IsEscalationNotice(id string) bool { return strings.HasPrefix(id, escalationPrefix) }

// EscalationNoticeTargets are the users an escalation notice called in, as
// its id records them ("escalate:<session>:<tier>:<nanos>:<usr>,<usr>…").
func EscalationNoticeTargets(id string) []ident.ID {
	if !IsEscalationNotice(id) {
		return nil
	}
	rest := strings.TrimPrefix(id, escalationPrefix)
	i := strings.LastIndex(rest, ":")
	if i < 0 || i == len(rest)-1 {
		return nil
	}
	var out []ident.ID
	for _, u := range strings.Split(rest[i+1:], ",") {
		if id, err := ident.Parse(u); err == nil && id.Kind() == ident.User {
			out = append(out, id)
		}
	}
	return out
}

// Escalatable reports whether a session can still be escalated: waiting, and
// not ending, lost or gone.
func Escalatable(inst Instance) bool {
	switch inst.Phase {
	case PhaseTerminating, PhaseGone, PhaseLost:
		return false
	}
	return inst.Activity == ActivityWaiting
}

// Escalate calls tier's members into inst's home channel (Jam acting on the
// operator's policy: no addressing applies; members already in stay in) and
// posts the escalation notice there as the session, naming everyone asked.
//
// The session must still be waiting, as the store has it now (the engine
// works from a list read earlier): one that is gone, ending or woken
// meanwhile gets no call-in, and nothing is reopened or rejoined for it.
func (ic *Intercom) Escalate(_ context.Context, inst Instance, tier int, category string, members []Member) error {
	if len(members) == 0 {
		return nil
	}
	cur, ok := ic.store.GetInstance(inst.ActorID)
	if !ok || !Escalatable(cur) {
		return fmt.Errorf("%w: session %s is no longer waiting", ErrRemoved, inst.ActorID)
	}
	inst = cur
	ch, err := ic.HomeChannel(inst)
	if err != nil {
		return err
	}
	seq := ic.tail()
	ids := make([]string, 0, len(members))
	names := make([]string, 0, len(members))
	for _, m := range members {
		if err := ic.store.JoinChannel(ch.ID, m.User.ID, seq); err != nil {
			return err
		}
		ids = append(ids, string(m.User.ID))
		names = append(names, m.User.Name)
	}
	why := fmt.Sprintf("escalation tier %d", tier)
	if category != "" {
		why += ", " + category
	}
	subject, after := "from "+sessionLabel(inst), ""
	if ch.Kind == SourceTicket {
		subject = "on " + inst.Unit
		after = " Replies here go to " + inst.Unit + "'s conversation, its issue included."
	}
	note := intercom.Squawk{
		ID:          fmt.Sprintf("%s%s:%d:%d:%s", escalationPrefix, inst.ActorID, tier, time.Now().UnixNano(), strings.Join(ids, ",")),
		From:        ident.ID(inst.ActorID),
		Body:        fmt.Sprintf("Needs input %s — called in %s (%s).%s", subject, strings.Join(names, ", "), why, after),
		ContentType: intercom.ContentPlain,
	}
	_, err = ic.PostTrusted(ch, note)
	return err
}
