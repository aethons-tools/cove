package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/studioagent"
)

const defaultGrace = 10 * time.Second

// defaultBackgroundWait bounds how long an episode holds stdin open after the
// agent's turn ended while background tasks are still running.
const defaultBackgroundWait = 30 * time.Minute

// resumePrompt is a ticket studio's wake text: written into a live episode, or
// as the first message of a new continued episode.
const resumePrompt = "New input may have arrived on your ticket — use the intercom `read` tool to fetch it, then continue. Use `report` to update the ticket and `end` when the ticket is finished."

// residentResumePrompt is resumePrompt's resident-mode (personal or standing
// session) counterpart, delivered the same way when a Wake — typically the
// owner replying — arrives.
const residentResumePrompt = "A reply may have arrived — use the intercom `read` tool to fetch new messages, then continue. " +
	"`send` without `to` posts to your own channel, which the person who started you is in: use it when you have results or need input."

// standingResumePrompt is a standing session's wake text: it has no owner;
// people reach it in its own channel or elsewhere.
const standingResumePrompt = "A message may have arrived — use the intercom `read` tool to fetch new messages, then continue. " +
	"`send` without `to` posts to your own channel; pass `to` to answer anywhere else."

// standingRestartPrompt is the first message of a standing session's first
// episode when it resumes a prior conversation (its /agent-data survived a
// restart): it replaces the raise prompt, which the conversation already holds.
const standingRestartPrompt = "Your session was restarted (for example, Jam was upgraded or your studio was re-raised); " +
	"your conversation and workspace are intact. Use the intercom `read` tool to fetch any messages that arrived meanwhile, " +
	"then continue where you left off. `send` without `to` posts to your own channel; pass `to` to answer anywhere else. " +
	"Set your `mission` again once you know your current focus."

// resumeFailWindow bounds the resume fallback: a resumed episode that exits
// non-zero within it, the agent never having replied, is retried fresh.
const resumeFailWindow = 2 * time.Minute

// streamLogCap is the stream log size past which Run rotates it at start
// (keeping one <path>.1), so a long-lived session's log can't grow unbounded
// on its persisted /agent-data. A var so tests can shrink it.
var streamLogCap int64 = 16 << 20

// defaultConversationMarker is the file a standing session's jam-studio-agent
// writes once its agent is first seen replying (so a conversation exists): on the persisted /agent-data
// volume, so a restarted session knows there is a conversation to continue. A
// var so tests can point it away from the real /agent-data.
var defaultConversationMarker = "/agent-data/.cove-conversation"

// defaultClearMarker is the file a Clear pressed while waiting writes (its
// contents are who pressed it): on the persisted /agent-data volume, so the
// fresh start it asks for survives a jam-studio-agent restart. A var so tests can
// point it away from the real /agent-data.
var defaultClearMarker = "/agent-data/.cove-clear-pending"

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent (and its gates)
	Prompt  string        // the full prompt, written as the first message on the agent's stdin
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	MaxWait time.Duration // unused (Jam bounds waiting); kept so old configs still set it
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
	// jam-studio-agent's env unchanged (an older launcher).
	Connector *ConnectorConfig
	// Resident marks a personal or standing session; it only picks the resume
	// prompt now — every session waits after every episode.
	Resident bool
	// StreamLogPath is the VM-local file the agent's stdout is
	// appended to; empty defaults to defaultStreamLogPath. It is deliberately
	// not jam-studio-agent's stdout, which Jam reads into its own log.
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
	// ConversationMarker is the standing-session conversation marker's path;
	// empty defaults to defaultConversationMarker. Only a "standing" session
	// reads or writes it: present at Run start, the first episode continues
	// the prior conversation with standingRestartPrompt (falling back once to a
	// fresh start if that fails before the agent replies).
	ConversationMarker string
	// ClearMarker is the clear-pending file's path; empty defaults to
	// defaultClearMarker. Present before an episode (or at Run start), that
	// episode starts a fresh conversation, whatever the session kind, with the
	// raise prompt and clearedNotice; its first reply removes it.
	ClearMarker string
	// TurnIO, when set, turns on the structured turn end: wakes deliver the
	// session's pending messages, and each turn's final text is posted and the
	// messages it was shown committed (see turnend.go). nil = the legacy read
	// prompt; the agent posts and commits itself.
	TurnIO TurnIO
	// AgentSocket is where the agent socket is served (with TurnIO set);
	// empty defaults to AgentSocketPath.
	AgentSocket string
	// KeepDir is where kept processes' logs and registry live; empty
	// defaults to defaultKeepDir.
	KeepDir string
}

