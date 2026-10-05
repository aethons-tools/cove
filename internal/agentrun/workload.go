package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

const defaultGrace = 10 * time.Second

// defaultMaxWait bounds how long a Waiting unit blocks for a Wake before Run
// gives up and reports Done.
const defaultMaxWait = 30 * time.Minute

// defaultBackgroundWait bounds how long an episode holds stdin open after the
// agent's turn ended while background tasks are still running.
const defaultBackgroundWait = 30 * time.Minute

// resumePrompt is the stdin message a Wake delivers: written into a live
// episode, or as the first message of a new continued episode once a Wake has
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
	Prompt  string        // the full prompt, written as the first message on the agent's stdin
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	MaxWait time.Duration // how long a needs-input turn waits for a Wake; default 30m
	// BackgroundWait bounds how long stdin stays open after a turn ends with
	// background tasks outstanding; then stdin is closed and the agent stops them.
	// Default 30m.
	BackgroundWait time.Duration
	Spawner        Spawner // nil → the real execSpawner
	// Harness is the agent CLI driven each episode (argv, stdin encoding,
	// stdout events, pre-flight check); nil → Claude{}.
	// Run refuses to start the agent if Harness.Validate fails.
	Harness Harness
	// Connector, when set, refreshes the agent's connector before every spawn
	// (episode — not per prompt: a Wake written into a live episode keeps that
	// episode's env) (GET /connector) and reports the applied fingerprint; nil inherits
	// cove-master's env unchanged (an older launcher).
	Connector *ConnectorConfig
	// Resident keeps the cove alive between episodes (personal and standing
	// sessions): after every episode, whatever its outcome, Run reports Waiting
	// and blocks until a Wake (a new continued episode) or shutdown —
	// never MaxWait.
	Resident bool
	// StreamLogPath is the VM-local file the agent's stdout is
	// appended to; empty defaults to defaultStreamLogPath. It is deliberately
	// not cove-master's stdout, which Jam reads into its own log.
	StreamLogPath string
	// Context is the compiled session context; nil (an older launcher) runs
	// the agent without it. Written under ContextDir once at Run start; its core
	// is passed to Harness.Command on every turn.
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
	// ContextFetchTimeout bounds each context refresh fetch; 0 = 3 s.
	ContextFetchTimeout time.Duration
}

const defaultStreamLogPath = "/agent-data/agent-stream.jsonl"

// defaultContextDir is where a session context is written when Config leaves
// ContextDir empty. A var so tests can point it away from the real /agent-data.
var defaultContextDir = sessionctx.Dir

