package jam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Escalation (docs/modules/ROOT/pages/usage/jam/escalation.adoc): a tier
// opens a dm between the waiting session and the tier's one person, or a
// group of the session and the tier's people, and posts the escalation
// notice there; a session on a unit also posts it to the unit's log. Until a
// session's turn-end text follows the conversation (COV-350), the tier's
// people are also called into its home channel, so they hear its reply.

const escalationPrefix = "escalate:"

// IsEscalationNotice reports whether a squawk id is an escalation notice.
func IsEscalationNotice(id string) bool { return strings.HasPrefix(id, escalationPrefix) }

// EscalationNoticeTargets are the users an escalation notice asked, as its
// id records them ("escalate:<session>:<tier>:<nanos>:<usr>,<usr>…"). The
// work log's copy records none ("…:<nanos>:log:"): its people are notified
// in the dm or group.
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

// Escalate calls tier's members into inst's home channel (members already
// in stay in), opens the dm or group of inst and them (Jam acting on the
// operator's policy: no addressing applies), and posts the escalation notice
// there as the session, naming everyone asked. When inst works on a
// unit, the notice is also posted to the unit's log, targeting nobody.
//
// The session must still be waiting, as the store has it now (the engine
// works from a list read earlier): one that is gone, ending or woken
// meanwhile gets no call-in, and nothing is opened or rejoined for it.
func (ic *Intercom) Escalate(_ context.Context, inst Instance, tier int, category string, members []Member) error {
	if len(members) == 0 {
		return nil
	}
	cur, ok := ic.store.GetInstance(inst.ActorID)
	if !ok || !Escalatable(cur) {
		return fmt.Errorf("%w: session %s is no longer waiting", ErrRemoved, inst.ActorID)
	}
	inst = cur
	project, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, inst.Project)
	}
	home, err := ic.HomeChannel(inst)
	if err != nil {
		return err
	}
	seq := ic.tail()
	parties := []ident.ID{ident.ID(inst.ActorID)}
	ids := make([]string, 0, len(members))
	names := make([]string, 0, len(members))
	for _, m := range members {
		if err := ic.store.JoinChannel(home.ID, m.User.ID, seq); err != nil {
			return err
		}
		parties = append(parties, m.User.ID)
		ids = append(ids, string(m.User.ID))
		names = append(names, m.User.Name)
	}
	ch, err := ic.chat(project, parties)
	if err != nil {
		return err
	}
	why := fmt.Sprintf("escalation tier %d", tier)
	if category != "" {
		why += ", " + category
	}
	subject := "from " + sessionLabel(inst)
	if inst.Unit != "" {
		subject += " on " + inst.Unit
	}
	id := fmt.Sprintf("%s%s:%d:%d:", escalationPrefix, inst.ActorID, tier, time.Now().UnixNano())
	note := intercom.Squawk{
		ID:          id + strings.Join(ids, ","),
		From:        ident.ID(inst.ActorID),
		Body:        fmt.Sprintf("Needs input %s — called in %s (%s).", subject, strings.Join(names, ", "), why),
		ContentType: intercom.ContentPlain,
	}
	if _, err := ic.PostTrusted(ch, note); err != nil {
		return err
	}
	work, ok, err := ic.ticketChannel(inst, true)
	if err != nil || !ok {
		return err
	}
	note.ID = id + "log:"
	_, err = ic.PostTrusted(work, note)
	return err
}
