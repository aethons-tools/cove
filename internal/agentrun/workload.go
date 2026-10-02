package agentrun

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

const defaultGrace = 10 * time.Second

// defaultMaxWait bounds how long a Waiting unit blocks for a Wake before Run
// gives up and reports Done.
const defaultMaxWait = 30 * time.Minute

// mcpConfigPath is the baked-in MCP config (internal/assemble/hardening/
// image-files/etc/claude-code/mcp.json) that gives claude -p the Jam
// messaging tools. --strict-mcp-config keeps claude from also picking up any
// project/user-level MCP config.
const mcpConfigPath = "/etc/claude-code/mcp.json"

// resumePrompt is passed (with --continue) on every turn after the first,
// once a Wake has broken the unit out of a needs-input wait.
const resumePrompt = "New input may have arrived on your ticket — use the messaging `read` tool to fetch it, then continue the task. When finished, write .at-task/worker-result.json as before."

// residentResumePrompt is passed (with --continue) on every turn after the
// first in resident mode (a personal session), once a Wake — typically the
// owner replying — has broken the cove out of its wait.
const residentResumePrompt = "Your owner may have replied — use the intercom `read` tool to fetch new messages, then continue. " +
	"Use `send` to message your owner when you have results or need input."

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent + dir whose .at-task/worker-result.json is read
	Prompt  string        // the full prompt passed as claude's positional arg
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	MaxWait time.Duration // how long a needs-input turn waits for a Wake; default 30m
	Spawner Spawner       // nil → the real execSpawner
	// MCPConfigPath is the --mcp-config file passed to claude; empty defaults to
	// mcpConfigPath. Run refuses to start the agent if it is missing/unreadable
	// (COV-190) so a stale image never yields a silently toolless agent.
	MCPConfigPath string
	// Connector, when set, refreshes the agent's connector before every spawn
	// (GET /connector) and reports the applied fingerprint; nil inherits
	// cove-master's env unchanged (an older launcher).
	Connector *ConnectorConfig
	// Resident keeps the cove alive between turns (personal sessions): after
	// every turn, whatever its outcome, Run reports Waiting and blocks until a
	// Wake (resume with --continue) or shutdown — never MaxWait.
	Resident bool
	// StreamLogPath is the VM-local file claude's stdout (stream-json) is
	// appended to; empty defaults to defaultStreamLogPath. It is deliberately
	// not cove-master's stdout, which Jam reads into its own log.
	StreamLogPath string
}

const defaultStreamLogPath = "/agent-data/agent-stream.jsonl"

// Workload runs the claude agent as a turn loop and maps its lifecycle onto
// the covemaster Activity stream: a needs-input turn suspends (reports
// Waiting) until a Wake arrives or MaxWait elapses. It implements
// covemaster.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
	conn    *connectorRefresher
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
	if cfg.MCPConfigPath == "" {
		cfg.MCPConfigPath = mcpConfigPath
	}
	sp := cfg.Spawner
	if sp == nil {
		sp = execSpawner{grace: cfg.Grace}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var conn *connectorRefresher
	if cfg.Connector != nil {
		conn = newConnectorRefresher(*cfg.Connector, log)
	}
	return &Workload{cfg: cfg, log: log, spawner: sp, conn: conn, wake: make(chan struct{}, 1)}
}

// claudeArgs builds claude's argv for one turn. continued prepends
// --continue, used for every turn after a resume-on-wake.
func (w *Workload) claudeArgs(prompt string, continued bool) []string {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--output-format", "stream-json", "--verbose")
	return append(args, "--dangerously-skip-permissions", "--mcp-config", w.cfg.MCPConfigPath, "--strict-mcp-config", prompt)
}

