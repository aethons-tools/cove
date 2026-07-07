package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is the git surface at-work needs. See ShellGit for the real implementation.
type Git interface {
	EnsureClean(ctx context.Context, remote, dir string) error // clone if absent; else verify clean
	Sync(ctx context.Context, dir, branch string) error        // checkout + fast-forward from origin
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
		f, err := os.CreateTemp("", "at-work-askpass-*.sh")
		if err != nil {
			return nil, err
		}
		if _, err := f.WriteString("#!/bin/sh\nprintf '%s\\n' \"$AT_WORK_ASKPASS_TOKEN\"\n"); err != nil {
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
		"GIT_AUTHOR_NAME=at-work", "GIT_AUTHOR_EMAIL=at-work@aethons.tools",
		"GIT_COMMITTER_NAME=at-work", "GIT_COMMITTER_EMAIL=at-work@aethons.tools",
	)
	if g.askpass != "" {
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+g.askpass, "AT_WORK_ASKPASS_TOKEN="+g.token)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (g *ShellGit) EnsureClean(ctx context.Context, remote, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		_, err := g.git(ctx, "", "clone", remote, dir)
		return err
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
