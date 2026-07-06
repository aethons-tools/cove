package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/config"
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

// handle runs one issue synchronously: claim → brief → command → broker.
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
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("tempdir: %w", err))
		return
	}
	defer os.RemoveAll(dir)
	briefPath := filepath.Join(dir, "brief.md")
	resultPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(briefPath, []byte(brief), 0o600); err != nil {
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("write brief: %w", err))
		return
	}

	secrets, err := config.ResolveSecrets(e.cfg.Secrets, e.resolve)
	if err != nil {
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("resolve secrets: %w", err))
		return
	}
	env := config.BuildEnv(config.Task{
		Issue: iss.Identifier, Class: iss.Class, Repo: e.cfg.Repo.Slug,
		Timeout: cl.Timeout, BriefPath: briefPath, ResultPath: resultPath,
	}, secrets)

	d, _ := time.ParseDuration(cl.Timeout) // validated by config
	rctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	runErr := e.exec.Run(rctx, cl.Command, env)

	e.broker(ctx, iss, config.ReadResult(resultPath), runErr)
}

// broker performs the tracker writes for one result. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, res config.Result, runErr error) {
	switch {
	case runErr == nil && res.Status == config.StatusOK:
		e.post(ctx, iss, okComment(res))
		e.transition(ctx, iss, RoleInReview)
	case res.Status == config.StatusNeedsInput:
		e.post(ctx, iss, needsInputComment(res))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(res, runErr))
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

func okComment(res config.Result) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if res.Artifacts.PRURL != "" {
		b.WriteString("PR: " + res.Artifacts.PRURL + "\n")
	}
	if res.Artifacts.Branch != "" {
		b.WriteString("Branch: " + res.Artifacts.Branch + "\n")
	}
	if res.Artifacts.DocPath != "" {
		b.WriteString("Doc: " + res.Artifacts.DocPath + "\n")
	}
	if res.Summary != "" {
		b.WriteString("\n" + res.Summary + "\n")
	}
	return b.String()
}

func needsInputComment(res config.Result) string {
	n := res.NeedsInput
	if n == nil {
		return "❓ NEEDS INPUT\n\n" + res.Summary
	}
	return "❓ NEEDS INPUT\n\n" +
		"**Doing:** " + n.Doing + "\n" +
		"**Blocker:** " + n.Blocker + "\n" +
		"**Need:** " + n.Need + "\n" +
		"**Tried:** " + n.Tried + "\n" +
		"**Safe state:** " + n.SafeState + "\n"
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

func errorComment(res config.Result, runErr error) string {
	msg := res.Summary
	if runErr != nil {
		msg = "command failed: " + runErr.Error()
		if res.Summary != "" {
			msg += " (" + res.Summary + ")"
		}
	}
	return "⚠️ Dispatch error — routed to NEEDS INPUT.\n\n" + msg + "\n"
}
