package jam

import (
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// LegacyInbox is the read side of the frozen legacy log an inbox still
// reaches: squawks addressed to a target before the cutover.
type LegacyInbox interface {
	ReadInboxSince(t intercom.Target, afterSeq int64, limit int) []intercom.LegacySquawk
	ReadInboxBefore(t intercom.Target, beforeSeq int64, limit int) []intercom.LegacySquawk
}

// SessionInbox reads a participant's inbox: its deliveries in the channel
// log and — for a session from before the cutover — the legacy squawks
// addressed to it (actor:<id>), below the cutover seq. One queue, ordered by
// seq across both. A legacy entry comes back with no Channel and with From
// set to its legacy target ("actor:<id>", "human:<name>"); Party renders it.
type SessionInbox struct {
	Log    intercom.Store
	Legacy LegacyInbox // nil: no legacy log
}

func (in SessionInbox) legacy(p ident.ID, rows []intercom.LegacySquawk) []intercom.Squawk {
	cut := in.Log.CutoverSeq()
	var out []intercom.Squawk
	for _, m := range rows {
		if m.Seq >= cut {
			continue
		}
		out = append(out, intercom.Squawk{Seq: m.Seq, ID: m.ID, From: ident.ID(m.From.String()), Body: m.Body, At: m.At,
			ReplyTo: m.ReplyTo, ContentType: m.ContentType})
	}
	return out
}

// Since reads p's inbox after a seq, ascending, at most limit (<= 0: all).
func (in SessionInbox) Since(p ident.ID, afterSeq int64, limit int) []intercom.Squawk {
	var out []intercom.Squawk
	if in.Legacy != nil && afterSeq < in.Log.CutoverSeq()-1 {
		out = in.legacy(p, in.Legacy.ReadInboxSince(intercom.Target{Kind: "actor", Ref: string(p)}, afterSeq, limit))
	}
	if limit > 0 && len(out) >= limit {
		return out[:limit]
	}
	rest := 0
	if limit > 0 {
		rest = limit - len(out)
	}
	return append(out, in.Log.InboxSince(p, afterSeq, rest)...)
}

// Before reads the limit entries of p's inbox nearest below a seq (<= 0: the
// end), ascending.
func (in SessionInbox) Before(p ident.ID, beforeSeq int64, limit int) []intercom.Squawk {
	newer := in.Log.InboxBefore(p, beforeSeq, limit)
	if in.Legacy == nil || (limit > 0 && len(newer) >= limit) {
		return newer
	}
	cut := in.Log.CutoverSeq()
	bound := cut
	if beforeSeq > 0 && beforeSeq < cut {
		bound = beforeSeq
	}
	rest := 0
	if limit > 0 {
		rest = limit - len(newer)
	}
	return append(in.legacy(p, in.Legacy.ReadInboxBefore(intercom.Target{Kind: "actor", Ref: string(p)}, bound, rest)), newer...)
}

// Party is a participant or channel as the wire and the UIs show it.
type Party struct {
	ID    ident.ID `json:"id,omitempty"`
	Kind  string   `json:"kind"`
	Label string   `json:"label"`
}

// PartyOf renders a participant: a session, user or account by id — or a
// legacy target ("actor:<id>", "human:<name>") from before the cutover.
func (ic *Intercom) PartyOf(id ident.ID) Party {
	if kind, ref, ok := strings.Cut(string(id), ":"); ok {
		switch kind {
		case "actor":
			return Party{ID: ident.ID(ref), Kind: "session", Label: ic.label(ident.ID(ref))}
		default:
			return Party{Kind: "user", Label: ref}
		}
	}
	kind := "session"
	switch id.Kind() {
	case ident.User:
		kind = "user"
	case ident.Account:
		kind = "account"
	}
	return Party{ID: id, Kind: kind, Label: ic.label(id)}
}

// ChannelParty renders a channel; one that is gone (its project was removed)
// is "(removed channel)".
func (ic *Intercom) ChannelParty(id ident.ID) Party {
	ch, ok := ic.store.GetChannel(id)
	if !ok {
		return Party{ID: id, Kind: "channel", Label: "(removed channel)"}
	}
	label := ch.Label
	if ch.Status != StatusLive {
		label += " (archived)"
	}
	return Party{ID: id, Kind: string(ch.Kind), Label: label}
}
