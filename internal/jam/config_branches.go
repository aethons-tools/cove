package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	Head   string // read with HeadNow while requests may run
	Store  *MemStore

	sync.Mutex // one request at a time on the workspace

	headMu sync.Mutex       // guards Head
	base   ConfigTreeExport // the rendering at Head: what a save diffs against
}

// HeadNow is the workspace's head.
func (ws *ConfigWorkspace) HeadNow() string {
	ws.headMu.Lock()
	defer ws.headMu.Unlock()
	return ws.Head
}

func (ws *ConfigWorkspace) setHead(h string) {
	ws.headMu.Lock()
	ws.Head = h
	ws.headMu.Unlock()
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
	if ws, ok := b.workspaces[branch]; ok && ws.HeadNow() == head {
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
	base, err := renderWorkspace(scratch)
	if err != nil {
		return nil, err
	}
	return &ConfigWorkspace{Branch: branch, Head: head, Store: scratch, base: base}, nil
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

// Save commits ws's store back to its branch: the files whose rendering
// changed since the workspace was loaded or last saved (ExportConfigTree,
// every object), put or removed. Files it didn't render differently stay as
// the branch has them (hand edits, comments), as do files outside the
// config dir and jam.yml. No change, no commit. A branch that moved since ws
// was read is configrepo.ErrStale; any failure drops ws (its store may hold
// the unsaved edit).
func (b *ConfigBranches) Save(ctx context.Context, ws *ConfigWorkspace, author configrepo.Signature, msg string) error {
	exp, err := renderWorkspace(ws.Store)
	if err == nil {
		var head string
		if head, err = b.Repo.Commit(ctx, ws.Branch, ws.HeadNow(), b.diff(ws.base, exp), author, msg); err == nil {
			ws.setHead(head)
			ws.base = exp
			return nil
		}
	}
	b.Drop(ws.Branch)
	return err
}

// renderWorkspace is st's config tree as the repo holds it. No serve config:
// the repo's jam.yml is compare-only, never edited from a branch.
func renderWorkspace(st *MemStore) (ConfigTreeExport, error) {
	return ExportConfigTree(st.configSnapshot(), st.ListConfigMarks(), nil, true)
}

// diff is what changed from the rendering before to after, as repo paths.
func (b *ConfigBranches) diff(before, after ConfigTreeExport) configrepo.Changes {
	ch := configrepo.Changes{Put: map[string]configrepo.File{}}
	full := func(rel string) string { return path.Join(b.Dir, rel) }
	for p, c := range after.Files {
		if old, ok := before.Files[p]; !ok || old != c {
			ch.Put[full(p)] = configrepo.File{Data: []byte(c), Mode: filemode.Regular}
		}
	}
	for p := range before.Files {
		if _, ok := after.Files[p]; !ok {
			ch.Delete = append(ch.Delete, full(p))
		}
	}
	kitFiles := func(fs []ConfigKitFile) map[string]ConfigKitFile {
		m := make(map[string]ConfigKitFile, len(fs))
		for _, f := range fs {
			m[f.Path] = f
		}
		return m
	}
	mode := func(m uint32) filemode.FileMode {
		if m&0o111 != 0 {
			return filemode.Executable
		}
		return filemode.Regular
	}
	for dir, fs := range after.Kits {
		old := kitFiles(before.Kits[dir])
		for _, f := range fs {
			if o, ok := old[f.Path]; !ok || !bytes.Equal(o.Data, f.Data) || o.Mode != f.Mode {
				ch.Put[full(dir+"/"+f.Path)] = configrepo.File{Data: f.Data, Mode: mode(f.Mode)}
			}
		}
	}
	for dir, fs := range before.Kits {
		now := kitFiles(after.Kits[dir])
		for _, f := range fs {
			if _, ok := now[f.Path]; !ok {
				ch.Delete = append(ch.Delete, full(dir+"/"+f.Path))
			}
		}
	}
	return ch
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

// ConfigBranchInfo is one branch for the editor's branch list.
type ConfigBranchInfo struct {
	Name     string `json:"name"`
	Head     string `json:"head"`
	Followed bool   `json:"followed"` // the branch the Jam follows: read-only
}

// List is the followed branch, then the others by name.
func (b *ConfigBranches) List(ctx context.Context) ([]ConfigBranchInfo, error) {
	bs, err := b.Repo.Branches(ctx)
	if err != nil {
		return nil, err
	}
	out := []ConfigBranchInfo{}
	var rest []ConfigBranchInfo
	for _, br := range bs {
		info := ConfigBranchInfo{Name: br.Name, Head: br.Head, Followed: br.Name == b.Repo.Branch}
		if info.Followed {
			out = append(out, info)
		} else {
			rest = append(rest, info)
		}
	}
	return append(out, rest...), nil
}

// Create makes branch name at the followed head.
func (b *ConfigBranches) Create(ctx context.Context, name string) (ConfigBranchInfo, error) {
	head, err := b.Repo.CreateBranch(ctx, name)
	if err != nil {
		return ConfigBranchInfo{}, err
	}
	return ConfigBranchInfo{Name: name, Head: head}, nil
}

// DefaultBranchName is an operator's own branch: jam/<operator>, lowercased,
// anything outside [a-z0-9._-] a dash.
func DefaultBranchName(operator string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(operator) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	name := strings.Trim(sb.String(), "-.")
	if name == "" {
		name = "operator"
	}
	return "jam/" + name
}

// Merge merges branch (at head) into the followed branch, refusing a merge
// whose config would not plan against the live Jam (ErrConfigRefused, with
// the refusals; nothing pushed). The follower then applies it at once.
func (b *ConfigBranches) Merge(ctx context.Context, branch, head string, author configrepo.Signature) (configrepo.MergeResult, error) {
	check := func(files map[string]configrepo.File) error {
		tree, err := b.treeOf(files)
		if err != nil {
			return err
		}
		res, err := ApplyConfigTree(ctx, b.Live, b.Fetcher, tree, b.planEnv(), ConfigApplyOptions{DryRun: true, Force: true, Source: b.Env.Followed, Now: time.Now()}, nil, "merge:"+branch)
		if err != nil {
			return err
		}
		if len(res.Plan.Refusals) > 0 {
			return fmt.Errorf("%w:\n%s", ErrConfigRefused, strings.Join(res.Plan.Refusals, "\n"))
		}
		return nil
	}
	res, err := b.Repo.Merge(ctx, branch, head, author, "jam: merge "+branch, check)
	if err != nil {
		return res, err
	}
	b.Drop(branch)
	if b.Log != nil {
		b.Log.Info("config branch merged", "operator", author.Name, "branch", branch, "head", res.Head, "fastForward", res.FastForward, "noOp", res.NoOp)
	}
	if f := b.Env.Follower; f != nil && !res.NoOp {
		go f.Tick(context.WithoutCancel(ctx)) // applies the merge now, not within the minute
	}
	return res, nil
}

// errNoPRs is a PR on a Jam whose config repo is not on GitHub.
var errNoPRs = writeErr(http.StatusNotImplemented, "pull requests need config.github")

// PR opens (or finds the open) pull request from branch into the followed branch.
func (b *ConfigBranches) PR(ctx context.Context, branch string) (configrepo.PullRequest, error) {
	if b.GitHub == nil {
		return configrepo.PullRequest{}, errNoPRs
	}
	if branch == b.Repo.Branch {
		return configrepo.PullRequest{}, configrepo.ErrFollowedBranch
	}
	return b.GitHub.OpenPR(ctx, branch, b.Repo.Branch, "Jam config: "+branch,
		"Config changes made in the Jam admin console on branch `"+branch+"`.")
}

// FindPR is branch's open pull request, if any (none without GitHub).
func (b *ConfigBranches) FindPR(ctx context.Context, branch string) (configrepo.PullRequest, bool, error) {
	if b.GitHub == nil {
		return configrepo.PullRequest{}, false, nil
	}
	return b.GitHub.FindPR(ctx, branch, b.Repo.Branch)
}
