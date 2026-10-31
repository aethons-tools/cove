package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/jam/configrepo"
)

// The config branch editor (docs/usage/jam/config-branches.md): the admin
// console edits a branch of the followed config repo. A branch's workspace
// is a scratch MemStore holding its config tree, so the console's existing
// forms edit it; Save writes the scratch store back as one commit.

// ConfigBranches is the followed repo's branches as the editor sees them.
type ConfigBranches struct {
	Repo    *configrepo.Repo
	GitHub  *configrepo.GitHub // nil = no pull requests
	Dir     string             // the config dir in the repo ("" = its root)
	Fetcher KitFetcher         // fetches .source.yml kits while a branch loads
	Env     ConfigEnv          // the live planner's env (Followed, Follower)
	Live    Store
	Log     *slog.Logger

	mu         sync.Mutex
	workspaces map[string]*ConfigWorkspace
}

// ConfigWorkspace is one branch at Head, loaded into Store. A save moves
// Head; callers serialize use of one workspace with its Lock.
type ConfigWorkspace struct {
	Branch string
	Head   string
	Store  *MemStore

	sync.Mutex
}

// Workspace is branch's workspace: the cached one while the branch is still
// at its head, else loaded afresh. A branch whose tree does not plan is a
// 409 naming its refusals.
func (b *ConfigBranches) Workspace(ctx context.Context, branch string) (*ConfigWorkspace, error) {
	if branch == b.Repo.Branch {
		return nil, configrepo.ErrFollowedBranch
	}
	heads, err := b.Repo.Branches(ctx)
	if err != nil {
		return nil, err
	}
	head := ""
	for _, h := range heads {
		if h.Name == branch {
			head = h.Head
		}
	}
	if head == "" {
		return nil, configrepo.ErrNoBranch
	}
	b.mu.Lock()
	if ws, ok := b.workspaces[branch]; ok && ws.Head == head {
		b.mu.Unlock()
		return ws, nil
	}
	b.mu.Unlock()
	ws, err := b.load(ctx, branch)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.workspaces == nil {
		b.workspaces = map[string]*ConfigWorkspace{}
	}
	b.workspaces[branch] = ws
	return ws, nil
}

// Drop forgets branch's cached workspace.
func (b *ConfigBranches) Drop(branch string) {
	b.mu.Lock()
	delete(b.workspaces, branch)
	b.mu.Unlock()
}

// planEnv is Env without the follower: a branch's plans never touch it.
func (b *ConfigBranches) planEnv() ConfigEnv {
	env := b.Env
	env.Follower, env.Conditions = nil, nil
	return env
}

// load reads branch into a fresh scratch store.
func (b *ConfigBranches) load(ctx context.Context, branch string) (*ConfigWorkspace, error) {
	head, files, err := b.Repo.Files(ctx, branch)
	if err != nil {
		return nil, err
	}
	tree, err := b.treeOf(files)
	if err != nil {
		return nil, err
	}
	scratch := NewMemStore()
	src := b.Env.Followed
	src.Ref = branch
	_, err = ApplyConfigTree(ctx, scratch, b.Fetcher, tree, b.planEnv(), ConfigApplyOptions{Source: src, Commit: head, Force: true, Now: time.Now()}, nil, "branch:"+branch)
	if errors.Is(err, ErrConfigRefused) {
		return nil, writeErr(http.StatusConflict, "branch %s does not plan: %s", branch, strings.TrimPrefix(err.Error(), ErrConfigRefused.Error()+":\n"))
	}
	if err != nil {
		return nil, err
	}
	return &ConfigWorkspace{Branch: branch, Head: head, Store: scratch}, nil
}

// treeOf loads the config tree from files' config dir.
func (b *ConfigBranches) treeOf(files map[string]configrepo.File) (*ConfigTree, error) {
	tmp, err := os.MkdirTemp("", "jam-branch-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := b.materialize(tmp, files); err != nil {
		return nil, err
	}
	return LoadConfigTree(tmp)
}

// configPath is p relative to the config dir; ok=false outside it.
func (b *ConfigBranches) configPath(p string) (string, bool) {
	if b.Dir == "" {
		return p, true
	}
	rel, ok := strings.CutPrefix(p, b.Dir+"/")
	return rel, ok
}

// materialize writes files' config-dir files into dir at their modes.
func (b *ConfigBranches) materialize(dir string, files map[string]configrepo.File) error {
	for p, f := range files {
		rel, ok := b.configPath(p)
		if !ok {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if !strings.HasPrefix(full, dir+string(filepath.Separator)) {
			return fmt.Errorf("config branch: unsafe path %q", p)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.Mode == filemode.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(full, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

// Save commits ws's store back to its branch: the store's config tree
// written over the branch's files (WriteConfigTree, as config write does),
// and the files that changed or went. No change, no commit. A branch that
// moved since ws was read is configrepo.ErrStale, and ws is dropped.
func (b *ConfigBranches) Save(ctx context.Context, ws *ConfigWorkspace, author configrepo.Signature, msg string) error {
	// No serve config: the repo's jam.yml is compare-only, never edited here.
	exp, err := ExportConfigTree(ws.Store.configSnapshot(), ws.Store.ListConfigMarks(), nil, true)
	if err != nil {
		return err
	}
	_, files, err := b.Repo.Files(ctx, ws.Branch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "jam-branch-save-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := b.materialize(tmp, files); err != nil {
		return err
	}
	if err := WriteConfigTree(tmp, exp); err != nil {
		return err
	}
	ch, err := b.changes(tmp, files)
	if err != nil {
		return err
	}
	head, err := b.Repo.Commit(ctx, ws.Branch, ws.Head, ch, author, msg)
	if errors.Is(err, configrepo.ErrStale) {
		b.Drop(ws.Branch)
		return err
	}
	if err != nil {
		return err
	}
	ws.Head = head
	return nil
}

// changes compares the written dir with the branch's config-dir files.
func (b *ConfigBranches) changes(dir string, files map[string]configrepo.File) (configrepo.Changes, error) {
	ch := configrepo.Changes{Put: map[string]configrepo.File{}}
	full := func(rel string) string { return path.Join(b.Dir, rel) }
	seen := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := filemode.Regular
		if info.Mode()&0o111 != 0 {
			mode = filemode.Executable
		}
		if old, ok := files[full(rel)]; !ok || !bytes.Equal(old.Data, data) || old.Mode != mode {
			ch.Put[full(rel)] = configrepo.File{Data: data, Mode: mode}
		}
		return nil
	})
	if err != nil {
		return ch, err
	}
	for p := range files {
		if rel, ok := b.configPath(p); ok && !seen[rel] {
			ch.Delete = append(ch.Delete, p)
		}
	}
	return ch, nil
}

// configBranchStatus maps the configrepo errors (and conflicts) for ConfigStatus.
func configBranchStatus(err error) (int, string, bool) {
	var ce *configrepo.ConflictError
	switch {
	case errors.Is(err, configrepo.ErrStale):
		return http.StatusConflict, err.Error(), true
	case errors.Is(err, configrepo.ErrNoBranch):
		return http.StatusNotFound, err.Error(), true
	case errors.Is(err, configrepo.ErrBranchExists):
		return http.StatusConflict, err.Error(), true
	case errors.Is(err, configrepo.ErrFollowedBranch):
		return http.StatusBadRequest, err.Error(), true
	case errors.As(err, &ce):
		return http.StatusConflict, "Merge refused: " + ce.Error() + ". Open a pull request instead.", true
	case errors.Is(err, configrepo.ErrRemote):
		return http.StatusBadGateway, err.Error(), true
	}
	return 0, "", false
}
