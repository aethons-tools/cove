package userui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/numfmt"
)

// The session view (docs/usage/jam/intercom-ui.md#the-session-view): a
// session's home channel gains a header — who it is, what it's doing, what
// it costs, quick actions — and tabs for the pane beside the messages.

// SessionOps are the session actions the header offers (serve: the
// supervisor and the standing reconciler).
type SessionOps interface {
	Teardown(ctx context.Context, actorID string) error
	// Upgrade queues a standing session's upgrade, returning its state.
	Upgrade(inst jam.Instance) (string, error)
	// UpgradeState is a standing session's pending upgrade ("" none).
	UpgradeState(inst jam.Instance) string
	// Restart restarts a session's studio, keeping the session (a standing
	// one is re-raised by its reconciler, any other recovered).
	Restart(ctx context.Context, actorID string) error
	// Command sends a session-control press (interrupt, compact, clear),
	// resuming a paused session first; by is who pressed it.
	Command(ctx context.Context, inst jam.Instance, c jam.SessionCommand, by string) error
}

// homeSession is the live session whose home channel chID is (see
// jam.HomeSession).
func homeSession(d Deps, chID string) (jam.Instance, bool) {
	return jam.HomeSession(d.Store, ident.ID(chID))
}

// sessionFor is session id if viewer p may see its home channel.
func (h *handler) sessionFor(p jam.Participant, id string) (jam.Instance, string, bool) {
	inst, ok := h.d.Store.GetInstance(id)
	if !ok || inst.Phase == jam.PhaseGone || h.d.Intercom == nil {
		return jam.Instance{}, "", false
	}
	home, ok := h.d.Intercom.HomeChannelIfExists(inst)
	if !ok || !h.d.Intercom.CanSee(p.UserID, home) {
		return jam.Instance{}, "", false
	}
	return inst, string(home.ID), true
}

// headSession is the session whose header shows in conversation sel for p:
// sel's home session, when its routes resolve home to sel (a ticket
// session's home is its ticket channel, not its own session channel).
func (h *handler) headSession(p jam.Participant, sel string) (jam.Instance, bool) {
	inst, ok := homeSession(h.d, sel)
	if !ok {
		return jam.Instance{}, false
	}
	if _, home, ok := h.sessionFor(p, inst.ActorID); ok && home == sel {
		return inst, true
	}
	return jam.Instance{}, false
}

// sessionHead is inst's header for p; home is its home channel.
func (h *handler) sessionHead(r *http.Request, p jam.Participant, inst jam.Instance, home string) SessionHead {
	now := time.Now()
	pr, _ := appui.PrincipalFrom(r)
	op := pr.Admin
	hd := SessionHead{ID: inst.ActorID, Label: inst.Name, Channel: home, Kind: kindLabel(inst), Role: inst.Role, Project: jam.ProjectName(h.d.Store, inst.Project),
		Ticket: inst.Unit, OpenAsks: inst.OpenAsks, Uptime: since(now, inst.RaisedAt), Mission: inst.Mission}
	if hd.Label == "" {
		hd.Label = inst.ActorID
	}
	if inst.OwnerID != "" {
		hd.Owner = h.d.Intercom.PartyOf(inst.OwnerID).Label
	}
	if rows := sessionRows([]string{inst.ActorID}, []jam.Instance{inst}, h.presence); len(rows) == 1 {
		hd.State = rows[0]
	}
	hd.Since = since(now, stateSince(inst))
	hd.Card, _ = h.card(inst)
	hd.Color, _ = h.d.Store.SessionColor(inst.ActorID)
	if h.d.Totals != nil {
		if t, err := h.d.Totals.Of(inst.ActorID); err == nil {
			if t.Turns > 0 {
				hd.Cost, hd.TurnCost = usd(t.CostUSD), usd(t.TurnCostUSD)
				hd.Tokens = numfmt.Int(t.InputTokens) + " in / " + numfmt.Int(t.OutputTokens) + " out tokens"
			}
			hd.Context, hd.ContextTip = contextUse(t)
		}
	}
	if _, ok := collab.Decide(h.d.Store, now, collabPrincipal(r, p), inst, collab.SSH); ok {
		hd.SSH = "jam ssh " + inst.ActorID
	}
	if h.d.Ops != nil {
		hd.Ops = true
		hd.CanRelease = mayRelease(inst, p, op)
		hd.CanRestart, hd.RestartLabel = op, "Restart"
		hd.CanEnd = op && inst.SessionKind != jam.SessionKindPersonal // Release is how a personal session ends
		if inst.SessionKind == jam.SessionKindStanding {
			hd.CanUpgrade = op
			hd.UpgradeState = h.d.Ops.UpgradeState(inst)
		}
		a := jam.CommandActivity(inst)
		hd.CanInterrupt = jam.CommandAllowed(jam.CmdInterrupt, a)
		hd.CanCompact = jam.CommandAllowed(jam.CmdCompact, a)
		hd.CanClear = jam.CommandAllowed(jam.CmdClear, a)
	}
	return hd
}

