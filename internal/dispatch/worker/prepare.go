package worker

import (
	"context"
	"fmt"
	"strings"
)

// Prepare sets up (or resumes) the work branch in dir. It does no content extraction —
// the worker reads its brief straight from task.json. Idempotent: a clean existing
// checkout and an existing remote work-branch are reused.
func Prepare(ctx context.Context, dir string, task Task, git Git) error {
	sb, wb := task.Repo.SourceBranch, task.Repo.WorkBranch
	if wb == "" || wb == sb {
		return fmt.Errorf("work-branch must be non-empty and differ from source-branch %q", sb)
	}
	host := task.Repo.Host
	if host == "" {
		host = "https://github.com"
	}
	remote := strings.TrimSuffix(host, "/") + "/" + task.Repo.Name + ".git"
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
		if err := git.Sync(ctx, dir, wb); err != nil {
			return fmt.Errorf("resume %s: %w", wb, err)
		}
		return nil
	}
	if err := git.NewBranch(ctx, dir, wb, sb); err != nil {
		return fmt.Errorf("create %s: %w", wb, err)
	}
	return nil
}
