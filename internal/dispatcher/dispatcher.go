// Package dispatcher is harbor's resident intake: an always-on poll loop that
// turns ready tracker tickets into managed-cove raises, bounded by a
// registry-derived concurrency cap. It lives outside internal/harbor core (it
// imports the tracker + kit + supervisor) and is wired from cmd/at-harbor.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/harbor"
)

// Raiser is the supervisor's raise entrypoint (satisfied by *harbor.Supervisor).
type Raiser interface {
	Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error)
}

// Registry reads the durable Instance registry (satisfied by harbor.Store).
type Registry interface {
	GetInstance(actorID string) (harbor.Instance, bool)
	ListInstances() []harbor.Instance
}

// Tracker is the scheduler.Tracker subset the dispatcher needs (satisfied by *linear.Client).
type Tracker interface {
	ListReady(ctx context.Context) ([]scheduler.Issue, error)
	Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error)
	Transition(ctx context.Context, issueID string, role scheduler.Role) error
}

// Config is the dispatcher's behavior configuration.
type Config struct {
	Role          string        // role raised coves get (must grant anthropic + git)
	Project       string        // optional
	MaxConcurrent int           // required, > 0 — max live Instances maintained
	PollInterval  time.Duration // default 30s if <= 0
}

const defaultPollInterval = 30 * time.Second

// resultProtocol instructs the agent to record its outcome. The task is inline
// (the brief precedes this), so unlike the dispatch-worker protocol there is no
// ".at-task/task.json" to read; output-handling (PR/push) is deferred. The
// worker-result.json schema matches internal/dispatch/worker.WorkerResult, which
// the cove's agent wrapper reads to map ok/needs-input/error onto its lifecycle.
const resultProtocol = `---
Your task is described above. Do the work in this repository: make the changes and run the project's tests.

When finished, write your result to .at-task/worker-result.json as EXACTLY ONE of:
  {"status":{"ok":{}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}
Use ok only if the change is complete and tests pass.`

type Dispatcher struct {
	tracker  Tracker
	raiser   Raiser
	registry Registry
	cfg      Config
	log      *slog.Logger
}

func New(t Tracker, r Raiser, reg Registry, cfg Config, log *slog.Logger) *Dispatcher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Dispatcher{tracker: t, raiser: r, registry: reg, cfg: cfg, log: log}
}

// Run polls until ctx is cancelled: an immediate tick, then every PollInterval
// (mirrors Supervisor.Run).
func (d *Dispatcher) Run(ctx context.Context) {
	d.tick(ctx)
	tk := time.NewTicker(d.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			d.tick(ctx)
		}
	}
}

// tick runs one poll pass: list ready → dedup → cap → claim → raise.
func (d *Dispatcher) tick(ctx context.Context) {
	issues, err := d.tracker.ListReady(ctx)
	if err != nil {
		d.log.Warn("dispatcher: list ready failed", "error", err.Error())
		return
	}
	live := d.countLive()
	for _, iss := range issues {
		if !iss.DispatchLabeled {
			continue // not tagged for dispatch — never claimed, counted, or raised
		}
		actorID := "cove-" + iss.Identifier
		if _, ok := d.registry.GetInstance(actorID); ok {
			continue // already raised (dedup)
		}
		if live >= d.cfg.MaxConcurrent {
			d.log.Info("dispatcher: at capacity, deferring", "max", d.cfg.MaxConcurrent)
			break // backpressure — wait for a slot next tick
		}
		if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleInProgress); err != nil {
			d.log.Warn("dispatcher: claim failed", "issue", iss.Identifier, "error", err.Error())
			continue
		}
		prompt, err := d.buildPrompt(ctx, iss)
		if err != nil {
			d.log.Warn("dispatcher: build prompt failed", "issue", iss.Identifier, "error", err.Error())
			d.needsInput(ctx, iss)
			continue
		}
		if _, _, _, err := d.raiser.Raise(ctx, harbor.RaiseSpec{
			ActorID: actorID, Role: d.cfg.Role, Project: d.cfg.Project, Unit: iss.Identifier, Prompt: prompt,
		}); err != nil {
			d.log.Warn("dispatcher: raise failed", "issue", iss.Identifier, "error", err.Error())
			d.needsInput(ctx, iss)
			continue
		}
		d.log.Info("dispatcher: raised cove", "issue", iss.Identifier, "actor", actorID)
		live++
	}
}

// countLive counts Instances that occupy a concurrency slot (everything not gone;
// gone Instances are already deregistered, but filter defensively).
func (d *Dispatcher) countLive() int {
	n := 0
	for _, i := range d.registry.ListInstances() {
		if i.Phase != harbor.PhaseGone {
			n++
		}
	}
	return n
}

func (d *Dispatcher) buildPrompt(ctx context.Context, iss scheduler.Issue) (string, error) {
	comments, err := d.tracker.Comments(ctx, iss.ID)
	if err != nil {
		return "", fmt.Errorf("comments: %w", err)
	}
	return scheduler.AssembleBrief(iss, comments) + "\n\n" + resultProtocol, nil
}

func (d *Dispatcher) needsInput(ctx context.Context, iss scheduler.Issue) {
	if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleNeedsInput); err != nil {
		d.log.Warn("dispatcher: move to needs-input failed", "issue", iss.Identifier, "error", err.Error())
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
