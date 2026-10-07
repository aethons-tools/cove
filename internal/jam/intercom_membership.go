package jam

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Membership verbs (intercom slice 3b): a member calls someone in, a person
// joins a channel they can see, anyone but a channel's own session leaves.
// See docs/superpowers/specs/2026-10-07-intercom-slice3-session-channels-design.md §3.

// IsCallInNotice reports whether a squawk id is a call-in notice: Jam's own
// note inside the channel, which the relays never render onto a ticket's
// issue or a room's surface (only onto people's own inboxes).
func IsCallInNotice(id string) bool { return strings.HasPrefix(id, "callin:") }

// ErrFixedMembers refuses a membership change a channel doesn't take: a chat
// is its fixed member set, and a room takes no sessions.
var ErrFixedMembers = errors.New("intercom: this channel's members are fixed")

// CallIn has member p call who — "user:<name|id>" or "session:<label|id>" —
// into channel chID ("" for a session: its home channel). The inviter must
// be in the channel, and a session inviter's addressing must allow who (as
// for a send); the invitee must be live in the channel's project. They join
// from the tail and hear the inviter's notice. Calling in a member is a
// no-op. ErrSendDenied (not a member, not allowed, no such channel — alike),
// ErrSendUnresolved (no such invitee), ErrFixedMembers.
func (ic *Intercom) CallIn(p Poster, chID ident.ID, who string) (Channel, ident.ID, error) {
	var ch Channel
	switch {
	case chID == "" && p.Session != nil:
		home, err := ic.HomeChannel(*p.Session)
		if err != nil {
			return Channel{}, "", err
		}
		ch = home
	default:
		got, ok := ic.store.GetChannel(chID)
		if !ok {
			return Channel{}, "", ErrSendDenied
		}
		ch = got
	}
	if !ic.isMemberOf(ch, p.ID) {
		return Channel{}, "", ErrSendDenied
	}
	if ch.Status != StatusLive {
		return Channel{}, "", fmt.Errorf("%w: channel %s", ErrRemoved, ch.ID)
	}
	if ch.Kind == SourceChat {
		return Channel{}, "", ErrFixedMembers
	}
	project, ok := ic.projectOf(ch)
	if !ok {
		return Channel{}, "", ErrSendDenied
	}
	invitee, err := ic.resolveInvitee(p, project, ch, who)
	if err != nil {
		return Channel{}, "", err
	}
	if ic.isMemberOf(ch, invitee) {
		return ch, invitee, nil
	}
	if err := ic.store.JoinChannel(ch.ID, invitee, ic.tail()); err != nil {
		return Channel{}, "", err
	}
	note := intercom.Squawk{ID: fmt.Sprintf("callin:%s:%s:%d", ch.ID, invitee, time.Now().UnixNano()), From: p.ID, Body: ic.label(p.ID) + " called in " + ic.label(invitee), ContentType: intercom.ContentPlain}
	if _, err := ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, p.ID)}, note); err != nil {
		return Channel{}, "", err
	}
	return ch, invitee, nil
}

// resolveInvitee resolves who for a call-in by p into ch of project:
// checked against a session inviter's addressing before it is looked up
// (403 before 404).
func (ic *Intercom) resolveInvitee(p Poster, project Project, ch Channel, who string) (ident.ID, error) {
	kind, ref, _ := strings.Cut(who, ":")
	if ref == "" || (kind != "user" && kind != "human" && kind != "session") {
		return "", ErrSendDenied
	}
	var globs []string
	session := p.Session != nil
	if session {
		if p.Actor == nil {
			return "", ErrSendDenied
		}
		globs = ic.ceiling(p, project.Name)
	}
	if kind == "session" {
		if ch.Kind == SourceRoom {
			return "", ErrFixedMembers // rooms are post-only for sessions
		}
		target, found := ic.sessionNamed(project, ref)
		if !session && found && !ic.MayReach(p.ID, target) {
			return "", ErrSendDenied // never a back door into someone's personal session
		}
		if session && !(found && target.ActorID == p.Session.ActorID) {
			allowed := found && anyAllowed([]string{"session:" + sessionLabel(target), "session:" + target.ActorID}, globs)
			if tracker, ok := ic.tracker(); allowed && ok && !sessionTicketAllowed(ic.store, tracker, *p.Session, target, globs) {
				allowed = false
			}
			switch {
			case allowed:
			case anyAllowed([]string{"session:" + ref}, globs) && !found:
				return "", ErrSendUnresolved
			default:
				return "", ErrSendDenied
			}
		}
		if !found {
			return "", ErrSendUnresolved
		}
		return ident.ID(target.ActorID), nil
	}
	u, found := ic.memberUser(project, ref)
	if session {
		switch {
		case found && anyAllowed([]string{"user:" + u.Name, "user:" + string(u.ID)}, globs):
		case anyAllowed([]string{"user:" + ref}, globs) && !found:
			return "", ErrSendUnresolved
		default:
			return "", ErrSendDenied
		}
	}
	if !found {
		return "", ErrSendUnresolved
	}
	return u.ID, nil
}

// projectOf is the project a channel belongs to.
func (ic *Intercom) projectOf(ch Channel) (Project, bool) {
	e, ok := ic.store.Resolve(ch.ProjectID)
	if !ok {
		return Project{}, false
	}
	return ic.store.GetProject(e.Name)
}

// JoinChannel joins person u to channel chID from the tail: one they can see
// (CanSee — never a personal session's channel they weren't called into)
// and that takes members (not a chat). Joining one's channel is a no-op.
// ErrSendDenied alike for a channel that doesn't exist or isn't theirs.
func (ic *Intercom) JoinChannel(u, chID ident.ID) error {
	ch, ok := ic.store.GetChannel(chID)
	if !ok || u.Kind() != ident.User || ch.Kind == SourceChat || !ic.CanSee(u, ch) {
		return ErrSendDenied
	}
	if ch.Status != StatusLive {
		return fmt.Errorf("%w: channel %s", ErrRemoved, ch.ID)
	}
	return ic.store.JoinChannel(ch.ID, u, ic.tail())
}

// LeaveChannel ends p's membership of chID from the tail: what was delivered
// stays. A session never leaves its home (its own channel, or the ticket it
// works while it lives); a chat is fixed. Leaving a channel one isn't in is
// a no-op.
func (ic *Intercom) LeaveChannel(p, chID ident.ID) error {
	ch, ok := ic.store.GetChannel(chID)
	if !ok {
		return ErrSendDenied
	}
	if !ic.isMemberOf(ch, p) {
		if !ic.CanSee(p, ch) {
			return ErrSendDenied // never tells whether it exists
		}
		return nil
	}
	if ch.Kind == SourceChat {
		return ErrFixedMembers
	}
	if isSessionID(p) {
		if inst, ok := ic.store.GetInstance(string(p)); ok && inst.Phase != PhaseGone {
			if home, ok := ic.homeIfExists(inst); ok && home.ID == ch.ID {
				return ErrSendDenied
			}
		}
	}
	return ic.store.LeaveChannel(ch.ID, p, ic.tail())
}

// homeIfExists is inst's home channel if it already exists (creating none).
func (ic *Intercom) homeIfExists(inst Instance) (Channel, bool) {
	if inst.Unit != "" {
		if ch, ok := ic.TicketChannelOf(inst); ok {
			return ch, true
		}
	}
	return ic.ownSessionChannel(inst)
}
