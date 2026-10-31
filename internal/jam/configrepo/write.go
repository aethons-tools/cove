package configrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Signature is a commit's author: the operator.
type Signature struct{ Name, Email string }

// committer is every commit's committer: Jam made it.
var committer = Signature{Name: "Jam", Email: "jam@localhost"}

// Changes are the files a commit writes (Put) and removes (Delete), by slash
// path from the repo root.
type Changes struct {
	Put    map[string]File
	Delete []string
}

// checkBranch refuses an invalid branch name and the followed branch.
func (r *Repo) checkBranch(name string) error {
	if name == r.Branch {
		return ErrFollowedBranch
	}
	if name == "" || plumbing.NewBranchReferenceName(name).Validate() != nil || strings.HasPrefix(name, "-") {
		return fmt.Errorf("config repo: %q is not a valid branch name", name)
	}
	return nil
}

// CreateBranch makes name at the followed branch's head and returns it.
func (r *Repo) CreateBranch(ctx context.Context, name string) (string, error) {
	if err := r.checkBranch(name); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, auth, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	if _, err := head(repo, name); err == nil {
		return "", ErrBranchExists
	}
	from, err := head(repo, r.Branch)
	if err != nil {
		return "", err
	}
	if err := r.push(ctx, repo, auth, name, from.Hash); err != nil {
		if errors.Is(err, ErrStale) {
			return "", ErrBranchExists // created by someone else meanwhile
		}
		return "", err
	}
	return from.Hash.String(), nil
}

// Commit writes ch onto branch as one commit by author and returns the new
// head. branch must still be at wantHead (else ErrStale, as is a push that
// is not a fast-forward). A commit that would change nothing is not made:
// wantHead comes back.
func (r *Repo) Commit(ctx context.Context, branch, wantHead string, ch Changes, author Signature, msg string) (string, error) {
	if err := r.checkBranch(branch); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, auth, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	parent, err := head(repo, branch)
	if err != nil {
		return "", err
	}
	if parent.Hash.String() != wantHead {
		return "", ErrStale
	}
	flat, err := flatten(parent)
	if err != nil {
		return "", err
	}
	for p, f := range ch.Put {
		h, err := writeBlob(repo, f.Data)
		if err != nil {
			return "", err
		}
		mode := f.Mode
		if mode != filemode.Executable {
			mode = filemode.Regular
		}
		flat[p] = entry{hash: h, mode: mode}
	}
	for _, p := range ch.Delete {
		delete(flat, p)
	}
	tree, err := writeTree(repo, flat)
	if err != nil {
		return "", err
	}
	if tree == parent.TreeHash {
		return wantHead, nil
	}
	c, err := writeCommit(repo, tree, msg, author, parent.Hash)
	if err != nil {
		return "", err
	}
	if err := r.push(ctx, repo, auth, branch, c); err != nil {
		return "", err
	}
	return c.String(), nil
}

// push sets branch to commit on the remote; a non-fast-forward is ErrStale.
func (r *Repo) push(ctx context.Context, repo *git.Repository, auth transport.AuthMethod, branch string, commit plumbing.Hash) error {
	local := plumbing.NewBranchReferenceName(branch)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(local, commit)); err != nil {
		return err
	}
	spec := config.RefSpec(local.String() + ":" + local.String())
	err := repo.PushContext(ctx, &git.PushOptions{RemoteName: remoteName, Auth: auth, RefSpecs: []config.RefSpec{spec}})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	case strings.Contains(err.Error(), "non-fast-forward"):
		return ErrStale
	default:
		return r.explain("push", err)
	}
	// The fetched copy follows at once, so the next read needs no round trip
	// to see it.
	return repo.Storer.SetReference(plumbing.NewHashReference(remoteRef(branch), commit))
}

// entry is one file in a flattened tree.
type entry struct {
	hash plumbing.Hash
	mode filemode.FileMode
}

// flatten is c's tree as slash path → entry.
func flatten(c *object.Commit) (map[string]entry, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	out := map[string]entry{}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, e, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if e.Mode == filemode.Dir {
			continue
		}
		out[name] = entry{hash: e.Hash, mode: e.Mode}
	}
	return out, nil
}

func writeBlob(repo *git.Repository, data []byte) (plumbing.Hash, error) {
	o := repo.Storer.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(o)
}

// writeTree stores flat as nested trees and returns the root's hash.
func writeTree(repo *git.Repository, flat map[string]entry) (plumbing.Hash, error) {
	// dirs: directory → its direct entries (files, and subdirs filled in later).
	files := map[string][]object.TreeEntry{}
	dirs := map[string]bool{"": true}
	for p, e := range flat {
		dir, name := path.Split(p)
		dir = strings.TrimSuffix(dir, "/")
		files[dir] = append(files[dir], object.TreeEntry{Name: name, Mode: e.mode, Hash: e.hash})
		for d := dir; d != ""; d = parentDir(d) {
			dirs[d] = true
		}
	}
	// Deepest first, so each subtree exists before its parent.
	order := make([]string, 0, len(dirs))
	for d := range dirs {
		order = append(order, d)
	}
	sort.Slice(order, func(i, j int) bool {
		di, dj := strings.Count(order[i], "/"), strings.Count(order[j], "/")
		if order[i] == "" {
			di = -1
		}
		if order[j] == "" {
			dj = -1
		}
		if di != dj {
			return di > dj
		}
		return order[i] < order[j]
	})
	hashes := map[string]plumbing.Hash{}
	for _, d := range order {
		entries := append([]object.TreeEntry(nil), files[d]...)
		for sub := range dirs {
			if sub != "" && parentDir(sub) == d {
				entries = append(entries, object.TreeEntry{Name: path.Base(sub), Mode: filemode.Dir, Hash: hashes[sub]})
			}
		}
		sort.Sort(object.TreeEntrySorter(entries))
		t := &object.Tree{Entries: entries}
		o := repo.Storer.NewEncodedObject()
		if err := t.Encode(o); err != nil {
			return plumbing.ZeroHash, err
		}
		h, err := repo.Storer.SetEncodedObject(o)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		hashes[d] = h
	}
	return hashes[""], nil
}

func parentDir(d string) string {
	if i := strings.LastIndexByte(d, '/'); i >= 0 {
		return d[:i]
	}
	return ""
}

func writeCommit(repo *git.Repository, tree plumbing.Hash, msg string, author Signature, parents ...plumbing.Hash) (plumbing.Hash, error) {
	now := time.Now()
	c := &object.Commit{
		Author:       object.Signature{Name: author.Name, Email: author.Email, When: now},
		Committer:    object.Signature{Name: committer.Name, Email: committer.Email, When: now},
		Message:      msg,
		TreeHash:     tree,
		ParentHashes: parents,
	}
	o := repo.Storer.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(o)
}
