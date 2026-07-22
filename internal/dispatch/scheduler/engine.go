package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
)

// Engine polls a Tracker and dispatches ready autonomous work.
type Engine struct {
	cfg     kit.Config
	kitDir  string
	tracker Tracker
	exec    Executor
	log     *logging.Logger

	gsem chan struct{}            // global concurrency
	csem map[string]chan struct{} // per-class concurrency (nil entry = no cap)
	wg   sync.WaitGroup

	mu   sync.Mutex          // guards live
	live map[string]struct{} // issue IDs this process is actively dispatching
}

// runID mints a per-dispatch correlation id: run_<issue-identifier>_<4 hex
// chars>. It's attached to every log line for one dispatch so a failed run
// is grep-able out of interleaved concurrent dispatches.
func runID(identifier string) string {
	var b [2]byte
	_, _ = rand.Read(b[:]) // crypto/rand
	return "run_" + identifier + "_" + hex.EncodeToString(b[:])
}

// New builds an Engine.
func New(cfg kit.Config, kitDir string, t Tracker, e Executor, lg *logging.Logger) *Engine {
	gcap := 1
	if cfg.Dispatch != nil && cfg.Dispatch.Concurrency > 0 {
		gcap = cfg.Dispatch.Concurrency
	}
	csem := map[string]chan struct{}{}
	for name := range cfg.Workers {
		rw, err := cfg.ResolvedWorker(name) // skips <common> (errors) and applies the base
		if err != nil {
			continue
		}
		if n := rw.ConcurrencyOrZero(); n > 0 {
			csem[name] = make(chan struct{}, n)
		}
	}
	return &Engine{
		cfg: cfg, kitDir: kitDir, tracker: t, exec: e, log: lg,
		gsem: make(chan struct{}, gcap), csem: csem,
		live: map[string]struct{}{},
	}
}

// markLive records that this process is actively dispatching issueID, so the
// reaper will never treat it as an orphan. unmarkLive clears it when the
// dispatch finishes.
func (e *Engine) markLive(issueID string) {
	e.mu.Lock()
	e.live[issueID] = struct{}{}
	e.mu.Unlock()
}
func (e *Engine) unmarkLive(issueID string) {
	e.mu.Lock()
	delete(e.live, issueID)
	e.mu.Unlock()
}
func (e *Engine) isLive(issueID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.live[issueID]
	return ok
}

// handle runs one issue synchronously: claim → brief → at-cove work → broker.
// Every log line for this dispatch carries a run id plus the issue/class it's
// working, and a step attr naming the phase, so one dispatch's logs are
// grep-able out of interleaved concurrent dispatches.
func (e *Engine) handle(ctx context.Context, iss Issue) {
	rid := runID(iss.Identifier)
	dl := e.log.With(
		slog.String("run", rid),
		slog.String("issue", iss.Identifier),
		slog.String("class", iss.Class),
	)
	if err := e.tracker.Transition(ctx, iss.ID, RoleInProgress); err != nil {
		dl.Error("claim failed", slog.String("step", "claim"), slog.Any("err", err))
		return
	}
	rw, err := e.cfg.ResolvedWorker(iss.Class)
	if err != nil {
		return // not a dispatchable worker class (defensive; the Run filter already gates this)
	}

	comments, err := e.tracker.Comments(ctx, iss.ID)
	if err != nil {
		dl.Warn("no comments; continuing", slog.String("step", "brief"), slog.Any("err", err))
	}
	brief := assembleBrief(iss, comments)

	dir, err := os.MkdirTemp("", "at-cove-dispatch-")
	if err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("tempdir: %w", err)), nil, dl)
		return
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "task.json")
	outPath := filepath.Join(dir, "task-result.json")

	task := worker.Task{
		Issue: worker.TaskIssue{Key: iss.Identifier, Title: iss.Title},
		Repo: worker.TaskRepo{
			WorkBranch: iss.Class + "/" + iss.Identifier,
		},
		Worker: worker.TaskWorker{Class: iss.Class},
		Task:   worker.TaskSpec{Brief: brief},
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("marshal task: %w", err)), nil, dl)
		return
	}
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("write task: %w", err)), nil, dl)
		return
	}

	work, _ := time.ParseDuration(rw.Timeout) // validated by config
	over := 15 * time.Minute
	if e.cfg.Dispatch != nil && e.cfg.Dispatch.DispatchOverhead != "" {
		over, _ = time.ParseDuration(e.cfg.Dispatch.DispatchOverhead) // validated by config
	}
	rctx, cancel := context.WithTimeout(ctx, work+over)
	defer cancel()
	// e.kitDir is the .at-cove dir; --project-dir names its parent (the project root).
	argv := []string{"at-cove", "work", "--project-dir", filepath.Dir(e.kitDir), "--in", inPath, "--out", outPath, "--timeout", rw.Timeout}
	dl.Info("dispatching work", slog.String("step", "dispatch"), slog.String("argv", strings.Join(argv, " ")))
	// Pass the run id into the work subprocess (spec §7) so its own records — and
	// the VM records it merges — join this dispatch's trace under the same `run`.
	runErr := e.exec.Run(rctx, argv, []string{"COVE_RUN_ID=" + rid})

	e.broker(ctx, iss, readResult(outPath), runErr, dl)
}

