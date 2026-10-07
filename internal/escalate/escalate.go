// Package escalate is Jam's resident escalation engine: while a session is
// Waiting and has asked for a person, it calls ordered tiers of people from
// its Project's escalation policy into the session's home channel on
// per-tier timers, advancing to the next tier on timeout (intercom slice 4:
// escalation as call-in). It reads no
// comments, wakes no coves, and tears nothing down — reply-detection, waking, and
// max-wait teardown stay in internal/wakeon. The two engines share only the
// Instance.Activity==Waiting gate. Wired from cmd/at-jam; not imported by
// internal/jam core.
package escalate

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

type Registry interface{ ListInstances() []jam.Instance }
type Projects interface {
	GetProject(name string) (jam.Project, bool)
	// Members lists a project's members (their @-handles name them on the
	// tracker).
	Members(project string) []jam.Member
}
type State interface {
	SetEscalation(actorID string, tier int, at time.Time) error
}

// Caller calls a tier's people into a session's home channel and posts the
// escalation notice there (jam.Intercom.Escalate); the relays deliver it.
type Caller interface {
	Escalate(ctx context.Context, inst jam.Instance, tier int, category string, members []jam.Member) error
}

type Config struct{ PollInterval time.Duration }

const defaultPollInterval = 30 * time.Second

type Engine struct {
	reg   Registry
	proj  Projects
	state State
	call  Caller
	cfg   Config
	now   func() time.Time
	log   *slog.Logger
}

func New(reg Registry, proj Projects, state State, call Caller, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{reg, proj, state, call, cfg, time.Now, log}
}

func (e *Engine) Run(ctx context.Context) {
	e.tick(ctx)
	tk := time.NewTicker(e.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, inst := range e.reg.ListInstances() {
		if inst.Activity != jam.ActivityWaiting || inst.EndRequested != nil {
			continue // a session that asked to end is not soliciting anyone
		}
		if !askedForPerson(inst) {
			continue // every turn ends in Waiting: only an ask opens an escalation
		}
		proj, ok := e.proj.GetProject(inst.Project)
		if !ok {
			continue
		}
		chain := chainFor(proj, inst.EscalationCategory)
		if len(chain) == 0 {
			continue // neither a category chain nor a default configured
		}
		// Open tier 0 iff no escalation is open (TierPingedAt zero — NOT the int,
		// whose zero value would read as "tier 0 pinged").
		if inst.TierPingedAt.IsZero() {
			e.pingTier(ctx, inst, proj, chain, 0)
			continue
		}
		cur := inst.EscalationTier
		if cur+1 < len(chain) && e.now().Sub(inst.TierPingedAt) > chain[cur].Timeout {
			e.pingTier(ctx, inst, proj, chain, cur+1)
		}
	}
}

// askedForPerson reports whether a Waiting session asked for a person: a
// needs-input ticket report, or the escalate tool since it was last woken.
func askedForPerson(inst jam.Instance) bool {
	return inst.EscalationAsked || inst.Report != nil && inst.Report.State == jam.ReportNeedsInput
}

// chainFor picks the tier chain for a cove's declared category, falling back to
// the Project's default chain for an unset/unknown/empty-configured category.
func chainFor(proj jam.Project, category string) []jam.EscalationTier {
	if c, ok := proj.EscalationByCategory[category]; ok && len(c) > 0 {
		return c
	}
	return proj.Escalation
}

// pingTier calls the tier's people into the session's home channel (with
// the escalation notice) and records the advance. A tier with no resolvable
// members calls no one but still advances the timer (so a mis-configured
// tier can't wedge a blocked session); a failed call is retried next tick.
func (e *Engine) pingTier(ctx context.Context, inst jam.Instance, proj jam.Project, chain []jam.EscalationTier, tier int) {
	members := e.resolveMembers(e.proj.Members(inst.Project), chain, tier)
	if len(members) > 0 {
		if err := e.call.Escalate(ctx, inst, tier, inst.EscalationCategory, members); err != nil {
			e.log.Warn("escalate: call-in failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
			return // retry next tick; do NOT advance
		}
		e.log.Info("escalate: called in tier", "actor", inst.ActorID, "tier", tier, "targets", len(members))
	} else {
		e.log.Warn("escalate: tier has no resolvable people, advancing", "actor", inst.ActorID, "tier", tier)
	}
	if err := e.state.SetEscalation(inst.ActorID, tier, e.now()); err != nil {
		e.log.Warn("escalate: set state failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
	}
}

// resolveMembers resolves a tier's user:<name|usr_id> targets (human: is the
// pre-registry alias) to the project's members; anything else is skipped
// with a warning.
func (e *Engine) resolveMembers(members []jam.Member, chain []jam.EscalationTier, tier int) []jam.Member {
	var out []jam.Member
	for _, target := range chain[tier].Targets {
		kind, ref, ok := strings.Cut(target, ":")
		if !ok || (kind != "user" && kind != "human") {
			e.log.Warn("escalate: skipping non-user tier target", "target", target)
			continue
		}
		found := false
		for _, m := range members {
			if m.User.Name == ref || string(m.User.ID) == ref {
				out = append(out, m)
				found = true
				break
			}
		}
		if !found {
			e.log.Warn("escalate: user target not a project member, skipping", "target", target)
		}
	}
	return out
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
