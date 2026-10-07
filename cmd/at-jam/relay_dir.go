package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/relay"
)

// directory is the concrete relay.Directory over the channel registry
// (intercom slice 2b): a squawk is rendered onto its channel's surfaces —
// ticket and room bindings, a chat's members' inboxes — and a foreign event
// is routed by its surface (a ticket's binding, a room's channel) or by the
// receipt of the post it replies to (Discord). project and selfIdentity are
// the Requisitioner's (Linear ingress only runs with one).
type directory struct {
	store        jam.Store
	ic           *jam.Intercom
	project      string
	selfIdentity string                  // Jam's Linear viewer displayName (self-post filter)
	tracker      func() (ident.ID, bool) // the Linear connection ticket bindings are on
	discord      ident.ID                // the Discord connection (Jam's bot); "" without one
	receipts     *fileReceipts           // discord-msg-id → receipt (nil when discord unconfigured)
	log          *slog.Logger            // optional: debug attribution notes
}

func (d *directory) debug(msg string, args ...any) {
	if d.log != nil {
		d.log.Debug(msg, args...)
	}
}

// Projects lists the projects a relay engine polls. Discord covers every
// project whose chat service is discord, plus the Requisitioner's project;
// Linear's feed is team-wide, polled once under the Requisitioner's project.
func (d *directory) Projects(service string) []string {
	if service != "discord" {
		return []string{d.project}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range d.store.ListProjects() {
		if p, ok := d.store.GetProject(name); ok && jam.ChatKind(d.store, p) == "discord" && !seen[name] {
			seen[name] = true
			out = append(out, name)
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
//     the Linear ticket of a session in the chat (the pre-channel "user:x
//     from a ticket session" delivery).
//
// Discord shows "<author>: " before every body; Linear before a person's
// (Jam posts as itself), never before a session's.
func (d *directory) Surfaces(service string, m intercom.Squawk) []relay.Delivery {
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
		for _, b := range ch.Bindings {
			conn, ok := d.store.GetConnection(b.ConnectionID)
			if !ok || conn.Kind != service || cameFrom(b.ConnectionID, b.Ref) {
				continue
			}
			out = append(out, relay.Delivery{Service: service, Address: b.Ref, BodyPrefix: prefix(service)})
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
			if mem.Handle != "" {
				mentions = append(mentions, "@"+strings.TrimPrefix(mem.Handle, "@"))
			}
		}
		// The Linear fallback carries a session's message to a person (the
		// pre-channel "user:x from a ticket session"); a person's chat
		// message is never made a public comment.
		// A session channel has no such fallback: it is never rendered onto a
		// ticket another session in it works on.
		if tracker, ok := d.tracker(); service == "linear" && ch.Kind == jam.SourceChat && isSession(m.From) && ok && ticket != "" && len(mentions) > 0 && !cameFrom(tracker, ticket) {
			out = append(out, relay.Delivery{Service: "linear", Address: ticket, BodyPrefix: strings.Join(mentions, " ") + " " + prefix("linear")})
		}
	}
	return out
}

// Route maps a foreign event into the channel log.
func (d *directory) Route(service, project string, e relay.Event) (relay.Routed, bool) {
	if service == "discord" {
		return d.routeDiscord(e)
	}
	return d.routeLinear(e)
}

// routeLinear drops Jam's own comments (its posts, echoed back on the feed),
// then maps the issue to the channel holding its binding — a ticket's, or a
// room bound to it. A comment on an issue no channel holds is unroutable.
func (d *directory) routeLinear(e relay.Event) (relay.Routed, bool) {
	if e.Author == d.selfIdentity {
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
	from, ok := d.author("linear", ch, e.Surface, e.AuthorID, e.Author)
	if !ok {
		return relay.Routed{}, false
	}
	r := relay.Routed{Channel: ch.ID, From: from, Origin: conn, OriginRef: e.Surface}
	if e.ReplyToForeign != "" {
		r.ReplyTo = "in:linear:" + e.ReplyToForeign
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
	conn, ok := d.store.ConnectionOfKind(kind)
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