const defaultStreamLogPath = "/agent-data/agent-stream.jsonl"

// defaultContextDir is where a session context is written when Config leaves
// ContextDir empty. A var so tests can point it away from the real /agent-data.
var defaultContextDir = sessionctx.Dir

// Workload runs the agent (via its Harness) as a turn loop and maps its lifecycle onto
// the studioagent Activity stream: every turn ends in Waiting until a Wake
// arrives. It implements studioagent.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
	conn    *connectorRefresher
	wake    *wakeBox
	cmds    *cmdBox
	// interrupts numbers the interrupt control requests (request ids); Run's
	// goroutine only.
	interrupts uint64
	// gate is what a RunGate control needs from the running unit: its ctx, the
	// handle results go up on, and the agent's last spawn env (nil = inherit).
	gateMu  sync.Mutex
	gateCtx context.Context
	gateH   studioagent.Handle
	gateEnv []string
	// streamOut is the stream log every episode's stdout is appended to; nil =
	// unavailable (events only). Set once at Run start.
	streamOut io.Writer
	// contextCore is the written CORE.md path; "" = no context in effect.
	contextCore string
	// ctxr refreshes the context; nil = no live refresh.
	ctxr *contextRefresher
	// spec is the model-spec in effect (last Validated); specKey its identity,
	// compared with each delivered spec to re-validate on a change.
	spec    *modelspec.Spec
	specKey string
	// inflight is the prompt in flight (structured turn end); gen numbers
	// prompts. Run's goroutine only.
	inflight turnMeta
	gen      uint64
	// interrupted: the last episode exited with an interrupted turn not yet
	// acknowledged; its result, drained after the exit, didn't end normally.
	interrupted bool
	// directives are the current turn's turn_end call (nil = defaults), set
	// on the agent socket's goroutine.
	dirMu      sync.Mutex
	directives *Directives
	// muted: Jam was last asked not to wake the session on messages (Run's
	// goroutine only).
	muted bool
	// structuredOn mirrors structured() for the agent socket's goroutine.
	structuredOn atomic.Bool
	// keep owns the kept processes (with TurnIO set; nil otherwise); keptMu
	// serializes reporting their count to Jam.
	keep   *keeper
	keptMu sync.Mutex
	// note is a line the next delivered prompt opens with (a reply that
	// could not go where turn_end asked). Run's goroutine only.
	note string
}