// broker performs the tracker writes for one dispatch result. Single writer.
// lg is the caller's logger (the per-dispatch dl from handle, or the
// engine-level e.log for the tick panic-recovery path where no dispatch-scoped
// logger is in scope).
func (e *Engine) broker(ctx context.Context, iss Issue, tr worker.TaskResult, runErr error, lg *logging.Logger) {
	variant, _ := tr.Status.ActiveTask()
	switch {
	case runErr == nil && variant == "ok":
		e.post(ctx, iss, okComment(tr), lg)
		e.transition(ctx, iss, RoleInReview, lg)
	case variant == "needs-input":
		e.post(ctx, iss, needsInputComment(tr), lg)
		e.transition(ctx, iss, RoleNeedsInput, lg)
	default:
		e.post(ctx, iss, errorComment(tr, runErr), lg)
		e.transition(ctx, iss, RoleNeedsInput, lg)
	}
}

func (e *Engine) transition(ctx context.Context, iss Issue, r Role, lg *logging.Logger) {
	if err := e.tracker.Transition(ctx, iss.ID, r); err != nil {
		lg.Error("transition failed", slog.String("step", "broker"), slog.Int("role", int(r)), slog.Any("err", err))
	}
}
func (e *Engine) post(ctx context.Context, iss Issue, body string, lg *logging.Logger) {
	if err := e.tracker.PostComment(ctx, iss.ID, body); err != nil {
		lg.Error("post comment failed", slog.String("step", "broker"), slog.Any("err", err))
	}
}

func okComment(tr worker.TaskResult) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if tr.Status.OK != nil {
		if tr.Status.OK.PRURL != "" {
			b.WriteString("PR: " + tr.Status.OK.PRURL + "\n")
		}
		if tr.Status.OK.Message != "" {
			b.WriteString(tr.Status.OK.Message + "\n")
		}
	}
	if wr, ok := worker.WorkerResultFrom(tr.WorkerResult); ok &&
		wr.Status.OK != nil && wr.Status.OK.PullRequest != nil && wr.Status.OK.PullRequest.Message != "" {
		b.WriteString("\n" + wr.Status.OK.PullRequest.Message + "\n")
	}
	return b.String()
}

func needsInputComment(tr worker.TaskResult) string {
	b := "❓ NEEDS INPUT\n\n"
	if wr, ok := worker.WorkerResultFrom(tr.WorkerResult); ok && wr.Status.NeedsInput != nil {
		n := wr.Status.NeedsInput
		b += "**Doing:** " + n.Doing + "\n" +
			"**Blocker:** " + n.Blocker + "\n" +
			"**Need:** " + n.Need + "\n" +
			"**Tried:** " + n.Tried + "\n"
	}
	if tr.Status.NeedsInput != nil {
		if tr.Status.NeedsInput.Message != "" {
			b += "**Handoff:** " + tr.Status.NeedsInput.Message + "\n"
		}
		if tr.Status.NeedsInput.Commit != "" {
			b += "**Commit:** " + tr.Status.NeedsInput.Commit + "\n"
		}
	}
	return b
}

