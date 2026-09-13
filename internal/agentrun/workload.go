package agentrun

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

const defaultGrace = 10 * time.Second

// defaultMaxWait bounds how long a Waiting unit blocks for a Wake before Run
// gives up and reports Done.
const defaultMaxWait = 30 * time.Minute

// mcpConfigPath is the baked-in MCP config (internal/assemble/hardening/
// image-files/etc/claude-code/mcp.json) that gives claude -p the harbor
// messaging tools. --strict-mcp-config keeps claude from also picking up any
// project/user-level MCP config.
const mcpConfigPath = "/etc/claude-code/mcp.json"

// resumePrompt is passed (with --continue) on every turn after the first,
// once a Wake has broken the unit out of a needs-input wait.
const resumePrompt = "New input may have arrived on your ticket — use the messaging `read` tool to fetch it, then continue the task. When finished, write .at-task/worker-result.json as before."

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent + dir whose .at-task/worker-result.json is read
	Prompt  string        // the full prompt passed as claude's positional arg
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	MaxWait time.Duration // how long a needs-input turn waits for a Wake; default 30m
	Spawner Spawner       // nil → the real execSpawner
}

// Workload runs the claude agent as a turn loop and maps its lifecycle onto
// the covemaster Activity stream: a needs-input turn suspends (reports
// Waiting) until a Wake arrives or MaxWait elapses. It implements
// covemaster.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
	wake    chan struct{}
}

// New builds a Workload. A nil Spawner uses the real os/exec-backed spawner; a
// non-positive Grace defaults to 10s; a non-positive MaxWait defaults to 30m.
func New(cfg Config, log *slog.Logger) *Workload {
	if cfg.Grace <= 0 {
		cfg.Grace = defaultGrace
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = defaultMaxWait
	}
	sp := cfg.Spawner
	if sp == nil {
		sp = execSpawner{grace: cfg.Grace}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Workload{cfg: cfg, log: log, spawner: sp, wake: make(chan struct{}, 1)}
}

// claudeArgs builds claude's argv for one turn. continued prepends
// --continue, used for every turn after a resume-on-wake.
func (w *Workload) claudeArgs(prompt string, continued bool) []string {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	return append(args, "--dangerously-skip-permissions", "--mcp-config", mcpConfigPath, "--strict-mcp-config", prompt)
}

// Run spawns claude -p in a turn loop and maps each turn's worker-result to
// the Activity stream. A needs-input turn reports Waiting and blocks until a
// Wake resumes it (with --continue on the resume prompt) or MaxWait elapses
// (Run then returns nil, ending the unit). Returning nil or an error both
// lead the client to report Done; a nil error means the unit ended cleanly
// (completed, or gave up waiting).
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error {
	prompt := w.cfg.Prompt
	continued := false
	for {
		args := w.claudeArgs(prompt, continued)
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir)
		if err != nil {
			return fmt.Errorf("agentrun: start claude: %w", err)
		}
		h.Report(covemaster.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)

		waitErr := proc.Wait()
		if ctx.Err() != nil {
			// Teardown / parent shutdown interrupted the run; the result (if any) is
			// not meaningful. The client's Done/exit path owns the ctx error.
			w.log.Info("agentrun: agent interrupted by context cancel", "err", ctx.Err())
			return ctx.Err()
		}

		wr, _, ok, rerr := worker.ReadWorkerResult(w.cfg.WorkDir)
		if rerr != nil {
			return fmt.Errorf("agentrun: read worker-result: %w", rerr)
		}
		if !ok {
			return fmt.Errorf("agentrun: agent wrote no worker-result (exit: %v)", waitErr)
		}
		status, serr := wr.Status.Active()
		if serr != nil {
			return fmt.Errorf("agentrun: %w", serr)
		}
		switch status {
		case "ok":
			w.log.Info("agentrun: agent completed ok")
			return nil
		case "needs-input":
			w.log.Info("agentrun: agent needs input; reporting Waiting")
			h.Report(covemaster.Waiting)
			select {
			case <-w.wake:
				prompt, continued = resumePrompt, true
				continue
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.cfg.MaxWait):
				w.log.Info("agentrun: max-wait elapsed; ending unit")
				return nil
			}
		case "error":
			msg := ""
			if wr.Status.Error != nil {
				msg = wr.Status.Error.Message
			}
			return fmt.Errorf("agentrun: agent reported error: %s", msg)
		default:
			return fmt.Errorf("agentrun: unexpected worker status %q", status)
		}
	}
}

// Control handles control messages. The client already cancels Run's ctx on
// Teardown (which SIGTERM/SIGKILLs the agent via execSpawner), so that case
// stays log-only. Wake delivers a non-blocking signal on the wake channel: a
// Run currently blocked on a needs-input wait consumes it and resumes; a wake
// arriving with no one waiting (or one already buffered) is dropped, since a
// resumed turn re-reads its ticket state regardless.
func (w *Workload) Control(c covemaster.Control) {
	switch c.Kind {
	case covemaster.Teardown:
		w.log.Info("agentrun: teardown requested; run context cancelled, agent terminating")
	case covemaster.Wake:
		w.log.Info("agentrun: wake requested")
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

var _ covemaster.Workload = (*Workload)(nil)