// New builds a Workload. A nil Spawner uses the real os/exec-backed spawner; a
// non-positive Grace defaults to 10s.
func New(cfg Config, log *slog.Logger) *Workload {
	if cfg.Grace <= 0 {
		cfg.Grace = defaultGrace
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
	if cfg.ConversationMarker == "" {
		cfg.ConversationMarker = defaultConversationMarker
	}
	if cfg.ClearMarker == "" {
		cfg.ClearMarker = defaultClearMarker
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
	return &Workload{cfg: cfg, log: log, spawner: sp, conn: conn, wake: newWakeBox(), cmds: newCmdBox()}
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
// so the agent's backgrounding works. After the process exits, Run reports
// Waiting and blocks until a Wake resumes it (a new continued episode) or ctx
// is cancelled (Jam tearing the session down: end, idle timeout, wait-max).
// No result file is read.
func (w *Workload) Run(ctx context.Context, h studioagent.Handle) error {
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
	var keptLost []string
	if w.cfg.TurnIO != nil {
		dir := w.cfg.KeepDir
		if dir == "" {
			dir = defaultKeepDir
		}
		w.keep = newKeeper(dir, w.cfg.WorkDir, w.agentEnv, w.keptExited, w.log)
		w.keep.onChange = w.reportKept
		keptLost = w.keep.CleanupStale()
		if len(keptLost) > 0 {
			go w.reportKept() // none kept now: Jam may pause the studio again
		}
		defer w.keep.StopAll()
		path := w.cfg.AgentSocket
		if path == "" {
			path = AgentSocketPath
		}
		if stop, err := serveAgentSocket(path, w.agentSocketHandler(), w.log); err != nil {
			w.log.Warn("agentrun: agent socket not served; turn_end and keep are unavailable", "path", path, "err", err.Error())
		} else {
			defer stop()
		}
	}
	logPath := w.cfg.StreamLogPath
	if logPath == "" {
		logPath = defaultStreamLogPath
	}
	rotateStreamLog(logPath, w.log)
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		w.log.Warn("agentrun: stream log unavailable; events only", "path", logPath, "err", err.Error())
	} else {
		defer f.Close()
		w.streamOut = f
	}
	prompt := w.cfg.Prompt
	continued := false
	// A standing session's state persists across restarts (COV-249): when a
	// prior conversation exists, the first episode continues it.
	// resuming: this episode continues from the marker (the fallback below
	// applies to it, once). A Clear still pending (pressed before a restart)
	// wins over the marker: start fresh.
	persists := w.cfg.SessionKind == "standing"
	resuming := false
	if fresh, ok := w.clearedStart(""); ok {
		prompt = fresh
	} else if persists {
		if _, err := os.Stat(w.cfg.ConversationMarker); err == nil {
			// Built in the loop, once the context is refreshed: the restart
			// prompt (and its batch) depends on the turn protocol in effect.
			prompt, continued, resuming = standingRestartPrompt, true, true
			w.log.Info("agentrun: resuming the prior conversation", "marker", w.cfg.ConversationMarker)
		}
	}
	keptNotice := ""
	if len(keptLost) > 0 {
		keptNotice = "\n\n" + fmt.Sprintf(keptLostNotice, strings.Join(keptLost, ", "))
	}
	// fresh: the episode opens with the raise prompt, which shows no batch.
	fresh := !resuming
	var turn uint32
	for {
		turn++
		t := turn
		first := prompt
		if w.ctxr != nil {
			// Every episode starts on the current bundle. Episode 1 (also after a
			// jam-studio-agent restart) gets no notice: nothing came before it.
			if changed := w.ctxr.episode(ctx); turn > 1 {
				first += contextNotice(changed, false)
			}
		}
		if turn == 1 && resuming {
			first = w.restartText(ctx)
		}
		if fresh {
			// After the refresh: the turn protocol in effect decides.
			w.newTurn(turnMeta{structured: w.structured()})
			fresh = false
		}
		if turn == 1 {
			first += keptNotice
		}
		proc, tr, split, err := w.spawn(ctx, h, t, continued)
		if err != nil {
			return err
		}
		w.dropStaleInterrupt(h, t) // before Running: a press after it is this episode's
		h.Report(studioagent.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)
		started := time.Now()

		waitErr := w.episode(ctx, h, t, proc, tr, first, false)
		split.Flush()
		for _, r := range tr.TakeTurnEnds() {
			if w.interrupted {
				r.OK = false
			}
			if ctx.Err() == nil && w.finishTurn(ctx, r) {
				// The last turn ended as the process exited; the compact it
				// asked for runs as a maintenance episode before the next wake.
				w.cmds.postCompact(agentBy, "")
			}
		}
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
		if resuming {
			resuming = false
			if waitErr != nil && !tr.Replied() && time.Since(started) < resumeFailWindow {
				// --continue failed before the agent replied (e.g. no saved
				// conversation): drop the marker and start fresh, once.
				w.log.Warn("agentrun: resuming the prior conversation failed; starting fresh", "err", waitErr.Error())
				if err := os.Remove(w.cfg.ConversationMarker); err != nil && !os.IsNotExist(err) {
					w.log.Warn("agentrun: conversation marker not removed", "path", w.cfg.ConversationMarker, "err", err.Error())
				}
				// The restart's batch was never shown: nothing of it is owed.
				prompt, continued, fresh = w.cfg.Prompt+keptNotice, false, true
				continue
			}
		}

		// Every turn ends the same way, whatever the session kind: wait for a
		// Wake (a reply, an alarm, the idle timeout). Jam decides when the
		// session ends (end, idle teardown, wait-max); no result file is read.
		w.logTurn(waitErr)
		h.Report(studioagent.Waiting)
		for {
			rs, c, ok := w.awaitWake(ctx, h, turn, nil)
			if !ok {
				return ctx.Err()
			}
			if c == nil {
				prompt, continued = w.deliverText(ctx, rs), true
				if fresh, ok := w.clearedStart(prompt); ok {
					prompt, continued = fresh, false
				}
				break
			}
			if _, ok := w.clearPending(); ok {
				// The cleared conversation is not continued: nothing to compact.
				w.ignoreCommand(h, turn, "compact", c.by, "no conversation")
				continue
			}
			turn++
			if err := w.compactIdle(ctx, h, turn, *c); err != nil {
				return err
			}
		}
	}
}

// compactIdle runs a Compact pressed with no live episode as a maintenance
// episode: a continued process whose only message is /compact, bounded by
// compactTimeout. Jam isn't told (no Running, no Waiting after): the session
// stays waiting. Wakes stay buffered for the wait after it, and so do Clears.
// It returns an error only when ctx ends; any other failure is logged and Run
// goes back to waiting.
func (w *Workload) compactIdle(ctx context.Context, h studioagent.Handle, turn uint32, c compactReq) error {
	mctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()
	proc, tr, split, err := w.spawn(mctx, h, turn, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.log.Warn("agentrun: compact not run — the agent could not be started; waiting", "by", c.by, "err", err.Error())
		return nil
	}
	w.log.Info("agentrun: compacting the conversation in a maintenance episode", "by", c.by)
	w.emitCommand(h, turn, "compact", c.by, "")
	w.dropStaleInterrupt(h, turn)
	w.newTurn(turnMeta{quiet: true})
	waitErr := w.episode(mctx, h, turn, proc, tr, compactText(c.instructions), true)
	split.Flush()
	// The episode consumed any command signal without taking a Clear (or a
	// newer Compact restored on exit): hand them on to the wait.
	w.cmds.repost()
	switch {
	case ctx.Err() != nil:
		w.log.Info("agentrun: agent interrupted by context cancel", "err", ctx.Err())
		return ctx.Err()
	case mctx.Err() != nil:
		w.log.Warn("agentrun: compact timed out; maintenance episode stopped, waiting", "by", c.by, "timeout", compactTimeout.String())
	case waitErr != nil:
		w.log.Warn("agentrun: compact FAILED — maintenance episode exited non-zero; waiting", "by", c.by, "exit", waitErr.Error())
	default:
		w.log.Info("agentrun: compacted; waiting", "by", c.by)
	}
	return nil
}

// spawn starts one agent process for event turn turn: it refreshes the
// connector (re-validating a changed model-spec), rewrites the harness files
// and spawns the harness command, its stdout teed to the stream log, the
// session event stream and a fresh idle tracker. The splitter must be flushed
// once the process exits.
func (w *Workload) spawn(ctx context.Context, h studioagent.Handle, turn uint32, continued bool) (Process, *idleTracker, *lineSplitter, error) {
	split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(turn, line, dropped) }}
	tr := newIdleTracker(w.cfg.Harness.ParseEvent, func(msg string, a ...any) { w.log.Warn(msg, a...) })
	// A reply means a conversation exists: a standing session's to continue
	// after a restart, and (fresh) the one a pending Clear asked for.
	var onReply []func()
	if w.cfg.SessionKind == "standing" {
		onReply = append(onReply, w.markConversation)
	}
	if !continued {
		onReply = append(onReply, w.consumeClear)
	}
	if len(onReply) > 0 {
		tr.onReply = func() {
			for _, f := range onReply {
				f()
			}
		}
	}
	trSplit := &lineSplitter{max: trackerMaxLine, emit: func(line []byte, dropped uint64) {
		if dropped > 0 {
			w.log.Warn("agentrun: stdout line over the idle tracker cap ignored", "dropped", dropped)
			return
		}
		tr.Observe(line)
	}}
	sinks := []io.Writer{split, trSplit}
	if w.streamOut != nil {
		sinks = append([]io.Writer{w.streamOut}, sinks...)
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
			return nil, nil, nil, err
		}
		if changed {
			h.ConnectorApplied(fp)
		}
	}
	// The files the argv names are rewritten every episode, not only on a
	// spec change: the agent may have deleted them (a missing --settings
	// file fails every later claude at startup).
	if err := w.cfg.Harness.Prepare(w.spec); err != nil {
		w.log.Error("agentrun: harness files could not be written — refusing to start the agent", "err", err.Error())
		return nil, nil, nil, err
	}
	bin, args, extra := w.cfg.Harness.Command(Episode{Continued: continued, ContextCore: w.contextCore, Spec: w.spec})
	env = w.overlayEnv(env, extra)
	w.gateMu.Lock()
	w.gateEnv = env // gates run with the agent's env
	w.gateMu.Unlock()
	proc, err := w.spawner.Spawn(ctx, bin, args, w.cfg.WorkDir, env, sink)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agentrun: start %s: %w", bin, err)
	}
	return proc, tr, split, nil
}