// Run polls every poll-interval until ctx is done, draining in-flight work on exit.
func (e *Engine) Run(ctx context.Context) error {
	d, _ := time.ParseDuration(e.cfg.Tracker.Linear.PollInterval) // validated by config
	t := time.NewTicker(d)
	defer t.Stop()
	e.tick(ctx) // immediate first pass
	for {
		select {
		case <-ctx.Done():
			e.wait()
			return ctx.Err()
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick is one poll pass: claim+dispatch the ready autonomous issues whose
// blockers are all Done, up to the concurrency caps, then reap stale claims.
func (e *Engine) tick(ctx context.Context) {
	// Dispatch is READY-only: ListReady returns issues whose blockers are all Done
	// (the tracker gates on the relationships). The scheduler never promotes from
	// the backlog — backlog means "not active" and is left untouched (COV-65).
	ready, err := e.tracker.ListReady(ctx)
	if err != nil {
		e.log.Error("list ready failed", slog.Any("err", err))
		return
	}
	for _, iss := range ready {
		if _, err := e.cfg.ResolvedWorker(iss.Class); err != nil {
			continue // skip interactive (collaborator) / unknown / <common> classes
		}
		if !e.acquire(iss.Class) {
			continue // caps full this tick
		}
		iss := iss
		e.markLive(iss.ID) // before dispatch so the reaper can't race the claim
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer e.release(iss.Class)
			defer e.unmarkLive(iss.ID)
			defer func() {
				if r := recover(); r != nil {
					e.log.Error("panic handling issue", slog.String("issue", iss.Identifier), slog.Any("err", r))
					// best-effort: park the issue for a human rather than crash the loop
					e.transition(context.Background(), iss, RoleNeedsInput, e.log)
				}
			}()
			e.handle(ctx, iss)
		}()
	}

	e.reap(ctx)
}

// reap moves orphaned IN PROGRESS issues to NEEDS INPUT: those in a dispatchable
// worker class, stuck past reaper-timeout, that no live in-process dispatch owns.
// It only ever considers issues the dispatcher itself could have claimed (a
// configured worker class); interactive, unknown, or unlabeled IN PROGRESS issues
// are a human's to manage and are left untouched (COV-55). It backstops the case a
// per-dispatch time budget can't — the in-process dispatch is gone (a crashed or
// hung worker, or a scheduler restart mid-run) but the tracker still shows IN
// PROGRESS. A run this process is actively dispatching is never reaped, however
// long it takes; its own time budget governs it.
func (e *Engine) reap(ctx context.Context) {
	if e.cfg.Dispatch == nil || e.cfg.Dispatch.ReaperTimeout == "" {
		return // reaper disabled
	}
	timeout, _ := time.ParseDuration(e.cfg.Dispatch.ReaperTimeout) // validated by config
	inProgress, err := e.tracker.ListInProgress(ctx)
	if err != nil {
		e.log.Error("list in-progress failed", slog.Any("err", err))
		return
	}
	for _, ip := range inProgress {
		if _, err := e.cfg.ResolvedWorker(ip.Class); err != nil {
			continue // not a dispatchable worker class — the dispatcher never claims it
			// (interactive/unknown/unlabeled issues), so it is never ours to reap (COV-55)
		}
		if e.isLive(ip.ID) {
			continue // this process still owns the run; never reap a live dispatch
		}
		if time.Since(ip.StartedAt) <= timeout {
			continue // still within budget
		}
		e.log.Warn("reaping stale claim",
			slog.String("issue", ip.Identifier), slog.String("step", "reap"))
		e.post(ctx, ip.Issue, reapedComment(e.cfg.Dispatch.ReaperTimeout), e.log)
		e.transition(ctx, ip.Issue, RoleNeedsInput, e.log)
	}
}

func reapedComment(timeout string) string {
	return "⚠️ Reaped: stuck in IN PROGRESS past reaper-timeout (" + timeout + ") — " +
		"likely a crashed/hung worker or a scheduler restart. Re-open to retry.\n"
}

// acquire takes a global slot and (if the class caps concurrency) a class slot,
// without blocking. It returns false and holds nothing if either is full.
func (e *Engine) acquire(class string) bool {
	select {
	case e.gsem <- struct{}{}:
	default:
		return false
	}
	cs := e.csem[class]
	if cs == nil {
		return true
	}
	select {
	case cs <- struct{}{}:
		return true
	default:
		<-e.gsem
		return false
	}
}

func (e *Engine) release(class string) {
	if cs := e.csem[class]; cs != nil {
		<-cs
	}
	<-e.gsem
}

// wait blocks until all in-flight dispatches finish (shutdown drain / tests).
func (e *Engine) wait() { e.wg.Wait() }

func errorComment(tr worker.TaskResult, runErr error) string {
	msg := ""
	if tr.Status.Error != nil {
		msg = tr.Status.Error.Message
		if tr.Status.Error.Detail != "" {
			msg += ": " + tr.Status.Error.Detail
		}
	}
	var b strings.Builder
	b.WriteString("⚠️ ERROR\n\n")
	if msg != "" {
		b.WriteString(msg + "\n")
	}
	// The result message is often only the symptom (e.g. "no dispatch output" when
	// the worker crashed before writing one); runErr carries the cause (the
	// non-zero exit + the worker's output tail). Surface both when they differ, so
	// a failed run is self-diagnosing from the tracker.
	if runErr != nil && runErr.Error() != msg {
		b.WriteString("\n" + runErr.Error() + "\n")
	}
	if msg == "" && runErr == nil {
		b.WriteString("dispatch failed\n")
	}
	return b.String()
}

// readResult reads a worker.TaskResult from path, synthesizing an ERROR result when
// the file is missing, unreadable, invalid, or has no valid status.
func readResult(path string) worker.TaskResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return errorResult(fmt.Errorf("no dispatch output: %w", err))
	}
	var tr worker.TaskResult
	if err := json.Unmarshal(data, &tr); err != nil {
		return errorResult(fmt.Errorf("invalid dispatch output: %w", err))
	}
	if _, err := tr.Status.ActiveTask(); err != nil {
		return errorResult(fmt.Errorf("dispatch output: %w", err))
	}
	return tr
}

func errorResult(err error) worker.TaskResult {
	return worker.ErrorResult(err.Error(), "")
}
