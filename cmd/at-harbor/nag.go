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
type intercomNagger struct {
	log squawkAppender
}

func (n intercomNagger) Nag(_ context.Context, inst harbor.Instance, idle time.Duration) error {
	return n.send(inst, fmt.Sprintf(
		"Your personal session %s (%s) has been waiting on you for %s. Reply to this message to pick it back up, or release it with: at-harbor session release %s",
		inst.ActorID, inst.Role, formatIdle(idle), inst.ActorID))
}

func (n intercomNagger) NotifyReclaimed(_ context.Context, inst harbor.Instance, idle time.Duration) error {
	return n.send(inst, fmt.Sprintf(
		"Reclaimed your personal session %s (%s) after %s without a reply.",
		inst.ActorID, inst.Role, formatIdle(idle)))
}

func (n intercomNagger) send(inst harbor.Instance, body string) error {
	if inst.Owner == "" {
		return fmt.Errorf("nag %s: no owner", inst.ActorID)
	}
	_, err := n.log.Append(intercom.Squawk{
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
