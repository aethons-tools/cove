package main

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/notify"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/relay"
	"github.com/aethons-tools/cove/internal/tracker"
)

// directory is the concrete relay.Directory over the channel registry
// (intercom slice 2b): a squawk is rendered onto its channel's surfaces —
// ticket and room bindings, a chat's members' inboxes — and a foreign event
// is routed by its surface (a ticket's binding, a room's channel) or by the
// receipt of the post it replies to (Discord). project and selfIdentity are
// the Requisitioner's (tracker ingress only runs with one). The push service
// is egress only: a squawk's notified people's devices. So is notify: the
// same people's open Jam streams.
type directory struct {
	store        jam.Store
	ic           *jam.Intercom
	project      string
	selfIdentity string                  // Jam's tracker identity (Linear displayName, GitHub login): the self-post filter
	tracker      func() (ident.ID, bool) // the tracker connection ticket bindings are on
	trackerKind  string                  // the tracker's kind (relay service); "" = linear
	discord      ident.ID                // the Discord connection (Jam's bot); "" without one
	receipts     *fileReceipts           // discord-msg-id → receipt (nil when discord unconfigured)
	log          *slog.Logger            // optional: debug attribution notes
	pushOn       bool                    // runtime.push is set: Surfaces("push") lists devices
	pushFocus    *push.Focus             // who has which conversation open (nil: nobody's suppressed)
	notifyOn     bool                    // the user app is served: Surfaces("notify") lists recipients
}

func (d *directory) debug(msg string, args ...any) {
	if d.log != nil {
		d.log.Debug(msg, args...)
	}
}

