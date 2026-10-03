package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

const defaultGrace = 10 * time.Second

// defaultMaxWait bounds how long a Waiting unit blocks for a Wake before Run
// gives up and reports Done.
const defaultMaxWait = 30 * time.Minute

// defaultBackgroundWait bounds how long an episode holds stdin open after the
// agent's turn ended while background tasks are still running.
const defaultBackgroundWait = 30 * time.Minute

// mcpConfigPath is the baked-in MCP config (internal/assemble/hardening/
// image-files/etc/claude-code/mcp.json) that gives claude -p the Jam
// messaging tools. --strict-mcp-config keeps claude from also picking up any
// project/user-level MCP config.
const mcpConfigPath = "/etc/claude-code/mcp.json"

// resumePrompt is the stdin message a Wake delivers: written into a live
// episode, or as the first message of a new --continue episode once a Wake has
// broken the unit out of a needs-input wait.
const resumePrompt = "New input may have arrived on your ticket — use the messaging `read` tool to fetch it, then continue the task. When finished, write .at-task/worker-result.json as before."

// residentResumePrompt is resumePrompt's resident-mode (personal or standing
// session) counterpart, delivered the same way when a Wake — typically the
// owner replying — arrives.
const residentResumePrompt = "Your owner may have replied — use the intercom `read` tool to fetch new messages, then continue. " +
	"Use `send` to message your owner when you have results or need input."

// standingResumePrompt is a standing session's wake text: it has no owner and
// no default recipient.
const standingResumePrompt = "A message may have arrived — use the intercom `read` tool to fetch new messages, then continue. Pass `to` when you `send`."

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent + dir whose .at-task/worker-result.json is read
	Prompt  string        // the full prompt, written as the first stream-json message on claude's stdin
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	MaxWait time.Duration // how long a needs-input turn waits for a Wake; default 30m
	// BackgroundWait bounds how long stdin stays open after a turn ends with
	// background tasks outstanding; then stdin is closed and claude stops them.
	// Default 30m.
	BackgroundWait time.Duration
	Spawner        Spawner // nil → the real execSpawner
	// MCPConfigPath is the --mcp-config file passed to claude; empty defaults to
	// mcpConfigPath. Run refuses to start the agent if it is missing/unreadable
	// (COV-190) so a stale image never yields a silently toolless agent.
	MCPConfigPath string
	// Connector, when set, refreshes the agent's connector before every spawn
	// (episode — not per prompt: a Wake written into a live episode keeps that
	// episode's env) (GET /connector) and reports the applied fingerprint; nil inherits
	// cove-master's env unchanged (an older launcher).
	Connector *ConnectorConfig
	// Resident keeps the cove alive between episodes (personal and standing
	// sessions): after every episode, whatever its outcome, Run reports Waiting
	// and blocks until a Wake (a new episode with --continue) or shutdown —
	// never MaxWait.
	Resident bool
	// StreamLogPath is the VM-local file claude's stdout (stream-json) is
	// appended to; empty defaults to defaultStreamLogPath. It is deliberately
	// not cove-master's stdout, which Jam reads into its own log.
	StreamLogPath string
	// Context is the compiled session context; nil (an older launcher) runs
	// claude without it. Written under ContextDir once at Run start; its core is
	// passed with --append-system-prompt-file on every turn.
	Context *sessionctx.Bundle
	// ContextDir is where Context is written; empty defaults to sessionctx.Dir.
	ContextDir string
	// ContextSource, when set (with Context), re-fetches the bundle before every
	// later episode and every wake written into a live one; nil = the raise-time
	// bundle for the whole run (an older launcher or Jam).
	ContextSource ContextSource
	// SessionKind is "ephemeral" | "personal" | "standing" ("" = ephemeral); it
	// picks the resume prompt.
	SessionKind string
}

const defaultStreamLogPath = "/agent-data/agent-stream.jsonl"