// Workload runs the agent (via its Harness) as a turn loop and maps its lifecycle onto
// the covemaster Activity stream: a needs-input turn suspends (reports
// Waiting) until a Wake arrives or MaxWait elapses. It implements
// covemaster.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
	conn    *connectorRefresher
	wake    *wakeBox
	// gate is what a RunGate control needs from the running unit: its ctx, the
	// handle results go up on, and the agent's last spawn env (nil = inherit).
	gateMu  sync.Mutex
	gateCtx context.Context
	gateH   covemaster.Handle
	gateEnv []string
	// contextCore is the written CORE.md path; "" = no context in effect.
	contextCore string
	// ctxr refreshes the context; nil = no live refresh.
	ctxr *contextRefresher
	// spec is the model-spec in effect (last Validated); specKey its identity,
	// compared with each delivered spec to re-validate on a change.
	spec    *modelspec.Spec
	specKey string
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
	if cfg.Harness == nil {
		cfg.Harness = Claude{}
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
	return &Workload{cfg: cfg, log: log, spawner: sp, conn: conn, wake: newWakeBox()}
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

// Run runs the agent as a sequence of episodes. An episode is one agent
// process fed messages on stdin: the prompt first, then one coalesced
// resume prompt per batch of Wakes. Stdin is closed only when the agent's turn
// has ended and no background task is outstanding (or BackgroundWait elapsed),
// so the agent's backgrounding works. After the process exits, its worker-result
// maps to Activity as before: a needs-input episode reports Waiting and blocks
// until a Wake resumes it (a new continued episode) or MaxWait elapses
// (Run then returns nil, ending the unit). Returning nil or an error both lead
// the client to report Done; a nil error means the unit ended cleanly
// (completed, or gave up waiting). In resident mode (personal sessions) every
// episode ends in Waiting and only a Wake or ctx cancel moves the loop on.
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error {
	w.gateMu.Lock()
	w.gateCtx, w.gateH = ctx, h
	w.gateMu.Unlock()
	// Fail loud rather than launch a broken agent (e.g. a toolless one,
	// COV-190, or the wrong CLI version for its model-spec). The raise-time
	// connector carries the spec the first episode is checked against.
	var initial *modelspec.Spec
	if w.cfg.Connector != nil {
		initial = w.cfg.Connector.Initial.ModelSpec
	}
	if err := w.applySpec(initial); err != nil {
		return err
	}
	if w.cfg.Context == nil {
		clearContext(w.cfg.ContextDir)
	} else {
		if err := writeContext(w.cfg.ContextDir, *w.cfg.Context); err != nil {
			// The harness may hard-fail on a missing context file, so run
			// without the context rather than not at all.
			w.log.Warn("agentrun: session context not written; running without it", "dir", w.cfg.ContextDir, "err", err.Error())
			clearContext(w.cfg.ContextDir)
		} else {
			w.contextCore = filepath.Join(w.cfg.ContextDir, "CORE.md")
			w.log.Info("agentrun: session context applied", "fingerprint", short(w.cfg.Context.Fingerprint))
			if w.cfg.ContextSource != nil {
				w.ctxr = newContextRefresher(w.cfg.ContextSource, *w.cfg.Context, w.cfg.ContextDir, w.log)
				w.ctxr.liveTimeout = w.cfg.ContextFetchTimeout
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
		turn++
		t := turn
		split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(t, line, dropped) }}
		tr := newIdleTracker(w.cfg.Harness.ParseEvent, func(msg string, a ...any) { w.log.Warn(msg, a...) })
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
			// A model-spec edit takes effect here, at the next episode: the
			// harness re-checks the new spec first and the run fails loud if
			// it can't satisfy it.
			if err := w.applySpec(w.conn.last.ModelSpec); err != nil {
				return err
			}
			if changed {
				h.ConnectorApplied(fp)
			}
		}
		bin, args, extra := w.cfg.Harness.Command(Episode{Continued: continued, ContextCore: w.contextCore, Spec: w.spec})
		env = w.overlayEnv(env, extra)
		w.gateMu.Lock()
		w.gateEnv = env // gates run with the agent's env
		w.gateMu.Unlock()
		first := prompt
		if w.ctxr != nil {
			// Every episode starts on the current bundle. Episode 1 (also after a
			// cove-master restart) gets no notice: nothing came before it.
			if changed := w.ctxr.episode(ctx); turn > 1 {
				first += contextNotice(changed, false)
			}
		}
		proc, err := w.spawner.Spawn(ctx, bin, args, w.cfg.WorkDir, env, sink)
		if err != nil {
			return fmt.Errorf("agentrun: start %s: %w", bin, err)
		}
		h.Report(covemaster.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)

		waitErr := w.episode(ctx, h, proc, tr, first)
		split.Flush()
		if tr.ResumeOwed() {
			// The last resume prompt was written but the agent never started the
			// turn it asked for: its reasons are owed again.
			w.wake.restore()
		}
		if tr.WakeOwed() {
			// Coalesced mid-turn but never delivered, or delivered but the process
			// exited (or the write failed) before the agent started the turn it
			// asked for: hand it to the post-exit wait so it resumes at once.
			w.wake.repost()
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
			rs, ok := w.awaitWake(ctx, nil)
			if !ok {
				return ctx.Err()
			}
			prompt, continued = renderWake(w.resumeText(), rs), true
			continue
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
			rs, ok := w.awaitWake(ctx, time.After(w.cfg.MaxWait))
			if ok {
				prompt, continued = renderWake(resumePrompt, rs), true
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.log.Info("agentrun: max-wait elapsed; ending unit")
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
}

// applySpec validates spec with the harness when it differs from the one in
// effect (and always on the first call), then makes it current. A failure is
// logged and returned: Run refuses to start the agent.
func (w *Workload) applySpec(spec *modelspec.Spec) error {
	key := specKey(spec)
	if w.specKey != "" && key == w.specKey {
		return nil
	}
	if err := w.cfg.Harness.Validate(spec); err != nil {
		w.log.Error("agentrun: harness pre-flight failed — refusing to start the agent", "model_spec", specName(spec), "err", err.Error())
		return err
	}
	if w.specKey != "" || spec != nil {
		w.log.Info("agentrun: model-spec applied", "model_spec", specName(spec), "fingerprint", short(key))
	}
	w.spec, w.specKey = spec, key
	return nil
}

// specKey identifies a spec's content (never empty, so "" means "none
// validated yet"): sha256 of its JSON, or of "null" for no spec.
func specKey(spec *modelspec.Spec) string {
	b, _ := json.Marshal(spec) // a delivered spec decoded from JSON re-encodes
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func specName(spec *modelspec.Spec) string {
	if spec == nil {
		return "(none)"
	}
	return spec.Name
}

// overlayEnv sets the harness's extra env over the spawn env (nil = inherit
// cove-master's env), except a key the current connector sets: the connector
// owns routing and identity. Logs never carry the values.
func (w *Workload) overlayEnv(env []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	owned := map[string]bool{}
	if w.conn != nil {
		for k := range w.conn.last.Expand(w.conn.cfg.BaseURL, "") {
			owned[k] = true
		}
	}
	set := map[string]bool{}
	for k := range extra {
		if !owned[k] {
			set[k] = true
		}
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !set[k] {
			out = append(out, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		out = append(out, k+"="+extra[k])
	}
	return out
}

// episode drives one agent process: writes prompt, then reacts to tracker
// changes, Wakes, the background-wait timer and exit until the process exits.
// It returns the process's exit error.
func (w *Workload) episode(ctx context.Context, h covemaster.Handle, proc Process, tr *idleTracker, prompt string) error {
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
		if _, err := in.Write(w.cfg.Harness.EncodeInput(text)); err != nil {
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
	// held: Holding was reported for the current hold; the next turn to start
	// (a delivered Wake or a background task's self-started turn) reports
	// Running again.
	held := false
	unhold := func() {
		if held {
			h.Report(covemaster.Running)
			held = false
		}
	}

	write(prompt) // the tracker starts busy
	// resume is what a Wake writes into this live episode: the resume prompt,
	// plus a notice when the refreshed context changed (once per write, so a
	// burst of coalesced wakes carries it once).
	resume := func() string {
		text := renderWake(w.resumeText(), w.wake.take())
		if w.ctxr != nil {
			text += contextNotice(w.ctxr.live(ctx), true)
		}
		return text
	}
	for {
		var wake <-chan struct{}
		if open {
			wake = w.wake.signal() // once stdin is closed, Wakes stay buffered for the post-exit wait
		}
		select {
		case err := <-exited:
			return err
		case <-ctx.Done():
			return <-exited // CommandContext SIGTERM/SIGKILLs the process
		case <-wake:
			// A signal whose reasons an earlier prompt already took is stale.
			if w.wake.has() && tr.Wake() {
				write(resume())
				tr.Wrote()
				unhold()
			}
		case <-tr.changed:
		case <-holdC:
			_, tasks := tr.Next()
			w.log.Warn("agentrun: background-wait elapsed with tasks outstanding; closing stdin (the agent stops them)",
				"wait", w.cfg.BackgroundWait.String(), "tasks", tasks)
			holdC = nil
			closeInput("background-wait elapsed")
			unhold() // the agent now stops its tasks: busy until the episode exits
		}
		if !open {
			continue
		}
		switch act, tasks := tr.Next(); act {
		case actDeliverWake:
			stopHold()
			write(resume())
			unhold()
		case actHold:
			if hold == nil {
				w.log.Info("agentrun: turn ended with background tasks outstanding; holding stdin open", "tasks", tasks)
				hold = time.NewTimer(w.cfg.BackgroundWait)
				holdC = hold.C
				h.Report(covemaster.Holding)
				held = true
			}
		case actClose:
			stopHold()
			closeInput("idle")
		case actWait:
			stopHold()
			unhold()
		}
	}
}

// awaitWake blocks until a Wake with reasons still to answer arrives, skipping
// stale signals. ok is false when ctx ends or timeout (nil = none) fires.
func (w *Workload) awaitWake(ctx context.Context, timeout <-chan time.Time) (rs []covemaster.WakeReason, ok bool) {
	for {
		select {
		case <-w.wake.signal():
			if rs = w.wake.take(); len(rs) > 0 {
				return rs, true
			}
		case <-ctx.Done():
			return nil, false
		case <-timeout:
			return nil, false
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
		// A non-zero exit with no worker-result is a FAILED turn — the agent
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
// Wake posts its reasons to the wake box and signals. While an episode is
// live, the episode loop consumes it (delivered now between turns, or
// coalesced into one resume prompt at turn end); after the process exited, a
// Run blocked on a needs-input wait consumes it and resumes. Coalesced wakes
// merge their reasons: the one resume prompt names them all (renderWake).
func (w *Workload) Control(c covemaster.Control) {
	switch c.Kind {
	case covemaster.Teardown:
		w.log.Info("agentrun: teardown requested; run context cancelled, agent terminating")
	case covemaster.RunGate:
		w.startGate(c.Gate)
	case covemaster.Wake:
		w.log.Info("agentrun: wake requested", "reasons", len(c.Reasons))
		w.wake.post(c.Reasons)
	}
}

var _ covemaster.Workload = (*Workload)(nil)

// startGate runs an alarm's gate in the background (see runGate) and reports
// its result up the handle. Before Run has started there is nowhere to report:
// the request is dropped and Jam times it out.
func (w *Workload) startGate(req *covemaster.GateRequest) {
	w.gateMu.Lock()
	ctx, h, env := w.gateCtx, w.gateH, w.gateEnv
	w.gateMu.Unlock()
	if req == nil || h == nil {
		w.log.Warn("agentrun: gate requested before the run started; dropped")
		return
	}
	w.log.Info("agentrun: running gate", "alarm", req.Alarm, "run", req.RunID)
	go func() {
		exit, timedOut, out, truncated := runGate(ctx, w.cfg.WorkDir, env, req.Command, req.Timeout)
		w.log.Info("agentrun: gate done", "alarm", req.Alarm, "run", req.RunID, "exit", exit, "timed_out", timedOut)
		h.GateResult(covemaster.GateResult{RunID: req.RunID, Exit: exit, TimedOut: timedOut, Output: out, Truncated: truncated})
	}()
}
