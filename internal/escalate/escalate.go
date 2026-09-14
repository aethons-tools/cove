// Package escalate is harbor's resident escalation engine: while a managed cove
// is Waiting, it pings ordered human tiers of the cove's Project escalation
// policy on per-tier timers, advancing to the next tier on timeout. It reads no
// comments, wakes no coves, and tears nothing down — reply-detection, waking, and
// max-wait teardown stay in internal/wakeon. The two engines share only the
// Instance.Activity==Waiting gate. Wired from cmd/at-harbor; not imported by
// internal/harbor core.
package escalate

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Projects interface {
	GetProject(name string) (harbor.Project, bool)
	GetRoster(project string) (harbor.Roster, bool)
}
type State interface {
	SetEscalation(actorID string, tier int, at time.Time) error
}
type Pinger interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}

type Config struct{ PollInterval time.Duration }

const defaultPollInterval = 30 * time.Second

type Engine struct {
	reg   Registry
	proj  Projects
	state State
	ping  Pinger
	cfg   Config
	now   func() time.Time
	log   *slog.Logger
}

func New(reg Registry, proj Projects, state State, ping Pinger, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{reg, proj, state, ping, cfg, time.Now, log}
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
		if inst.Activity != harbor.ActivityWaiting {
			continue
		}
		proj, ok := e.proj.GetProject(inst.Project)
		if !ok || len(proj.Escalation) == 0 {
			continue
		}
		// Open tier 0 iff no escalation is open (TierPingedAt zero — NOT the int,
		// whose zero value would read as "tier 0 pinged").
		if inst.TierPingedAt.IsZero() {
			e.pingTier(ctx, inst, proj, 0)
			continue
		}
		cur := inst.EscalationTier
		if cur+1 < len(proj.Escalation) && e.now().Sub(inst.TierPingedAt) > proj.Escalation[cur].Timeout {
			e.pingTier(ctx, inst, proj, cur+1)
		}
	}
}

// pingTier resolves the tier's human handles from the roster, posts an @-mention
// nudge on the cove's OWN ticket, and records the advance. A tier with no
// resolvable human handles posts nothing but still advances the timer (so a
// mis-configured tier can't wedge a blocked cove).
func (e *Engine) pingTier(ctx context.Context, inst harbor.Instance, proj harbor.Project, tier int) {
	handles := e.resolveHandles(proj, tier)
	if len(handles) > 0 {
		issueID, err := e.ping.IssueByIdentifier(ctx, inst.Unit)
		if err != nil {
			e.log.Warn("escalate: resolve ticket failed", "actor", inst.ActorID, "error", err.Error())
			return // retry next tick; do NOT advance (ticket transiently unavailable)
		}
		body := strings.Join(handles, " ") + " — cove " + inst.ActorID + " needs input on " + inst.Unit + " (escalation tier " + strconv.Itoa(tier) + ")"
		if err := e.ping.PostComment(ctx, issueID, body); err != nil {
			e.log.Warn("escalate: ping failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
			return // retry next tick; do NOT advance
		}
		e.log.Info("escalate: pinged tier", "actor", inst.ActorID, "tier", tier, "targets", len(handles))
	} else {
		e.log.Warn("escalate: tier has no resolvable human targets, advancing", "actor", inst.ActorID, "tier", tier)
	}
	if err := e.state.SetEscalation(inst.ActorID, tier, e.now()); err != nil {
		e.log.Warn("escalate: set state failed", "actor", inst.ActorID, "tier", tier, "error", err.Error())
	}
}

func (e *Engine) resolveHandles(proj harbor.Project, tier int) []string {
	roster := proj.Roster
	var handles []string
	for _, target := range proj.Escalation[tier].Targets {
		kind, name, ok := strings.Cut(target, ":")
		if !ok || kind != "human" {
			e.log.Warn("escalate: skipping non-human tier target", "target", target)
			continue
		}
		for _, h := range roster.Humans {
			if h.Name == name {
				handles = append(handles, "@"+h.Handle)
				break
			}
		}
	}
	return handles
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
