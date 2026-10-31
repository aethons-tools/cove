package configrepo

import (
	"context"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ConflictError is a merge refused because both sides changed the same
// files differently (a file deleted on one side and changed on the other
// included). Nothing was pushed: open a pull request instead.
type ConflictError struct{ Paths []string }

func (e *ConflictError) Error() string { return "merge conflict in: " + strings.Join(e.Paths, ", ") }

// MergeResult is a merge's outcome: the followed branch's head after it, and
// whether it was a fast-forward or had nothing to merge.
type MergeResult struct {
	Head        string `json:"head"`
	FastForward bool   `json:"fastForward"`
	NoOp        bool   `json:"noOp"`
}

// Merge merges branch (still at wantHead, else ErrStale) into the followed
// branch, as author with msg, and pushes it; the branch is then moved to the
// result too, level with the followed branch. check sees the merged files
// first: its error is returned as is, with nothing pushed. Already merged is
// a no-op; the followed branch an ancestor is a fast-forward; else a 3-way
// merge per file over the merge base, a *ConflictError when both sides
// changed a file differently.
func (r *Repo) Merge(ctx context.Context, branch, wantHead string, author Signature, msg string, check func(files map[string]File) error) (MergeResult, error) {
	if err := r.checkBranch(branch); err != nil {
		return MergeResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, auth, err := r.open(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	ours, err := head(repo, branch)
	if err != nil {
		return MergeResult{}, err
	}
	if ours.Hash.String() != wantHead {
		return MergeResult{}, ErrStale
	}
	theirs, err := head(repo, r.Branch)
	if err != nil {
		return MergeResult{}, err
	}
	if merged, err := ours.IsAncestor(theirs); err != nil {
		return MergeResult{}, err
	} else if merged || ours.Hash == theirs.Hash {
		return MergeResult{Head: theirs.Hash.String(), NoOp: true}, nil
	}
	if ff, err := theirs.IsAncestor(ours); err != nil {
		return MergeResult{}, err
	} else if ff {
		files, err := filesOf(ours)
		if err != nil {
			return MergeResult{}, err
		}
		if err := check(files); err != nil {
			return MergeResult{}, err
		}
		if err := r.push(ctx, repo, auth, r.Branch, ours.Hash); err != nil {
			return MergeResult{}, err
		}
		return MergeResult{Head: ours.Hash.String(), FastForward: true}, nil
	}
	bases, err := ours.MergeBase(theirs)
	if err != nil {
		return MergeResult{}, err
	}
	var base map[string]entry
	if len(bases) > 0 {
		if base, err = flatten(bases[0]); err != nil {
			return MergeResult{}, err
		}
	}
	o, err := flatten(ours)
	if err != nil {
		return MergeResult{}, err
	}
	t, err := flatten(theirs)
	if err != nil {
		return MergeResult{}, err
	}
	merged, conflicts := merge3(base, o, t)
	if len(conflicts) > 0 {
		return MergeResult{}, &ConflictError{Paths: conflicts}
	}
	files, err := readEntries(repo, merged)
	if err != nil {
		return MergeResult{}, err
	}
	if err := check(files); err != nil {
		return MergeResult{}, err
	}
	tree, err := writeTree(repo, merged)
	if err != nil {
		return MergeResult{}, err
	}
	c, err := writeCommit(repo, tree, msg, author, theirs.Hash, ours.Hash)
	if err != nil {
		return MergeResult{}, err
	}
	if err := r.push(ctx, repo, auth, r.Branch, c); err != nil {
		return MergeResult{}, err
	}
	// Level the branch with the merge (a fast-forward from ours). A failure
	// here leaves the merge in place: the branch is just behind.
	_ = r.push(ctx, repo, auth, branch, c)
	return MergeResult{Head: c.String()}, nil
}

// merge3 merges ours and theirs over base per path: a side that left a path
// as base takes the other's; identical sides agree; else it is a conflict
// (sorted).
func merge3(base, ours, theirs map[string]entry) (map[string]entry, []string) {
	paths := map[string]bool{}
	for _, m := range []map[string]entry{base, ours, theirs} {
		for p := range m {
			paths[p] = true
		}
	}
	out := map[string]entry{}
	var conflicts []string
	for p := range paths {
		b, inB := base[p]
		o, inO := ours[p]
		t, inT := theirs[p]
		same := func(x entry, inX bool, y entry, inY bool) bool { return inX == inY && (!inX || x == y) }
		var e entry
		var keep bool
		switch {
		case same(o, inO, t, inT):
			e, keep = o, inO
		case same(o, inO, b, inB):
			e, keep = t, inT
		case same(t, inT, b, inB):
			e, keep = o, inO
		default:
			conflicts = append(conflicts, p)
			continue
		}
		if keep {
			out[p] = e
		}
	}
	sort.Strings(conflicts)
	return out, conflicts
}

// readEntries reads flat's blobs as files.
func readEntries(repo *git.Repository, flat map[string]entry) (map[string]File, error) {
	out := make(map[string]File, len(flat))
	for p, e := range flat {
		data, err := readBlob(repo, e.hash)
		if err != nil {
			return nil, err
		}
		out[p] = File{Data: data, Mode: e.mode}
	}
	return out, nil
}

func readBlob(repo *git.Repository, h plumbing.Hash) ([]byte, error) {
	b, err := object.GetBlob(repo.Storer, h)
	if err != nil {
		return nil, err
	}
	rd, err := b.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(rd)
}