// markConversation records that this standing session has a conversation to
// continue after a restart. Best-effort: without it a restart starts fresh.
// Runs on the stdout goroutine (the idle tracker's onReply).
func (w *Workload) markConversation() {
	if err := os.WriteFile(w.cfg.ConversationMarker, nil, 0o600); err != nil {
		w.log.Warn("agentrun: conversation marker not written; a restart will start fresh", "path", w.cfg.ConversationMarker, "err", err.Error())
	}
}

// rotateStreamLog moves the stream log at path to <path>.1 (replacing any
// older one) when it exceeds streamLogCap. Best-effort.
func rotateStreamLog(path string, log *slog.Logger) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= streamLogCap {
		return
	}
	if err := os.Rename(path, path+".1"); err != nil {
		log.Warn("agentrun: stream log not rotated", "path", path, "err", err.Error())
		return
	}
	log.Info("agentrun: stream log rotated", "path", path, "size", fi.Size())
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
// jam-studio-agent's env), except a key the current connector sets: the connector
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
// changes, Wakes, session-header commands, the background-wait timer and exit
// until the process exits. turn is the episode's event turn. It returns the
// process's exit error.
//
// A maintenance episode's prompt is a /compact: it reads no Wakes (they stay
// buffered), takes no Clear, and closes stdin once the compaction's turn ends.
func (w *Workload) episode(ctx context.Context, h studioagent.Handle, turn uint32, proc Process, tr *idleTracker, prompt string, maintenance bool) error {
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
	writeRaw := func(b []byte) {
		if _, err := in.Write(b); err != nil {
			// The process is gone (EPIPE); its exit reports the real outcome.
			w.log.Warn("agentrun: write to agent stdin failed; awaiting exit", "err", err.Error())
			closeInput("stdin write failed")
		}
	}
	write := func(text string) { writeRaw(w.cfg.Harness.EncodeInput(text)) }
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
			h.Report(studioagent.Running)
			held = false
		}
	}

	tr.NoteWrite(w.inflight.gen)
	write(prompt) // the tracker starts busy
	// ackBy: who interrupted the turn in flight; when it ends, the acknowledge
	// turn is written instead of the usual turn-end action.
	ackBy := ""
	// compact: a Compact taken but not yet written (at the next turn
	// boundary); compacting: a written /compact's turn is in flight.
	var compact *compactReq
	compacting := maintenance
	// finish runs the structured turn end of every turn that has ended. It
	// runs before anything is written into the episode, so each prompt's
	// batch starts past what the turns before it committed.
	finish := func() {
		for _, r := range tr.TakeTurnEnds() {
			if maintenance {
				continue
			}
			if ackBy != "" {
				r.OK = false // the interrupted turn: whatever claude called it
			}
			if w.finishTurn(ctx, r) && !compacting && compact == nil {
				compact = &compactReq{by: agentBy}
			}
		}
	}
	// resume is what a Wake writes into this live episode: the resume prompt,
	// plus a notice when the refreshed context changed (once per write, so a
	// burst of coalesced wakes carries it once).
	resume := func() string {
		finish()
		text := w.deliverText(ctx, w.wake.take())
		tr.NoteWrite(w.inflight.gen)
		if w.ctxr != nil {
			text += contextNotice(w.ctxr.live(ctx), true)
		}
		return text
	}
	defer func() {
		if compact != nil {
			// The process exited before the turn boundary: the press is not lost.
			w.cmds.restoreCompact(*compact)
		}
		w.interrupted = ackBy != ""
	}()
	for {
		var wake, cmds <-chan struct{}
		if open {
			if !maintenance {
				wake = w.wake.signal() // once stdin is closed, Wakes stay buffered for the post-exit wait
			}
			cmds = w.cmds.signal() // and commands for awaitWake
		}
		select {
		case err := <-exited:
			return err
		case <-ctx.Done():
			return <-exited // CommandContext SIGTERM/SIGKILLs the process
		case <-wake:
			switch {
			case !w.wake.has():
				// A signal whose reasons an earlier prompt already took is stale.
			case ackBy != "":
				// The acknowledge turn answers it: only record it as pending
				// (so an exit before the ack keeps it owed), never deliver now.
				tr.Wake()
			case tr.Wake():
				// Marked before the write: a fast turn end must not be seen first.
				tr.Wrote()
				write(resume())
				unhold()
			}
		case <-cmds:
			if by, ok := w.cmds.takeInterrupt(); ok {
				switch {
				case !tr.Busy():
					w.ignoreCommand(h, turn, "interrupt", by, "not in a turn")
				case ackBy != "":
					w.ignoreCommand(h, turn, "interrupt", by, "already interrupting")
				case compacting:
					// The compaction finishes and its turn ends anyway.
					w.ignoreCommand(h, turn, "interrupt", by, "compacting")
				default:
					w.interrupts++
					w.log.Info("agentrun: interrupting the agent's turn", "by", by)
					w.emitCommand(h, turn, "interrupt", by, "")
					writeRaw(w.cfg.Harness.EncodeInterrupt(fmt.Sprintf("cove-int-%d", w.interrupts)))
					ackBy = by
				}
			}
			if by, instructions, ok := w.cmds.takeCompact(); ok {
				if compacting {
					w.ignoreCommand(h, turn, "compact", by, "already compacting")
				} else {
					compact = &compactReq{by: by, instructions: instructions} // the last press wins
				}
			}
			if !maintenance {
				// Clear is for a waiting session only; a maintenance episode
				// leaves it for the wait after it.
				if by, ok := w.cmds.takeClear(); ok {
					w.ignoreCommand(h, turn, "clear", by, "live episode")
				}
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
		finish()
		if !open {
			continue
		}
		if ackBy != "" && !tr.Busy() {
			// The interrupted turn ended: one acknowledge turn, which also
			// answers any wakes coalesced into the interrupted one.
			stopHold()
			finish() // the interrupted turn, while ackBy still marks it
			text := w.ackText(ctx, ackBy, w.wake.take())
			tr.NoteWrite(w.inflight.gen)
			if w.ctxr != nil {
				text += contextNotice(w.ctxr.live(ctx), true)
			}
			tr.Deliver() // before the write, as above
			write(text)
			unhold()
			ackBy = ""
			continue
		}
		if compacting {
			if tr.Busy() {
				// Nothing to do until the compaction's turn ends; a hold stays
				// reported (its turn is ours, not the agent's).
				continue
			}
			compacting = false
			if maintenance {
				closeInput("compacted")
				continue
			}
			// Carry on with the usual turn-end action.
		}
		if compact != nil && !tr.Busy() {
			// At the turn boundary (or now, between turns): compact before any
			// coalesced wake is delivered, so its resume lands in the new context.
			// A hold's timer restarts after the compaction; Holding stays reported.
			stopHold()
			w.log.Info("agentrun: compacting the conversation", "by", compact.by)
			tr.MarkBusy() // before the write, as above
			finish()
			w.newTurn(turnMeta{quiet: true})
			tr.NoteWrite(w.inflight.gen)
			write(compactText(compact.instructions))
			compacting = true
			w.emitCommand(h, turn, "compact", compact.by, "")
			compact = nil
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
				if !held { // a hold resumed after a compaction is still reported
					h.Report(studioagent.Holding)
					held = true
				}
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

// awaitWake blocks until a Wake with reasons still to answer (rs) or a Compact
// (compact) arrives, skipping stale signals. A Clear is handled here (see
// takeClear). turn is the last episode's event turn, for command records. ok
// is false when ctx ends or timeout (nil = none) fires.
func (w *Workload) awaitWake(ctx context.Context, h studioagent.Handle, turn uint32, timeout <-chan time.Time) (rs []studioagent.WakeReason, compact *compactReq, ok bool) {
	for {
		select {
		case <-w.wake.signal():
			if rs = w.wake.take(); len(rs) > 0 {
				// A Clear pressed alongside still applies to the episode this
				// Wake starts.
				w.takeClear(h, turn)
				return rs, nil, true
			}
		case <-w.cmds.signal():
			if by, ok := w.cmds.takeInterrupt(); ok {
				w.ignoreCommand(h, turn, "interrupt", by, "no live episode")
			}
			w.takeClear(h, turn)
			if by, instructions, ok := w.cmds.takeCompact(); ok {
				return nil, &compactReq{by: by, instructions: instructions}, true
			}
		case <-ctx.Done():
			return nil, nil, false
		case <-timeout:
			return nil, nil, false
		}
	}
}

// logTurn logs how a turn's agent process ended. A non-zero exit is a FAILED
// turn — the agent crashed or errored (auth/model-not-accessible/…) — and must
// be loud, not mistaken for a healthy wait; the session still waits, and Jam
// ends it (an unfinished ticket is then marked blocked). The cause is in the
// agent's own log (jam-studio-agent.log).
func (w *Workload) logTurn(waitErr error) {
	if waitErr != nil {
		w.log.Warn("agentrun: turn FAILED — agent exited non-zero; waiting (cause is in the agent log)", "exit", waitErr.Error())
		return
	}
	w.log.Info("agentrun: turn ended; waiting")
}

// Control handles control messages. The client already cancels Run's ctx on
// Teardown (which SIGTERM/SIGKILLs the agent via execSpawner), so that case
// stays log-only.
// Wake posts its reasons to the wake box and signals. While an episode is
// live, the episode loop consumes it (delivered now between turns, or
// coalesced into one resume prompt at turn end); after the process exited, a
// Run waiting after an episode consumes it and resumes. Coalesced wakes
// merge their reasons: the one resume prompt names them all (renderWake).
// Interrupt, Compact and Clear post to the command box the same way, never
// blocking: the episode loop acts on them, or awaitWake does (a Compact runs
// a maintenance episode, a Clear writes the clear marker, an Interrupt is
// dropped).
func (w *Workload) Control(c studioagent.Control) {
	switch c.Kind {
	case studioagent.Teardown:
		w.log.Info("agentrun: teardown requested; run context cancelled, agent terminating")
	case studioagent.RunGate:
		w.startGate(c.Gate)
	case studioagent.Wake:
		w.log.Info("agentrun: wake requested", "reasons", len(c.Reasons))
		w.wake.post(c.Reasons)
	case studioagent.Interrupt:
		w.log.Info("agentrun: interrupt requested", "by", c.By)
		w.cmds.postInterrupt(c.By)
	case studioagent.Compact:
		w.log.Info("agentrun: compact requested", "by", c.By)
		w.cmds.postCompact(c.By, c.Instructions)
	case studioagent.Clear:
		w.log.Info("agentrun: clear requested", "by", c.By)
		w.cmds.postClear(c.By)
	}
}

// takeClear takes a Clear pressed while waiting and writes the clear marker
// (its contents: who pressed it), so the next episode starts fresh, even
// after a restart. No turn starts. Run's goroutine only.
func (w *Workload) takeClear(h studioagent.Handle, turn uint32) {
	by, ok := w.cmds.takeClear()
	if !ok {
		return
	}
	w.emitCommand(h, turn, "clear", by, "")
	if err := os.WriteFile(w.cfg.ClearMarker, []byte(by), 0o600); err != nil {
		w.log.Warn("agentrun: clear marker not written; the conversation continues", "by", by, "path", w.cfg.ClearMarker, "err", err.Error())
		return
	}
	w.log.Info("agentrun: conversation cleared; the next episode starts fresh", "by", by)
}

// clearPending reports whether a Clear is pending (the marker exists) and who
// pressed it: "someone" when the marker is empty or unreadable.
func (w *Workload) clearPending() (by string, ok bool) {
	b, err := os.ReadFile(w.cfg.ClearMarker)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false
	case err != nil:
		w.log.Warn("agentrun: clear marker unreadable; clearing anyway", "path", w.cfg.ClearMarker, "err", err.Error())
	}
	if by = strings.TrimSpace(string(b)); by == "" {
		by = "someone"
	}
	return by, true
}

// clearedStart is a pending Clear's fresh first message: the raise prompt,
// clearedNotice, then wake (the text the episode would otherwise have opened
// with; "" = none). ok is false when no Clear is pending.
func (w *Workload) clearedStart(wake string) (prompt string, ok bool) {
	by, ok := w.clearPending()
	if !ok {
		return "", false
	}
	w.log.Info("agentrun: starting a fresh conversation after a clear", "by", by)
	prompt = w.cfg.Prompt + "\n\n" + clearedNotice(by)
	if wake != "" {
		prompt += "\n\n" + wake
	}
	return prompt, true
}

// consumeClear removes the clear marker once the fresh conversation exists
// (its first reply). Runs on the stdout goroutine (the idle tracker's
// onReply); Run reads or writes the marker only between episodes.
func (w *Workload) consumeClear() {
	if err := os.Remove(w.cfg.ClearMarker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log.Warn("agentrun: clear marker not removed; the next episode starts fresh again", "path", w.cfg.ClearMarker, "err", err.Error())
	}
}

var _ studioagent.Workload = (*Workload)(nil)

// startGate runs an alarm's gate in the background (see runGate) and reports
// its result up the handle. Before Run has started there is nowhere to report:
// the request is dropped and Jam times it out.
func (w *Workload) startGate(req *studioagent.GateRequest) {
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
		h.GateResult(studioagent.GateResult{RunID: req.RunID, Exit: exit, TimedOut: timedOut, Output: out, Truncated: truncated})
	}()
}
