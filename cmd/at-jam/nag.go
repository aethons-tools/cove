package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// notifier posts a notice as a session into its default channel: for a
// personal session, the chat with its owner. jam.Intercom.Notify.
type notifier interface {
	Notify(inst jam.Instance, id, body string) (intercom.Squawk, error)
}

// intercomNagger is wake-on's Nagger over the intercom: each nag, and the
// final reclaim notice, is a squawk posted AS the session into its chat with
// its owner. The relays deliver it like any squawk in that chat — so the
// owner's reply comes back to the session and wakes it — and the chat
// outlives the session, so the reclaim notice still arrives after teardown.
//
// Each nag carries the id jam.NagMessageID, so wake-on can tell an owner's
// "keep"/"release" reply to a nag from any other reply. The nag advertises
// those replies only when the owner could send one wake-on will act on: the
// project chats over discord and a reply from the owner is attributed to them
// (jam.DiscordAuthorOf) — they are bound to their Discord user id, or, unbound,
// their discord inbox is theirs alone.
type intercomNagger struct {
	log    notifier
	roster jam.Store        // nil: never offer keep/release
	now    func() time.Time // nil = time.Now
}

func (n intercomNagger) Nag(_ context.Context, inst jam.Instance, idle time.Duration) error {
	body := fmt.Sprintf(
		"Your personal session %s (%s) has been idle for %s. Reply to this message to pick it back up, or release it with: at-jam session release %s",
		inst.ActorID, inst.Role, formatIdle(idle), inst.ActorID)
	if n.ownerAttributable(inst) {
		body += ` Reply "keep" to keep it, or "release" to end it.`
	}
	return n.send(inst, jam.NagMessageID(inst.ActorID, n.clock()), body)
}

func (n intercomNagger) NotifyReclaimed(_ context.Context, inst jam.Instance, idle time.Duration) error {
	return n.send(inst, "", fmt.Sprintf(
		"Reclaimed your personal session %s (%s) after %s without a reply.",
		inst.ActorID, inst.Role, formatIdle(idle)))
}

// NotifyKept confirms an owner's "keep": the idle clock restarted, and the next
// nag comes after next.
func (n intercomNagger) NotifyKept(_ context.Context, inst jam.Instance, next time.Duration) error {
	return n.send(inst, "", fmt.Sprintf(
		"Keeping your personal session %s (%s). Next reminder in %s.",
		inst.ActorID, inst.Role, formatIdle(next)))
}

// NotifyReleased confirms an owner's "release": the session was torn down.
func (n intercomNagger) NotifyReleased(_ context.Context, inst jam.Instance) error {
	return n.send(inst, "", fmt.Sprintf(
		"Released your personal session %s (%s).", inst.ActorID, inst.Role))
}

// NotifyEnded tells a personal session's owner that it ended itself. A
// session with no owner (standing, ticket) gets no notice: wake-on logs it.
func (n intercomNagger) NotifyEnded(_ context.Context, inst jam.Instance, reason string) error {
	if inst.OwnerID == "" && inst.Owner == "" {
		return nil
	}
	return n.send(inst, "", fmt.Sprintf("Your personal session %s (%s) ended itself: %s", inst.ActorID, inst.Role, reason))
}

// ownerAttributable reports whether, in a discord-chat project, a reply from
// inst's owner to a nag in their inbox would be attributed to them
// (jam.DiscordAuthorOf, by their bound id or by their unshared inbox) — the
// condition under which the reply can act on the session.
func (n intercomNagger) ownerAttributable(inst jam.Instance) bool {
	if n.roster == nil {
		return false
	}
	p, ok := n.roster.GetProject(inst.Project)
	if !ok || jam.ChatKind(n.roster, p) != "discord" {
		return false
	}
	owner := inst.OwnerID
	if owner == "" {
		if owner, ok = n.roster.LookupName(ident.User, inst.Owner); !ok {
			return false
		}
	}
	m, ok := jam.MemberOf(n.roster, p.ID, owner)
	if !ok {
		return false
	}
	inbox, ok := m.Inbox("discord")
	if !ok {
		return false
	}
	// Ask the attribution rule itself about a reply from the owner's own
	// account (their bound id, or none) in their own inbox.
	got, _, ok := jam.DiscordAuthorOf(n.roster, p.ID, inbox, m.DiscordUID)
	return ok && got.User.ID == m.User.ID
}

func (n intercomNagger) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// send posts body as the session into its home channel; id "" gets a
// notice:… id (jam.Intercom.Notify).
func (n intercomNagger) send(inst jam.Instance, id, body string) error {
	if inst.OwnerID == "" && inst.Owner == "" {
		return fmt.Errorf("nag %s: no owner", inst.ActorID)
	}
	_, err := n.log.Notify(inst, id, body)
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
