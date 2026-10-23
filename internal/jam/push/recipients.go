package push

import (
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// Reader is what recipient selection reads (satisfied by adapters over
// jam.Store + *jam.Intercom).
type Reader interface {
	// Channel is a channel by id (ok=false: gone).
	Channel(id ident.ID) (jam.Channel, bool)
	// Members are ch's current members.
	Members(ch ident.ID) []ident.ID
	// CanSee is live-audience visibility: user is still a live member of
	// the channel's project and may see m.
	CanSee(user ident.ID, m intercom.Squawk) bool
	// PartyLabel is a participant's display label.
	PartyLabel(id ident.ID) string
	// ChannelLabel is ch's label as forUser sees it (a chat: the other
	// members' labels joined ", ").
	ChannelLabel(ch jam.Channel, forUser ident.ID) string
}

// Why a recipient is notified.
const (
	ReasonMessage    = "message"    // a message in a chat or session channel
	ReasonAsk        = "ask"        // an ask whose Of names them
	ReasonCallIn     = "callin"     // a call-in notice inviting them
	ReasonEscalation = "escalation" // an escalation notice targeting them
)

// Recipient is a user to notify and why.
type Recipient struct {
	User   ident.ID
	Reason string
}

// Recipients are the users squawk m notifies, sorted by user (spec §2):
// users only, current members of its channel who can see it, not its author,
// not focused on that channel (focused may be nil) — for any message in a
// chat or session channel, or, in any channel kind, an ask naming them, a
// call-in notice inviting them, or an escalation notice targeting them. Ask
// amend/withdraw/answer events notify nobody.
func Recipients(r Reader, m intercom.Squawk, focused func(user, channel ident.ID) bool) []Recipient {
	ch, ok := r.Channel(m.Channel)
	if !ok {
		return nil
	}
	reasons := map[ident.ID]string{}
	if ch.Kind == jam.SourceChat || ch.Kind == jam.SourceSession {
		for _, u := range r.Members(ch.ID) {
			reasons[u] = ReasonMessage
		}
	}
	if ev, ok := intercom.AskOf(m); ok {
		if ev.Event != intercom.AskEventAsk {
			return nil
		}
		for _, u := range ev.Of {
			reasons[u] = ReasonAsk
		}
	}
	if u, ok := jam.CallInInvitee(m.ID); ok {
		reasons[u] = ReasonCallIn
	}
	for _, u := range jam.EscalationNoticeTargets(m.ID) {
		reasons[u] = ReasonEscalation
	}
	members := r.Members(ch.ID)
	var out []Recipient
	for u, why := range reasons {
		if u.Kind() != ident.User || u == m.From || !slices.Contains(members, u) ||
			!r.CanSee(u, m) || focused != nil && focused(u, ch.ID) {
			continue
		}
		out = append(out, Recipient{User: u, Reason: why})
	}
	slices.SortFunc(out, func(a, b Recipient) int { return strings.Compare(string(a.User), string(b.User)) })
	return out
}