// Projects lists the projects a relay engine polls, by id. Discord covers every
// project whose chat service is discord, plus the Requisitioner's project;
// The tracker's feed (Linear's team, GitHub's repo) is polled once under the
// Requisitioner's project.
// Push and notify poll nothing (no ingress).
func (d *directory) Projects(service string) []string {
	if service == push.Service || service == notify.Service {
		return nil
	}
	if service != "discord" {
		return []string{d.project}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range d.store.ListProjects() {
		if p, ok := d.store.GetProject(name); ok && jam.ChatKind(d.store, p) == "discord" && !seen[string(p.ID)] {
			seen[string(p.ID)] = true
			out = append(out, string(p.ID)) // by id: cursors outlive a rename
		}
	}
	if d.project != "" && !seen[d.project] {
		out = append(out, d.project)
	}
	return out
}

// projectOf finds a channel's project record.
func (d *directory) projectOf(ch jam.Channel) (jam.Project, bool) {
	e, ok := d.store.Resolve(ch.ProjectID)
	if !ok {
		return jam.Project{}, false
	}
	return d.store.GetProject(e.Name)
}

// isSession reports whether a participant is a session (a ses_ id, or a
// grandfathered one: the only ids that aren't registry ids).
func isSession(id ident.ID) bool {
	_, err := ident.Parse(string(id))
	return err != nil || id.Kind() == ident.Session
}

// Surfaces lists where m goes on service, minus the surface it came from.
//
//   - A ticket or room: each of its bindings on a connection of service.
//   - A chat: each person in it (but the author) on their Discord inbox when
//     the project chats over Discord; anyone without one is @-mentioned on
//     the tracker ticket of a session in the chat (the pre-channel "user:x
//     from a ticket session" delivery).
//
// Discord shows "<author>: " before every body; the tracker before a person's
// (Jam posts as itself), never before a session's. Push: see pushSurfaces;
// notify: see notifySurfaces.
func (d *directory) Surfaces(service string, m intercom.Squawk) []relay.Delivery {
	switch service {
	case push.Service:
		return d.pushSurfaces(m) // asks notify: not paused for parts
	case notify.Service:
		return d.notifySurfaces(m) // likewise
	}
	if len(m.Parts) > 0 {
		return nil // relays are paused for parts (asks): the Inbox (/app/user) only
	}
	ch, ok := d.store.GetChannel(m.Channel)
	if !ok {
		return nil
	}
	from := d.ic.PartyOf(m.From)
	prefix := func(svc string) string {
		if svc == "discord" || !isSession(m.From) {
			return from.Label + ": "
		}
		return ""
	}
	cameFrom := func(conn ident.ID, ref string) bool { return m.Origin == conn && m.OriginRef == ref }
	var out []relay.Delivery
	switch ch.Kind {
	case jam.SourceTicket, jam.SourceRoom:
		if jam.IsLocalNotice(m.ID) {
			return nil // Jam's own note: never a public comment or a room post
		}
		escalated := escalationTargets(d, ch, m.ID)
		for _, b := range ch.Bindings {
			conn, ok := d.store.GetConnection(b.ConnectionID)
			if !ok || conn.Kind != service || cameFrom(b.ConnectionID, b.Ref) {
				continue
			}
			bp := prefix(service)
			if service == d.trackerService() && len(escalated.handles) > 0 {
				bp = strings.Join(escalated.handles, " ") + " " + bp // the tier's people, @-mentioned as before
			}
			out = append(out, relay.Delivery{Service: service, Address: b.Ref, BodyPrefix: bp})
		}
		// A ticket whose issue binding a room holds: its escalation still goes
		// onto the issue (the old ping's place), by the ticket's key.
		if service == d.trackerService() && ch.Kind == jam.SourceTicket && len(escalated.handles) > 0 && len(out) == 0 {
			if tracker, ok := d.tracker(); ok {
				if conn, key, ok := strings.Cut(ch.Key, "/"); ok && conn == string(tracker) {
					out = append(out, relay.Delivery{Service: service, Address: key, BodyPrefix: strings.Join(escalated.handles, " ") + " " + prefix(service)})
				}
			}
		}
		if service == "discord" {
			for _, inbox := range escalated.inboxes {
				out = append(out, relay.Delivery{Service: "discord", Address: inbox, BodyPrefix: prefix("discord")})
			}
		}
	case jam.SourceChat, jam.SourceSession: // a session channel reaches its people as a chat does
		p, ok := d.projectOf(ch)
		if !ok {
			return nil
		}
		discordChat := jam.ChatKind(d.store, p) == "discord"
		var ticket string
		var mentions []string
		for _, mm := range d.store.ChannelMembers(ch.ID) {
			if isSession(mm.ParticipantID) {
				if inst, ok := d.store.GetInstance(string(mm.ParticipantID)); ok && inst.Phase != jam.PhaseGone && inst.Unit != "" && ticket == "" {
					ticket = inst.Unit
				}
				continue
			}
			if mm.ParticipantID == m.From || mm.ParticipantID.Kind() != ident.User {
				continue
			}
			mem, ok := jam.MemberOf(d.store, p.ID, mm.ParticipantID)
			if !ok {
				continue // no longer a member of the project: nothing to deliver to
			}
			if inbox, ok := mem.Inbox("discord"); ok && discordChat {
				if service == "discord" && !cameFrom(d.discord, inbox) {
					out = append(out, relay.Delivery{Service: "discord", Address: inbox, BodyPrefix: prefix("discord")})
				}
				continue
			}
			if h := mem.HandleOn(d.trackerService()); h != "" {
				mentions = append(mentions, "@"+strings.TrimPrefix(h, "@"))
			}
		}
		// The tracker fallback carries a session's message to a person (the
		// pre-channel "user:x from a ticket session"); a person's chat
		// message is never made a public comment.
		// A session channel has no such fallback: it is never rendered onto a
		// ticket another session in it works on.
		// The session's unit must be a ticket on the tracker (canonical on
		// github; another repo's would never land).
		if tracker, ok := d.tracker(); service == d.trackerService() && ch.Kind == jam.SourceChat && isSession(m.From) && ok && len(mentions) > 0 {
			if key, onTicket := jam.TicketUnit(d.store, tracker, ticket); onTicket && !cameFrom(tracker, key) {
				out = append(out, relay.Delivery{Service: service, Address: key, BodyPrefix: strings.Join(mentions, " ") + " " + prefix(service)})
			}
		}
	}
	return out
}

// pushSurfaces lists one delivery per (recipient, device) of m — its
// recipients by the notify rule (push.Recipients over the live audience,
// minus people focused on its channel), each of their subscriptions
// addressed by push.AddressFor so the engine tracks every device apart. For
// push, BodyPrefix carries the recipient's user id (push never renders a
// body prefix): the surface's Deliver reads it to find the device.
func (d *directory) pushSurfaces(m intercom.Squawk) []relay.Delivery {
	if !d.pushOn {
		return nil
	}
	var focused func(user, channel ident.ID) bool
	if d.pushFocus != nil {
		focused = d.pushFocus.Focused
	}
	var out []relay.Delivery
	for _, rc := range push.Recipients(pushReader{store: d.store, ic: d.ic}, m, focused) {
		for _, sub := range d.store.PushSubscriptions(rc.User) {
			out = append(out, relay.Delivery{Service: push.Service, Address: push.AddressFor(sub.Endpoint), BodyPrefix: string(rc.User)})
		}
	}
	return out
}

// escalated is how an escalation notice reaches the people it called in:
// their tracker @-handles, and (in a Discord-chat project) their inboxes.
type escalated struct{ handles, inboxes []string }

// escalationTargets resolves the people an escalation notice id records to
// how they are reached (none for any other squawk).
func escalationTargets(d *directory, ch jam.Channel, id string) escalated {
	var out escalated
	users := jam.EscalationNoticeTargets(id)
	if len(users) == 0 {
		return out
	}
	p, ok := d.projectOf(ch)
	if !ok {
		return out
	}
	discordChat := jam.ChatKind(d.store, p) == "discord"
	for _, u := range users {
		mem, ok := jam.MemberOf(d.store, p.ID, u)
		if !ok {
			continue
		}
		if h := mem.HandleOn(d.trackerService()); h != "" {
			out.handles = append(out.handles, "@"+strings.TrimPrefix(h, "@"))
		}
		if inbox, ok := mem.Inbox("discord"); ok && discordChat && !slices.Contains(out.inboxes, inbox) {
			out.inboxes = append(out.inboxes, inbox)
		}
	}
	return out
}

// Route maps a foreign event into the channel log.
func (d *directory) Route(service, project string, e relay.Event) (relay.Routed, bool) {
	switch service {
	case push.Service, notify.Service:
		return relay.Routed{}, false // no ingress (never polled)
	case "discord":
		return d.routeDiscord(e)
	}
	if service == d.trackerService() {
		return d.routeTracker(service, e)
	}
	return relay.Routed{}, false
}

// trackerService is the relay service ticket bindings are delivered on.
func (d *directory) trackerService() string { return cmp.Or(d.trackerKind, tracker.KindLinear) }

// routeTracker drops Jam's own comments (its posts, echoed back on the feed),
// then maps the issue to the channel holding its binding: a ticket's, or a
// room bound to it. A comment on an issue no channel holds is unroutable.
// On GitHub it also drops any Bot account's comment, and an unknown viewer
// (an App token's ErrNoViewer) drops nobody by name; Linear's self filter is
// as it always was (an author equal to the viewer, even an empty one).
func (d *directory) routeTracker(service string, e relay.Event) (relay.Routed, bool) {
	if service == tracker.KindGitHub {
		if e.AuthorBot || (d.selfIdentity != "" && e.Author == d.selfIdentity) {
			return relay.Routed{}, false
		}
	} else if e.Author == d.selfIdentity {
		return relay.Routed{}, false
	}
	conn, ok := d.tracker()
	if !ok {
		return relay.Routed{}, false
	}
	ch, ok := d.store.ChannelByBinding(conn, e.Surface)
	if !ok {
		return relay.Routed{}, false
	}
	from, ok := d.author(service, ch, e.Surface, e.AuthorID, e.Author)
	if !ok {
		return relay.Routed{}, false
	}
	r := relay.Routed{Channel: ch.ID, From: from, Origin: conn, OriginRef: e.Surface}
	if e.ReplyToForeign != "" {
		r.ReplyTo = "in:" + service + ":" + e.ReplyToForeign
	}
	return r, true
}

// routeDiscord maps a Discord message: a reply goes to the conversation of
// the post it answers (its receipt: the channel, or — for a receipt from
// before the channel log — the author session's default channel while it
// lives); a non-reply goes to the room bound to the Discord channel, if any.
// Anything else — a non-reply in a shared inbox, Jam's own posts, a bot —
// is unroutable.
func (d *directory) routeDiscord(e relay.Event) (relay.Routed, bool) {
	if e.AuthorBot {
		return relay.Routed{}, false
	}
	var ch jam.Channel
	r := relay.Routed{Origin: d.discord, OriginRef: e.Surface}
	if e.ReplyToForeign != "" {
		rc, ok := d.receipts.Lookup(e.ReplyToForeign)
		if !ok {
			return relay.Routed{}, false
		}
		r.ReplyTo = rc.Message
		if r.ReplyTo == "" {
			r.ReplyTo = "in:discord:" + e.ReplyToForeign
		}
		if rc.Channel != "" {
			if ch, ok = d.store.GetChannel(ident.ID(rc.Channel)); !ok || ch.Status != jam.StatusLive {
				return relay.Routed{}, false // its conversation is gone (archived, or its project removed)
			}
		} else {
			inst, live := d.store.GetInstance(rc.Actor)
			if !live || inst.Phase == jam.PhaseGone {
				d.debug("relay: legacy discord receipt for an ended session; dropped", "session", rc.Actor)
				return relay.Routed{}, false
			}
			var err error
			if ch, err = d.ic.HomeChannel(inst); err != nil {
				return relay.Routed{}, false
			}
		}
	} else {
		var ok bool
		if ch, ok = d.store.ChannelByBinding(d.discord, e.Surface); !ok {
			return relay.Routed{}, false
		}
	}
	from, ok := d.author("discord", ch, e.Surface, e.AuthorID, e.Author)
	if !ok {
		return relay.Routed{}, false
	}
	r.Channel, r.From = ch.ID, from
	return r, true
}

// author names who a foreign event is from: on Discord, the member the
// attribution rules give it to (jam.DiscordAuthorOf: their bound Discord id,
// or an unbound member's own inbox); otherwise the account the author's
// service id is recorded as (never their display name, which anyone can
// set) — or the user an operator linked it to, when they're a member of the
// channel's project. ok=false: no service id to record.
func (d *directory) author(kind string, ch jam.Channel, surface, uid, label string) (ident.ID, bool) {
	p, ok := d.projectOf(ch)
	if !ok {
		return "", false
	}
	if kind == "discord" {
		if m, by, ok := jam.DiscordAuthorOf(d.store, p.ID, surface, uid); ok {
			d.debug("relay: discord reply attributed", "project", p.Name, "user", string(m.User.ID), "by", by)
			return m.User.ID, true
		}
	}
	if uid == "" {
		return "", false
	}
	conn, ok := d.authorConnection(kind)
	if !ok {
		var err error
		if conn, err = d.store.CreateConnection(jam.Connection{Kind: kind, Name: kind}); err != nil {
			if conn, ok = d.store.ConnectionOfKind(kind); !ok {
				d.debug("relay: record author: no connection", "kind", kind, "error", err.Error())
				return "", false
			}
		}
	}
	a, ok := d.store.AccountByUID(conn.ID, uid)
	if !ok || a.Label != label {
		var err error
		if a, err = d.store.UpsertAccount(jam.Account{ConnectionID: conn.ID, ServiceUID: uid, Label: label}); err != nil {
			d.debug("relay: record author failed", "kind", kind, "error", err.Error())
			return "", false
		}
	}
	if a.UserID != "" {
		if u, ok := d.store.GetUser(a.UserID); ok && u.Status == jam.StatusLive && d.store.IsMember(p.ID, u.ID) {
			return u.ID, true
		}
	}
	return a.ID, true
}

// authorConnection is the connection kind's authors are recorded on: the
// tracker connection for the tracker service (a Jam may hold several
// connections of its kind), else the store's one of that kind.
func (d *directory) authorConnection(kind string) (jam.Connection, bool) {
	if kind == d.trackerService() {
		if id, ok := d.tracker(); ok {
			if c, ok := d.store.GetConnection(id); ok {
				return c, true
			}
		}
	}
	return d.store.ConnectionOfKind(kind)
}

// Post appends a routed foreign event to its channel, trusted (its surface
// already placed it), with the channel's audience.
func (d *directory) Post(r relay.Routed, m intercom.Squawk) error {
	ch, ok := d.store.GetChannel(r.Channel)
	if !ok {
		return fmt.Errorf("%w: %w", relay.ErrPermanent, jam.ErrChannelNotFound)
	}
	_, err := d.ic.PostTrusted(ch, m)
	for _, permanent := range []error{jam.ErrRemoved, intercom.ErrDuplicateID, intercom.ErrInvalid} {
		if errors.Is(err, permanent) {
			return fmt.Errorf("%w: %w", relay.ErrPermanent, err)
		}
	}
	return err
}
