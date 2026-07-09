package worker

import "context"

// CodeHost opens (or finds) a pull request.
type CodeHost interface {
	OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}

// Complete reads the worker's result and finishes the work: commit/push and, when the
// worker proposed a PR, open it. It always returns a valid TaskResult (never a Go error),
// echoing the raw worker-result.
func Complete(ctx context.Context, dir string, task Task, git Git, ch CodeHost) TaskResult {
	wr, raw, ok, err := ReadWorkerResult(dir)
	if err != nil {
		return taskErr(nil, "unreadable worker result", err.Error())
	}
	if !ok {
		return taskErr(nil, "no worker result", "")
	}
	variant, verr := wr.Status.Active()
	if verr != nil {
		return taskErr(raw, "invalid worker result", verr.Error())
	}
	switch variant {
	case "ok":
		return completeOK(ctx, dir, task, git, ch, wr, raw)
	case "needs-input":
		return completeNeedsInput(ctx, dir, task, git, raw)
	default: // error
		msg := ""
		if wr.Status.Error != nil {
			msg = wr.Status.Error.Message
		}
		return taskErr(raw, "worker could not execute task", msg)
	}
}

func completeOK(ctx context.Context, dir string, task Task, git Git, ch CodeHost, wr WorkerResult, raw any) TaskResult {
	if has, err := git.HasChanges(ctx, dir); err != nil {
		return taskErr(raw, "check for changes", err.Error())
	} else if has {
		if _, err := git.Commit(ctx, dir, task.Issue.Key+": "+task.Issue.Title); err != nil {
			return taskErr(raw, "commit", err.Error())
		}
	}
	if err := git.Push(ctx, dir, task.Repo.WorkBranch); err != nil {
		return taskErr(raw, "push", err.Error())
	}
	okStatus := &TaskOK{Message: "pushed " + task.Repo.WorkBranch}
	if pr := wr.Status.OK.PullRequest; pr != nil {
		differs, err := git.DiffersFrom(ctx, dir, task.Repo.SourceBranch)
		if err != nil {
			return taskErr(raw, "diff", err.Error())
		}
		if differs {
			url, err := ch.OpenPR(ctx, task.Repo.Name, task.Repo.SourceBranch, task.Repo.WorkBranch, pr.Title, pr.Message)
			if err != nil {
				return taskErr(raw, "open PR", err.Error())
			}
			okStatus.PRURL = url
			okStatus.Message = "opened PR"
		} else {
			okStatus.Message = "no changes to open a PR"
		}
	}
	return TaskResult{Status: TaskStatus{OK: okStatus}, WorkerResult: raw}
}

func completeNeedsInput(ctx context.Context, dir string, task Task, git Git, raw any) TaskResult {
	if has, err := git.HasChanges(ctx, dir); err == nil && has {
		_, _ = git.Commit(ctx, dir, "WIP "+task.Issue.Key)
	}
	_ = git.Push(ctx, dir, task.Repo.WorkBranch) // best-effort; the WIP lives on origin
	head, _ := git.Head(ctx, dir)
	return TaskResult{
		Status:       TaskStatus{NeedsInput: &TaskNeedsInput{Message: "worker needs input; WIP pushed to " + task.Repo.WorkBranch, Commit: head}},
		WorkerResult: raw,
	}
}

// taskErr builds an ERROR TaskResult, echoing the raw worker-result. detail is the
// underlying diagnostic (omitted if "").
func taskErr(raw any, msg, detail string) TaskResult {
	tr := ErrorResult(msg, detail)
	tr.WorkerResult = raw
	return tr
}
