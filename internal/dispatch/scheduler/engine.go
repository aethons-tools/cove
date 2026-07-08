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

	"github.com/aethons-tools/cove/internal/dispatch/config"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

// Engine polls a Tracker and dispatches ready autonomous work.
type Engine struct {
	cfg     config.Config
	tracker Tracker
	exec    Executor
	resolve func([]string) (string, error)
	log     *log.Logger

	gsem chan struct{}            // global concurrency
	csem map[string]chan struct{} // per-class concurrency (nil entry = no cap)
	wg   sync.WaitGroup
}

// New builds an Engine. resolve turns a secret's argv into its value (host side).
func New(cfg config.Config, t Tracker, e Executor, resolve func([]string) (string, error), logger *log.Logger) *Engine {
	gcap := cfg.Concurrency
	if gcap < 1 {
		gcap = 1
	}
	csem := map[string]chan struct{}{}
	for name, cl := range cfg.Classes {
		if cl.Concurrency > 0 {
			csem[name] = make(chan struct{}, cl.Concurrency)
		}
	}
	return &Engine{
		cfg: cfg, tracker: t, exec: e, resolve: resolve, log: logger,
		gsem: make(chan struct{}, gcap), csem: csem,
	}
}

// handle runs one issue synchronously: claim → brief → at-cove dispatch → broker.
func (e *Engine) handle(ctx context.Context, iss Issue) {
	if err := e.tracker.Transition(ctx, iss.ID, RoleInProgress); err != nil {
		e.log.Printf("claim %s: %v", iss.Identifier, err)
		return
	}
	cl := e.cfg.Classes[iss.Class]

	comments, err := e.tracker.Comments(ctx, iss.ID)
	if err != nil {
		e.log.Printf("comments %s: %v (continuing with none)", iss.Identifier, err)
	}
	brief := assembleBrief(iss, e.cfg.Repo.Slug, comments)

	dir, err := os.MkdirTemp("", "at-dispatch-")
	if err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("tempdir: %w", err)), nil)
		return
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "input.json")
	outPath := filepath.Join(dir, "output.json")

	in := worker.Input{
		Issue: worker.IssueInput{Key: iss.Identifier, Title: iss.Title, WorkClass: iss.Class, Brief: brief},
		Repo: worker.RepoInput{
			Name: e.cfg.Repo.Slug, SourceBranch: e.cfg.Repo.SourceBranch,
			WorkBranch: iss.Class + "/" + iss.Identifier,
		},
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("marshal input: %w", err)), nil)
		return
	}
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("write input: %w", err)), nil)
		return
	}

	work, _ := time.ParseDuration(cl.Timeout)             // validated by config
	over, _ := time.ParseDuration(e.cfg.DispatchOverhead) // validated by config
	rctx, cancel := context.WithTimeout(ctx, work+over)
	defer cancel()
	argv := []string{"at-cove", "dispatch", cl.Kit, "--in", inPath, "--out", outPath, "--timeout", cl.Timeout}
	runErr := e.exec.Run(rctx, argv, nil)

	e.broker(ctx, iss, readOutput(outPath), runErr)
}

// broker performs the tracker writes for one dispatch outcome. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, out worker.Output, runErr error) {
	switch {
	case runErr == nil && out.Status == worker.StatusOK:
		e.post(ctx, iss, okComment(out))
		e.transition(ctx, iss, RoleInReview)
	case out.Status == worker.StatusNeedsInput:
		e.post(ctx, iss, needsInputComment(out))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(out, runErr))
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

func okComment(out worker.Output) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if out.Work.PRURL != "" {
		b.WriteString("PR: " + out.Work.PRURL + "\n")
	}
	if out.Work.Branch != "" {
		b.WriteString("Branch: " + out.Work.Branch + "\n")
	}
	if out.Agent != nil && out.Agent.PRMessage != "" {
		b.WriteString("\n" + out.Agent.PRMessage + "\n")
	}
	return b.String()
}

func needsInputComment(out worker.Output) string {
	b := "❓ NEEDS INPUT\n\n"
	if out.Agent != nil && out.Agent.NeedsInput != nil {
		n := out.Agent.NeedsInput
		b += "**Doing:** " + n.Doing + "\n" +
			"**Blocker:** " + n.Blocker + "\n" +
			"**Need:** " + n.Need + "\n" +
			"**Tried:** " + n.Tried + "\n"
	}
	if out.Work.SafeState != "" {
		b += "**Safe state:** " + out.Work.SafeState + "\n"
	}
	return b
}

// Run polls every poll-interval until ctx is done, draining in-flight work on exit.
func (e *Engine) Run(ctx context.Context) error {
	d, _ := time.ParseDuration(e.cfg.Tracker.PollInterval) // validated by config
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
		cl, ok := e.cfg.Classes[iss.Class]
		if !ok || cl.Mode != "autonomous" {
			continue // skip interactive / unknown classes
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

func errorComment(out worker.Output, runErr error) string {
	msg := out.Message
	if msg == "" && out.Work.Error != "" {
		msg = out.Work.Error
	}
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	if msg == "" {
		msg = "dispatch failed"
	}
	return "⚠️ ERROR\n\n" + msg + "\n"
}

// readOutput reads a worker.Output from path, synthesizing an ERROR output when
// the file is missing, unreadable, invalid, or has no status.
func readOutput(path string) worker.Output {
	data, err := os.ReadFile(path)
	if err != nil {
		return errorOutput(fmt.Errorf("no dispatch output: %w", err))
	}
	var out worker.Output
	if err := json.Unmarshal(data, &out); err != nil {
		return errorOutput(fmt.Errorf("invalid dispatch output: %w", err))
	}
	if out.Status == "" {
		return errorOutput(fmt.Errorf("dispatch output has no status"))
	}
	return out
}

func errorOutput(err error) worker.Output {
	return worker.Output{Status: worker.StatusError, Message: err.Error()}
}
