package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/kit"
)

// Engine polls a Tracker and dispatches ready autonomous work.
type Engine struct {
	cfg     kit.Config
	kitDir  string
	tracker Tracker
	exec    Executor
	log     *log.Logger

	gsem chan struct{}            // global concurrency
	csem map[string]chan struct{} // per-class concurrency (nil entry = no cap)
	wg   sync.WaitGroup
}

// New builds an Engine.
func New(cfg kit.Config, kitDir string, t Tracker, e Executor, logger *log.Logger) *Engine {
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
		if rw.Concurrency > 0 {
			csem[name] = make(chan struct{}, rw.Concurrency)
		}
	}
	return &Engine{
		cfg: cfg, kitDir: kitDir, tracker: t, exec: e, log: logger,
		gsem: make(chan struct{}, gcap), csem: csem,
	}
}

// handle runs one issue synchronously: claim → brief → at-cove work → broker.
func (e *Engine) handle(ctx context.Context, iss Issue) {
	if err := e.tracker.Transition(ctx, iss.ID, RoleInProgress); err != nil {
		e.log.Printf("claim %s: %v", iss.Identifier, err)
		return
	}
	rw, err := e.cfg.ResolvedWorker(iss.Class)
	if err != nil {
		return // not a dispatchable worker class (defensive; the Run filter already gates this)
	}

	comments, err := e.tracker.Comments(ctx, iss.ID)
	if err != nil {
		e.log.Printf("comments %s: %v (continuing with none)", iss.Identifier, err)
	}
	brief := assembleBrief(iss, comments)

	dir, err := os.MkdirTemp("", "at-cove-dispatch-")
	if err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("tempdir: %w", err)), nil)
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
		e.broker(ctx, iss, errorResult(fmt.Errorf("marshal task: %w", err)), nil)
		return
	}
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("write task: %w", err)), nil)
		return
	}

	work, _ := time.ParseDuration(rw.Timeout) // validated by config
	over := 15 * time.Minute
	if e.cfg.Dispatch != nil && e.cfg.Dispatch.DispatchOverhead != "" {
		over, _ = time.ParseDuration(e.cfg.Dispatch.DispatchOverhead) // validated by config
	}
	rctx, cancel := context.WithTimeout(ctx, work+over)
	defer cancel()
	argv := []string{"at-cove", "work", e.kitDir, "--in", inPath, "--out", outPath, "--timeout", rw.Timeout}
	e.log.Printf("dispatch %s: exec %s", iss.Identifier, strings.Join(argv, " "))
	runErr := e.exec.Run(rctx, argv, nil)

	e.broker(ctx, iss, readResult(outPath), runErr)
}

// broker performs the tracker writes for one dispatch result. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, tr worker.TaskResult, runErr error) {
	variant, _ := tr.Status.ActiveTask()
	switch {
	case runErr == nil && variant == "ok":
		e.post(ctx, iss, okComment(tr))
		e.transition(ctx, iss, RoleInReview)
	case variant == "needs-input":
		e.post(ctx, iss, needsInputComment(tr))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(tr, runErr))
		e.transition(ctx, iss, RoleNeedsInput)
	}
}

func (e *Engine) transition(ctx context.Context, iss Issue, r Role) {
	if err := e.tracker.Transition(ctx, iss.ID, r); err != nil {
		e.log.Printf("transition %s -> %d: %v", iss.Identifier, r, err)
	}
}
func (e *Engine) post(ctx context.Context, iss Issue, body string) {
	if err := e.tracker.PostComment(ctx, iss.ID, body); err != nil {
		e.log.Printf("comment %s: %v", iss.Identifier, err)
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

// tick is one poll pass: reconcile BLOCKED→READY, then claim+dispatch ready
// autonomous issues up to the concurrency caps.
func (e *Engine) tick(ctx context.Context) {
	if unb, err := e.tracker.ListUnblockable(ctx); err != nil {
		e.log.Printf("list unblockable: %v", err)
	} else {
		for _, iss := range unb {
			if err := e.tracker.Transition(ctx, iss.ID, RoleReady); err != nil {
				e.log.Printf("unblock %s: %v", iss.Identifier, err)
			}
		}
	}

	ready, err := e.tracker.ListReady(ctx)
	if err != nil {
		e.log.Printf("list ready: %v", err)
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
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer e.release(iss.Class)
			defer func() {
				if r := recover(); r != nil {
					e.log.Printf("panic handling %s: %v", iss.Identifier, r)
					// best-effort: park the issue for a human rather than crash the loop
					e.transition(context.Background(), iss, RoleNeedsInput)
				}
			}()
			e.handle(ctx, iss)
		}()
	}
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