func mayRelease(inst jam.Instance, p jam.Participant, op bool) bool {
	return inst.SessionKind == jam.SessionKindPersonal && (op || inst.OwnerID != "" && inst.OwnerID == p.UserID)
}

func kindLabel(inst jam.Instance) string {
	switch inst.SessionKind {
	case jam.SessionKindStanding:
		return "standing"
	case jam.SessionKindPersonal:
		return "personal"
	}
	if inst.Unit != "" {
		return "ticket"
	}
	return "session"
}

// stateSince is when inst entered its current state.
func stateSince(inst jam.Instance) time.Time {
	switch {
	case inst.Activity == jam.ActivityWaiting && !inst.WaitingSince.IsZero():
		return inst.WaitingSince
	case !inst.TurnEndedAt.IsZero() && inst.Activity != jam.ActivityRunning:
		return inst.TurnEndedAt
	}
	return inst.RaisedAt
}

// since is a short duration from t to now ("" for zero).
func since(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func usd(v float64) string { return numfmt.USD(v) }

// contextUse is how full the main thread's context is, short ("ctx 84k /
// 200k · 42%") and exact (for a tooltip); "" before the first model call.
func contextUse(t sessionevents.Totals) (short, exact string) {
	switch {
	case t.Context > 0 && t.ContextWindow > 0:
		pct := t.Context * 100 / t.ContextWindow
		return fmt.Sprintf("ctx %s / %s · %d%%", kTokens(t.Context), kTokens(t.ContextWindow), pct),
			fmt.Sprintf("context: %s of %s tokens (%s)", numfmt.Int(t.Context), numfmt.Int(t.ContextWindow), t.ContextModel)
	case t.Context > 0:
		return "ctx " + kTokens(t.Context), fmt.Sprintf("context: %s tokens (%s; window not reported yet)", numfmt.Int(t.Context), t.ContextModel)
	case !t.LastAt.IsZero() && t.ContextModel != "":
		return "ctx —", "context reset (compacted or cleared); its size shows after the next model call"
	}
	return "", ""
}

// kTokens is n tokens, rounded: 950, 84k, 1.2M.
func kTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		s := strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0") + "M"
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return strconv.FormatInt(n, 10)
}

// sessionOutcome runs action on inst for p, re-checking who may: 204, or the
// refusal's status, API code and text. Release, restart and upgrade are
// the owner's or an operator's; interrupt, compact and clear are anyone's
// who can see the session, while its activity allows (else 409).
func (h *handler) sessionOutcome(r *http.Request, p jam.Participant, inst jam.Instance, action string) (int, string, string) {
	pr, _ := appui.PrincipalFrom(r)
	op := pr.Admin
	var err error
	switch action {
	case "release":
		if !mayRelease(inst, p, op) {
			return http.StatusForbidden, "forbidden", "only the session's owner (or an operator) may release it"
		}
		err = h.d.Ops.Teardown(r.Context(), inst.ActorID)
	case "restart":
		if !op {
			return http.StatusForbidden, "forbidden", "operators only"
		}
		err = h.d.Ops.Restart(r.Context(), inst.ActorID)
	case "end":
		if !op || inst.SessionKind == jam.SessionKindPersonal {
			return http.StatusForbidden, "forbidden", "operators only (a personal session ends by release)"
		}
		err = h.d.Ops.Teardown(r.Context(), inst.ActorID)
	case "upgrade":
		if !op || inst.SessionKind != jam.SessionKindStanding {
			return http.StatusForbidden, "forbidden", "operators only, standing sessions"
		}
		_, err = h.d.Ops.Upgrade(inst)
	case string(jam.CmdInterrupt), string(jam.CmdCompact), string(jam.CmdClear):
		c := jam.SessionCommand(action)
		if a := jam.CommandActivity(inst); !jam.CommandAllowed(c, a) {
			return http.StatusConflict, "conflict", string(c) + " is not available while the session is " + string(a)
		}
		by := h.d.Intercom.PartyOf(p.UserID).Label
		if by == "" {
			by = string(p.UserID)
		}
		err = h.d.Ops.Command(r.Context(), inst, c, by)
	default:
		return http.StatusNotFound, "not_found", "no such session action"
	}
	if err != nil {
		h.lg.Error("session action failed", "session", inst.ActorID, "action", action, "error", err.Error())
		return http.StatusBadGateway, "action_failed", "the " + action + " failed; see the jam log"
	}
	h.lg.Info("session action", "session", inst.ActorID, "action", action, "by", string(p.UserID))
	return http.StatusNoContent, "", ""
}