// Run spawns claude -p in a turn loop and maps each turn's worker-result to
// the Activity stream. A needs-input turn reports Waiting and blocks until a
// Wake resumes it (with --continue on the resume prompt) or MaxWait elapses
// (Run then returns nil, ending the unit). Returning nil or an error both
// lead the client to report Done; a nil error means the unit ended cleanly
// (completed, or gave up waiting). In resident mode (personal sessions) every
// turn ends in Waiting and only a Wake or ctx cancel moves the loop on.
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error {
	// Fail loud if the MCP config is missing rather than launch a silently
	// toolless agent (COV-190): claude with --mcp-config pointing at a
	// nonexistent file registers no servers, so the agent would have no intercom
	// read/send tools and flail. A stale image (built before mcp.json shipped in
	// the hardening layer) is the typical cause.
	if _, err := os.Stat(w.cfg.MCPConfigPath); err != nil {
		w.log.Error("agentrun: MCP config missing — refusing to start a toolless agent", "path", w.cfg.MCPConfigPath, "err", err.Error())
		return fmt.Errorf("agentrun: MCP config %q missing or unreadable: %w", w.cfg.MCPConfigPath, err)
	}
	var out io.Writer // nil-able extra sink under the line splitter
	logPath := w.cfg.StreamLogPath
	if logPath == "" {
		logPath = defaultStreamLogPath
	}
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		w.log.Warn("agentrun: stream log unavailable; events only", "path", logPath, "err", err.Error())
	} else {
		defer f.Close()
		out = f
	}
	prompt := w.cfg.Prompt
	continued := false
	var turn uint32
	for {
		args := w.claudeArgs(prompt, continued)
		turn++
		t := turn
		split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(t, line, dropped) }}
		var sink io.Writer = split
		if out != nil {
			sink = io.MultiWriter(out, split)
		}
		var env []string
		if w.conn != nil {
			var fp string
			var changed bool
			env, fp, changed = w.conn.prepare(ctx)
			if changed {
				h.ConnectorApplied(fp)
			}
		}
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir, env, sink)
		if err != nil {
			return fmt.Errorf("agentrun: start claude: %w", err)
		}
		h.Report(covemaster.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)

		waitErr := proc.Wait()
		split.Flush()
		if ctx.Err() != nil {
			// Teardown / parent shutdown interrupted the run; the result (if any) is
			// not meaningful. The client's Done/exit path owns the ctx error.
			w.log.Info("agentrun: agent interrupted by context cancel", "err", ctx.Err())
			return ctx.Err()
		}

		if w.cfg.Resident {
			w.logResidentTurn(waitErr)
			h.Report(covemaster.Waiting)
			select {
			case <-w.wake:
				prompt, continued = residentResumePrompt, true
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
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

// logResidentTurn logs a resident turn's outcome. In resident mode no outcome
// ends the session — an ok, error, or missing worker-result is only reported —
// so the owner can reply and the cove carries on.
func (w *Workload) logResidentTurn(waitErr error) {
	wr, _, ok, rerr := worker.ReadWorkerResult(w.cfg.WorkDir)
	switch {
	case rerr != nil:
		w.log.Warn("agentrun: resident turn: unreadable worker-result; waiting for the owner", "err", rerr.Error())
	case !ok && waitErr != nil:
		// A non-zero exit with no worker-result is a FAILED turn — claude
		// crashed or errored (auth/model-not-accessible/…) before writing a
		// result. In resident mode the session still waits for the owner rather
		// than ending, but the failure must be loud, not mistaken for a healthy
		// idle wait. The cause is in the agent's own log (cove-master.log).
		w.log.Warn("agentrun: resident turn FAILED — agent exited non-zero and wrote no worker-result; waiting for the owner (cause is in the agent log)", "exit", waitErr.Error())
	case !ok:
		w.log.Info("agentrun: resident turn ended cleanly without a worker-result; waiting for the owner")
	default:
		status, serr := wr.Status.Active()
		if serr != nil {
			w.log.Warn("agentrun: resident turn: invalid worker status; waiting for the owner", "err", serr.Error())
			return
		}
		w.log.Info("agentrun: resident turn ended; waiting for the owner", "status", status)
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
