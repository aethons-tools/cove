package worker

import "context"

// CodeHost opens (or finds) a pull request.
type CodeHost interface {
	OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}

// Complete reads the agent's outcome and finishes the work: commit/push and, on OK,
// open a PR. It always returns a valid Output (never a Go error).
func Complete(ctx context.Context, dir string, in Input, git Git, ch CodeHost) Output {
	out := Output{Work: Work{Branch: in.Repo.WorkBranch}}
	oc, ok, err := ReadOutcome(dir)
	if err != nil {
		return errOut(out, "unreadable agent outcome: "+err.Error())
	}
	if !ok {
		return errOut(out, "no agent outcome")
	}
	out.Agent = &oc

	switch oc.Status {
	case StatusOK:
		return completeOK(ctx, dir, in, git, ch, out, oc)
	case StatusNeedsInput:
		return completeNeedsInput(ctx, dir, in, git, out)
	default: // ERROR or unknown
		msg := oc.Message
		if msg == "" {
			msg = "agent reported status " + oc.Status
		}
		return errOut(out, msg)
	}
}

func completeOK(ctx context.Context, dir string, in Input, git Git, ch CodeHost, out Output, oc Outcome) Output {
	if has, err := git.HasChanges(ctx, dir); err != nil {
		return errOut(out, "status: "+err.Error())
	} else if has {
		if _, err := git.Commit(ctx, dir, in.Issue.Key+": "+in.Issue.Title); err != nil {
			return errOut(out, "commit: "+err.Error())
		}
	}
	differs, err := git.DiffersFrom(ctx, dir, in.Repo.SourceBranch)
	if err != nil {
		return errOut(out, "diff: "+err.Error())
	}
	if !differs {
		return errOut(out, "agent reported OK but produced no changes")
	}
	if err := git.Push(ctx, dir, in.Repo.WorkBranch); err != nil {
		return errOut(out, "push: "+err.Error())
	}
	prURL, err := ch.OpenPR(ctx, in.Repo.Name, in.Repo.SourceBranch, in.Repo.WorkBranch,
		in.Issue.Key+": "+in.Issue.Title, oc.PRMessage)
	if err != nil {
		return errOut(out, "open PR: "+err.Error())
	}
	out.Status = StatusOK
	out.Message = "opened PR"
	out.Work.PRURL = prURL
	return out
}

func completeNeedsInput(ctx context.Context, dir string, in Input, git Git, out Output) Output {
	if has, err := git.HasChanges(ctx, dir); err == nil && has {
		_, _ = git.Commit(ctx, dir, "WIP "+in.Issue.Key)
	}
	_ = git.Push(ctx, dir, in.Repo.WorkBranch) // best-effort; safe state lives on origin
	head, _ := git.Head(ctx, dir)
	out.Status = StatusNeedsInput
	out.Work.SafeState = in.Repo.WorkBranch + " @ " + head
	return out
}

func errOut(out Output, msg string) Output {
	out.Status = StatusError
	out.Message = msg
	out.Work.Error = msg
	return out
}
