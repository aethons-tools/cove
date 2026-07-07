package worker

import (
	"context"
	"fmt"
)

// Prepare sets up (or resumes) the work branch in dir and writes the brief. It is
// idempotent: a clean existing checkout and an existing remote work-branch are reused.
func Prepare(ctx context.Context, dir string, in Input, git Git) error {
	sb, wb := in.Repo.SourceBranch, in.Repo.WorkBranch
	if wb == "" || wb == sb {
		return fmt.Errorf("work-branch must be non-empty and differ from source-branch %q", sb)
	}
	remote := "https://github.com/" + in.Repo.Name + ".git"
	if err := git.EnsureClean(ctx, remote, dir); err != nil {
		return err
	}
	if err := git.Sync(ctx, dir, sb); err != nil {
		return fmt.Errorf("sync base %s: %w", sb, err)
	}
	has, err := git.RemoteHasBranch(ctx, dir, wb)
	if err != nil {
		return err
	}
	if has {
		if err := git.Sync(ctx, dir, wb); err != nil { // resume prior WIP
			return fmt.Errorf("resume %s: %w", wb, err)
		}
	} else {
		if err := git.NewBranch(ctx, dir, wb, sb); err != nil {
			return fmt.Errorf("create %s: %w", wb, err)
		}
	}
	return WriteBrief(dir, in.Issue.Brief)
}