// defaultContextDir is where a session context is written when Config leaves
// ContextDir empty. A var so tests can point it away from the real /agent-data.
var defaultContextDir = sessionctx.Dir

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
	// contextCore is the written CORE.md path; "" = no context in effect.
	contextCore string
	// ctxr refreshes the context; nil = no live refresh.
	ctxr *contextRefresher
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
	if cfg.BackgroundWait <= 0 {
		cfg.BackgroundWait = defaultBackgroundWait
	}
	if cfg.MCPConfigPath == "" {
		cfg.MCPConfigPath = mcpConfigPath
	}
	if cfg.ContextDir == "" {
		cfg.ContextDir = defaultContextDir
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

// claudeArgs builds claude's argv for one episode. continued prepends
// --continue, used for every episode after a resume-on-wake. The prompt is
// not in argv: it is the first stream-json message on stdin.
func (w *Workload) claudeArgs(continued bool) []string {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	args = append(args, "--dangerously-skip-permissions", "--mcp-config", w.cfg.MCPConfigPath, "--strict-mcp-config")
	if w.contextCore != "" {
		// snapshot off: the default replays the first turn's system prompt on
		// every --continue, which would hide context updates.
		args = append(args, "--append-system-prompt-file", w.contextCore, "--system-prompt-snapshot", "off")
	}
	return args
}

// userMessage encodes text as one stream-json stdin line.
func userMessage(text string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message msg    `json:"message"`
	}{"user", msg{"user", text}})
	return append(b, '\n')
}

// resumeText is the prompt a Wake delivers.
func (w *Workload) resumeText() string {
	switch {
	case w.cfg.SessionKind == "standing":
		return standingResumePrompt
	case w.cfg.Resident:
		return residentResumePrompt
	}
	return resumePrompt
}

