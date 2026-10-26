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
	// Command sends a session-control press (interrupt, compact, clear),
	// resuming a paused session first; by is who pressed it.
	Command(ctx context.Context, inst jam.Instance, c jam.SessionCommand, by string) error
}

// homeSession is the live session whose home channel chID is: a session
// channel's session, or the session working a ticket channel's ticket.
func homeSession(d Deps, chID string) (jam.Instance, bool) {
	ch, ok := d.Store.GetChannel(ident.ID(chID))
	if !ok {
		return jam.Instance{}, false
	}
	live := func(i jam.Instance) bool { return i.Phase != jam.PhaseGone }
	switch ch.Kind {
	case jam.SourceSession:
		inst, ok := d.Store.GetInstance(ch.Key)
		return inst, ok && live(inst)
	case jam.SourceTicket:
		_, unit, found := strings.Cut(ch.Key, "/")
		if !found {
			return jam.Instance{}, false
		}
		for _, inst := range d.Store.ListInstances() {
			if inst.Unit == unit && live(inst) {
				if pr, ok := d.Store.GetProject(inst.Project); ok && pr.ID == ch.ProjectID {
					return inst, true
				}
			}
		}
	}
	return jam.Instance{}, false
}

// SessionHead is a session's header.
type SessionHead struct {
	ID, Label, Kind, Ticket, Role, Project, Owner string
	State                                         SessionRow
	Since                                         string // time in the current state
	OpenAsks                                      int
	Cost, TurnCost, Tokens                        string
	Context, ContextTip                           string // the context window's use; "" = no model call yet
	Uptime                                        string
	Mission                                       string // the agent's one-line focus; "" = none
	SSH                                           string // the copyable command; "" = not allowed
	Studio                                        bool   // the Studio tab: ssh allowed and framing on
	CanRelease, CanRestart, CanUpgrade            bool
	Ops                                           bool // the session actions are wired
	CanInterrupt, CanCompact, CanClear            bool // shown whenever Ops is set; disabled when false
	RestartLabel, UpgradeState                    string
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

func (h *handler) sessionHead(r *http.Request, p jam.Participant, inst jam.Instance) SessionHead {
	now := time.Now()
	pr, _ := appui.PrincipalFrom(r)
	op := pr.Admin
	hd := SessionHead{ID: inst.ActorID, Label: inst.Name, Kind: kindLabel(inst), Role: inst.Role, Project: jam.ProjectName(h.d.Store, inst.Project),
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
		hd.SSH = "at-jam ssh " + inst.ActorID
		hd.Studio = h.d.Frames != nil
	}
	if h.d.Ops != nil {
		hd.Ops = true
		hd.CanRelease = mayRelease(inst, p, op)
		hd.CanRestart = op
		hd.RestartLabel = "End"
		if inst.SessionKind == jam.SessionKindStanding {
			hd.RestartLabel = "Restart"
			hd.CanUpgrade = op
			hd.UpgradeState = h.d.Ops.UpgradeState(inst)
		}
		if inst.SessionKind == jam.SessionKindPersonal {
			hd.CanRestart = false // Release is how a personal session ends
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

// sessionHeadRoute renders a session's header (its refresh target).
func (h *handler) sessionHeadRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	inst, _, ok := h.sessionFor(p, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	hd := h.sessionHead(r, p, inst)
	h.render(w, "sessionhead", inboxPage{Conv: &Conversation{Head: &hd}})
}

// sessionAction runs release, restart or upgrade on a session, re-checking
// who may: 204 (a page reload once it ends, a refresh after an upgrade);
// 403; 404 alike for missing and unseen. Interrupt, compact and clear are
// anyone's who can see the session, while its activity allows (else 409).
func (h *handler) sessionAction(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	inst, _, ok := h.sessionFor(p, r.PathValue("id"))
	if !ok || h.d.Ops == nil {
		http.NotFound(w, r)
		return
	}
	pr, _ := appui.PrincipalFrom(r)
	op := pr.Admin
	var err error
	switch r.PathValue("action") {
	case "release":
		if !mayRelease(inst, p, op) {
			http.Error(w, "only the session's owner (or an operator) may release it", http.StatusForbidden)
			return
		}
		err = h.d.Ops.Teardown(r.Context(), inst.ActorID)
	case "restart":
		if !op || inst.SessionKind == jam.SessionKindPersonal {
			http.Error(w, "operators only", http.StatusForbidden)
			return
		}
		err = h.d.Ops.Teardown(r.Context(), inst.ActorID)
	case "upgrade":
		if !op || inst.SessionKind != jam.SessionKindStanding {
			http.Error(w, "operators only, standing sessions", http.StatusForbidden)
			return
		}
		_, err = h.d.Ops.Upgrade(inst)
	case string(jam.CmdInterrupt), string(jam.CmdCompact), string(jam.CmdClear):
		c := jam.SessionCommand(r.PathValue("action"))
		if a := jam.CommandActivity(inst); !jam.CommandAllowed(c, a) {
			http.Error(w, string(c)+" is not available while the session is "+string(a), http.StatusConflict)
			return
		}
		by := h.d.Intercom.PartyOf(p.UserID).Label
		if by == "" {
			by = string(p.UserID)
		}
		err = h.d.Ops.Command(r.Context(), inst, c, by)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.lg.Error("session action failed", "session", inst.ActorID, "action", r.PathValue("action"), "error", err.Error())
		http.Error(w, "the "+r.PathValue("action")+" failed; see the jam log", http.StatusBadGateway)
		return
	}
	h.lg.Info("session action", "session", inst.ActorID, "action", r.PathValue("action"), "by", string(p.UserID))
	switch r.PathValue("action") {
	case "upgrade", string(jam.CmdInterrupt), string(jam.CmdCompact), string(jam.CmdClear):
		w.Header().Set("HX-Trigger", "meChanged")
	default:
		w.Header().Set("HX-Refresh", "true") // the session is going: reload rather than refresh a head that 404s
	}
	w.WriteHeader(http.StatusNoContent)
}
