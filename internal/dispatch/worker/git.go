package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is the git surface at-task needs. See ShellGit for the real implementation.
type Git interface {
	EnsureClean(ctx context.Context, remote, dir string) error   // init in place if absent; else verify clean
	Clone(ctx context.Context, remote, branch, dir string) error // fresh clone of remote's branch into an empty dir
	Sync(ctx context.Context, dir, branch string) error          // checkout + fast-forward from origin
	RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error)
	NewBranch(ctx context.Context, dir, branch, from string) error
	HasChanges(ctx context.Context, dir string) (bool, error)
	DiffersFrom(ctx context.Context, dir, base string) (bool, error)
	Commit(ctx context.Context, dir, msg string) (sha string, err error)
	Push(ctx context.Context, dir, branch string) error
	Head(ctx context.Context, dir string) (sha string, err error)
}

// ShellGit runs the git CLI. Auth for https remotes flows through a temp GIT_ASKPASS
// script (token never on argv). A bot identity is set so commits work without global
// git config.
type ShellGit struct {
	token   string
	askpass string // path to the askpass script; "" when no token
}

func NewShellGit(token string) (*ShellGit, error) {
	g := &ShellGit{token: token}
	if token != "" {
		f, err := os.CreateTemp("", "at-task-askpass-*.sh")
		if err != nil {
			return nil, err
		}
		if _, err := f.WriteString("#!/bin/sh\nprintf '%s\\n' \"$AT_TASK_ASKPASS_TOKEN\"\n"); err != nil {
			return nil, err
		}
		f.Close()
		if err := os.Chmod(f.Name(), 0o700); err != nil {
			return nil, err
		}
		g.askpass = f.Name()
	}
	return g, nil
}

func (g *ShellGit) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=at-task", "GIT_AUTHOR_EMAIL=at-task@aethons.tools",
		"GIT_COMMITTER_NAME=at-task", "GIT_COMMITTER_EMAIL=at-task@aethons.tools",
	)
	if g.askpass != "" {
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+g.askpass, "AT_TASK_ASKPASS_TOKEN="+g.token)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (g *ShellGit) EnsureClean(ctx context.Context, remote, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		// Init in place: the orchestrator has already injected .at-task/ here, so a
		// `git clone` (which refuses a non-empty dir) won't work. Sync() then fetches
		// and checks out the base branch.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if _, err := g.git(ctx, dir, "init", "-q"); err != nil {
			return err
		}
		if _, err := g.git(ctx, dir, "remote", "add", "origin", remote); err != nil {
			return err
		}
		return excludeAtTask(dir)
	}
	status, err := g.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("refusing to run: %s has uncommitted changes", dir)
	}
	return nil
}

// excludeAtTask adds the .at-task/ handoff dir to the repo's local excludes so its
// files never appear in git status or get committed into the work branch.
func excludeAtTask(dir string) error {
	p := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n/" + taskSubdir + "/\n")
	return err
}

// Clone populates dir with a standard checkout of remote's branch (origin remote
// + working tree), authenticating https remotes through the same env-only askpass
// as every other op. Unlike EnsureClean, which inits in place over an
// orchestrator-injected .at-task/, Clone targets an empty dir — the collaborator
// workspace on first session — so a plain `git clone` (which refuses a non-empty
// dir) is exactly right, and Sync's single-branch fetch is not.
func (g *ShellGit) Clone(ctx context.Context, remote, branch, dir string) error {
	_, err := g.git(ctx, "", "clone", "--branch", branch, remote, dir)
	return err
}

func (g *ShellGit) Sync(ctx context.Context, dir, branch string) error {
	if _, err := g.git(ctx, dir, "fetch", "origin", branch); err != nil {
		return err
	}
	_, err := g.git(ctx, dir, "checkout", "-B", branch, "origin/"+branch)
	return err
}

func (g *ShellGit) RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error) {
	out, err := g.git(ctx, dir, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g *ShellGit) NewBranch(ctx context.Context, dir, branch, from string) error {
	_, err := g.git(ctx, dir, "checkout", "-b", branch, from)
	return err
}

func (g *ShellGit) HasChanges(ctx context.Context, dir string) (bool, error) {
	out, err := g.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g *ShellGit) DiffersFrom(ctx context.Context, dir, base string) (bool, error) {
	out, err := g.git(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "0", nil
}

func (g *ShellGit) Commit(ctx context.Context, dir, msg string) (string, error) {
	if _, err := g.git(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, dir, "commit", "-m", msg); err != nil {
		return "", err
	}
	return g.Head(ctx, dir)
}

func (g *ShellGit) Push(ctx context.Context, dir, branch string) error {
	_, err := g.git(ctx, dir, "push", "-u", "origin", branch)
	return err
}

func (g *ShellGit) Head(ctx context.Context, dir string) (string, error) {
	return g.git(ctx, dir, "rev-parse", "HEAD")
}

// ShellGit implements Git.
var _ Git = (*ShellGit)(nil)