// Run runs the agent as a sequence of episodes. An episode is one claude
// process fed stream-json on stdin: the prompt first, then one coalesced
// resume prompt per batch of Wakes. Stdin is closed only when the agent's turn
// has ended and no background task is outstanding (or BackgroundWait elapsed),
// so claude's backgrounding works. After the process exits, its worker-result
// maps to Activity as before: a needs-input episode reports Waiting and blocks
// until a Wake resumes it (a new episode with --continue) or MaxWait elapses
// (Run then returns nil, ending the unit). Returning nil or an error both lead
// the client to report Done; a nil error means the unit ended cleanly
// (completed, or gave up waiting). In resident mode (personal sessions) every
// episode ends in Waiting and only a Wake or ctx cancel moves the loop on.
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
	if w.cfg.Context == nil {
		clearContext(w.cfg.ContextDir)
	} else {
		if err := writeContext(w.cfg.ContextDir, *w.cfg.Context); err != nil {
			// claude hard-fails on a missing --append-system-prompt-file, so run
			// without the context rather than not at all.
			w.log.Warn("agentrun: session context not written; running without it", "dir", w.cfg.ContextDir, "err", err.Error())
			clearContext(w.cfg.ContextDir)
		} else {
			w.contextCore = filepath.Join(w.cfg.ContextDir, "CORE.md")
			w.log.Info("agentrun: session context applied", "fingerprint", short(w.cfg.Context.Fingerprint))
			if w.cfg.ContextSource != nil {
				w.ctxr = newContextRefresher(w.cfg.ContextSource, *w.cfg.Context, w.cfg.ContextDir, w.log)
			}
		}
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
		args := w.claudeArgs(continued)
		turn++
		t := turn
		split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(t, line, dropped) }}
		tr := newIdleTracker(func(msg string, a ...any) { w.log.Warn(msg, a...) })
		trSplit := &lineSplitter{max: trackerMaxLine, emit: func(line []byte, dropped uint64) {
			if dropped > 0 {
				w.log.Warn("agentrun: stdout line over the idle tracker cap ignored", "dropped", dropped)
				return
			}
			tr.Observe(line)
		}}
		sinks := []io.Writer{split, trSplit}
		if out != nil {
			sinks = append([]io.Writer{out}, sinks...)
		}
		sink := io.MultiWriter(sinks...)
		var env []string
		if w.conn != nil {
			var fp string
			var changed bool
			env, fp, changed = w.conn.prepare(ctx)
			if changed {
				h.ConnectorApplied(fp)
			}
		}
		first := prompt
		if w.ctxr != nil && turn > 1 {
			// The bundle was just written at Run start for episode 1; later
			// episodes start with a current system prompt.
			first += contextNotice(w.ctxr.episode(ctx), false)
		}
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir, env, sink)
		if err != nil {
			return fmt.Errorf("agentrun: start claude: %w", err)
		}
		h.Report(covemaster.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)

		waitErr := w.episode(ctx, proc, tr, first)
		split.Flush()
		if tr.WakeOwed() {
			// Coalesced mid-turn but never delivered, or delivered but the process
			// exited (or the write failed) before claude started the turn it asked
			// for: hand it to the post-exit wait so it resumes at once.
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
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
				prompt, continued = w.resumeText(), true
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

// episode drives one claude process: writes prompt, then reacts to tracker
// changes, Wakes, the background-wait timer and exit until the process exits.
// It returns the process's exit error.
func (w *Workload) episode(ctx context.Context, proc Process, tr *idleTracker, prompt string) error {
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	in := proc.Input()
	open := true
	closeInput := func(why string) {
		if !open {
			return
		}
		open = false
		w.log.Info("agentrun: closing agent stdin", "why", why)
		_ = in.Close()
	}
	write := func(text string) {
		if _, err := in.Write(userMessage(text)); err != nil {
			// The process is gone (EPIPE); its exit reports the real outcome.
			w.log.Warn("agentrun: write to agent stdin failed; awaiting exit", "err", err.Error())
			closeInput("stdin write failed")
		}
	}
	var hold *time.Timer
	var holdC <-chan time.Time
	stopHold := func() {
		if hold != nil {
			hold.Stop()
			hold, holdC = nil, nil
		}
	}
	defer stopHold()

	write(prompt) // the tracker starts busy
	// resume is what a Wake writes into this live episode: the resume prompt,
	// plus a notice when the refreshed context changed (once per write, so a
	// burst of coalesced wakes carries it once).
	resume := func() string {
		text := w.resumeText()
		if w.ctxr != nil {
			text += contextNotice(w.ctxr.live(ctx), true)
		}
		return text
	}
	for {
		var wake <-chan struct{}
		if open {
			wake = w.wake // once stdin is closed, Wakes stay buffered for the post-exit wait
		}
		select {
		case err := <-exited:
			return err
		case <-ctx.Done():
			return <-exited // CommandContext SIGTERM/SIGKILLs the process
		case <-wake:
			if tr.Wake() {
				write(resume())
				tr.Wrote()
			}
		case <-tr.changed:
		case <-holdC:
			_, tasks := tr.Next()
			w.log.Warn("agentrun: background-wait elapsed with tasks outstanding; closing stdin (claude stops them)",
				"wait", w.cfg.BackgroundWait.String(), "tasks", tasks)
			holdC = nil
			closeInput("background-wait elapsed")
		}
		if !open {
			continue
		}
		switch act, tasks := tr.Next(); act {
		case actDeliverWake:
			stopHold()
			write(resume())
		case actHold:
			if hold == nil {
				w.log.Info("agentrun: turn ended with background tasks outstanding; holding stdin open", "tasks", tasks)
				hold = time.NewTimer(w.cfg.BackgroundWait)
				holdC = hold.C
			}
		case actClose:
			stopHold()
			closeInput("idle")
		case actWait:
			stopHold()
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
// stays log-only.
// Wake delivers a non-blocking signal on the wake channel. While an episode
// is live, the episode loop consumes it (delivered now between turns, or
// coalesced into one resume prompt at turn end); after the process exited, a
// Run blocked on a needs-input wait consumes it and resumes. A wake arriving
// with one already buffered is dropped — a resumed turn re-reads its inbox.
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
