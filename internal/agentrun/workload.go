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

// mcpConfigPath is the baked-in MCP config (internal/assemble/hardening/
// image-files/etc/claude-code/mcp.json) that gives claude -p the harbor
// messaging tools. --strict-mcp-config keeps claude from also picking up any
// project/user-level MCP config.
const mcpConfigPath = "/etc/claude-code/mcp.json"

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent + dir whose .at-task/worker-result.json is read
	Prompt  string        // the full prompt passed as claude's positional arg
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	Spawner Spawner       // nil → the real execSpawner
}

// Workload runs the claude agent as a one-shot and maps its lifecycle onto the
// covemaster Activity stream. It implements covemaster.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
}

// New builds a Workload. A nil Spawner uses the real os/exec-backed spawner; a
// non-positive Grace defaults to 10s.
func New(cfg Config, log *slog.Logger) *Workload {
	if cfg.Grace <= 0 {
		cfg.Grace = defaultGrace
	}
	sp := cfg.Spawner
	if sp == nil {
		sp = execSpawner{grace: cfg.Grace}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Workload{cfg: cfg, log: log, spawner: sp}
}

// Run spawns claude -p, reports Running, then maps the worker-result to the
// Activity stream. Returning nil or an error both lead the client to report
// Done; a nil error means the unit completed cleanly.
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error {
	args := []string{"-p", "--dangerously-skip-permissions", "--mcp-config", mcpConfigPath, "--strict-mcp-config", w.cfg.Prompt}
	proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir)
	if err != nil {
		return fmt.Errorf("agentrun: start claude: %w", err)
	}
	h.Report(covemaster.Running)
	w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir)

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
		return nil
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

// Control handles control messages. The client already cancels Run's ctx on
// Teardown (which SIGTERM/SIGKILLs the agent via execSpawner), so both cases are
// log-only for a one-shot.
func (w *Workload) Control(c covemaster.Control) {
	switch c.Kind {
	case covemaster.Teardown:
		w.log.Info("agentrun: teardown requested; run context cancelled, agent terminating")
	case covemaster.Wake:
		w.log.Info("agentrun: wake requested; no-op for a one-shot agent")
	}
}

var _ covemaster.Workload = (*Workload)(nil)
