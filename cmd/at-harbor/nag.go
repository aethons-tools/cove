package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/intercom"
)

// squawkAppender is the write side of the intercom log the nagger needs.
// Satisfied by intercom.Store (*intercom.Log, the Postgres store).
type squawkAppender interface {
	Append(m intercom.Squawk) (intercom.Squawk, error)
}

// intercomNagger is wake-on's Nagger over the intercom log: each nag, and the
// final reclaim notice, is a squawk sent AS the cove (actor:<id>) to its owner
// (human:<owner>), stamped with the cove's project. The relay delivers it like
// any cove message — so the owner's reply routes back to the cove and wakes it
// — and resolves the owner from the squawk's project, so the reclaim notice is
// still delivered after the cove has been torn down.
//
// Each nag carries the id harbor.NagMessageID, so wake-on can tell an owner's
// "keep"/"release" reply to a nag from any other reply. The nag advertises
// those replies only when the owner could send one wake-on will act on: the
// project chats over discord and the owner's discord inbox is theirs alone
// (harbor.DiscordInboxOwner) — a reply there is attributed to them.
type intercomNagger struct {
	log    squawkAppender
	roster nagRoster
	now    func() time.Time // nil = time.Now
}

// nagRoster is the slice of harbor.Store the nagger reads to decide whether a
// nag offers keep/release. *harbor.FileStore (and fakeStore) satisfy it.
type nagRoster interface {
	GetRoster(project string) (harbor.Roster, bool)
	GetProject(name string) (harbor.Project, bool)
}

func (n intercomNagger) Nag(_ context.Context, inst harbor.Instance, idle time.Duration) error {
	body := fmt.Sprintf(
		"Your personal session %s (%s) has been waiting on you for %s. Reply to this message to pick it back up, or release it with: at-harbor session release %s",
		inst.ActorID, inst.Role, formatIdle(idle), inst.ActorID)
	if n.ownerHasOwnInbox(inst) {
		body += ` Reply "keep" to keep it, or "release" to end it.`
	}
	return n.send(inst, harbor.NagMessageID(inst.ActorID, n.clock()), body)
}

func (n intercomNagger) NotifyReclaimed(_ context.Context, inst harbor.Instance, idle time.Duration) error {
	return n.send(inst, "", fmt.Sprintf(
		"Reclaimed your personal session %s (%s) after %s without a reply.",
		inst.ActorID, inst.Role, formatIdle(idle)))
}

// NotifyKept confirms an owner's "keep": the idle clock restarted, and the next
// nag comes after next.
func (n intercomNagger) NotifyKept(_ context.Context, inst harbor.Instance, next time.Duration) error {
	return n.send(inst, "", fmt.Sprintf(
		"Keeping your personal session %s (%s). Next reminder in %s.",
		inst.ActorID, inst.Role, formatIdle(next)))
}

// NotifyReleased confirms an owner's "release": the session was torn down.
func (n intercomNagger) NotifyReleased(_ context.Context, inst harbor.Instance) error {
	return n.send(inst, "", fmt.Sprintf(
		"Released your personal session %s (%s).", inst.ActorID, inst.Role))
}

// ownerHasOwnInbox reports whether inst's owner has a discord inbox in a
// discord-chat project that no one else shares — the condition under which a
// reply to a nag is attributed to the owner and so can act on the session.
func (n intercomNagger) ownerHasOwnInbox(inst harbor.Instance) bool {
	if n.roster == nil {
		return false
	}
	if p, ok := n.roster.GetProject(inst.Project); !ok || p.ChatService != "discord" {
		return false
	}
	r, ok := n.roster.GetRoster(inst.Project)
	if !ok {
		return false
	}
	h, ok := findHuman(r, inst.Owner)
	if !ok {
		return false
	}
	p, ok := h.DeliveryFor("discord")
	if !ok {
		return false
	}
	owner, ok := harbor.DiscordInboxOwner(r, p.Address)
	return ok && owner == inst.Owner
}

func (n intercomNagger) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// send appends body as the cove to its owner; id "" lets the log assign one.
func (n intercomNagger) send(inst harbor.Instance, id, body string) error {
	if inst.Owner == "" {
		return fmt.Errorf("nag %s: no owner", inst.ActorID)
	}
	_, err := n.log.Append(intercom.Squawk{
		ID:      id,
		From:    intercom.Target{Kind: "actor", Ref: inst.ActorID},
		To:      []intercom.Target{{Kind: "human", Ref: inst.Owner}},
		Body:    body,
		Project: inst.Project,
	})
	return err
}

// formatIdle renders an idle duration for a human, rounded to the minute:
// "45m", "4h", "4h 30m", "1d", "3d 0h 30m".
func formatIdle(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		d = time.Minute
	}
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || (days > 0 && mins > 0) {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	return strings.Join(parts, " ")
}
