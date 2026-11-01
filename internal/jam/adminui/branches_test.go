package adminui_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// branchUI is a live Jam following a served repo (users/bob.yml on main,
// branch jam/alice) with the branch editor on.
func branchUI(t *testing.T) (http.Handler, jam.Store, *jam.ConfigBranches, string) {
	t.Helper()
	url, _, _ := gittest.Serve(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}, gittest.WithPush())
	live := newStore(t)
	followed := gitsource.Source{Repo: url, Ref: "main"}
	env := jam.ConfigEnv{CredExists: anyCred, Followed: followed}
	repo := &configrepo.Repo{URL: url, Branch: "main", Path: filepath.Join(t.TempDir(), "repo")}
	b := &jam.ConfigBranches{Repo: repo, Env: env, Live: live}
	if _, err := repo.CreateBranch(context.Background(), "jam/alice"); err != nil {
		t.Fatal(err)
	}
	factory := func(st jam.Store) http.Handler {
		return adminui.Handler(st, testLogger(), nil, nil, anyCred, nil, adminui.WithConfigEnv(env))
	}
	h := adminui.Handler(live, testLogger(), nil, nil, anyCred, nil, adminui.WithConfigEnv(env), adminui.WithConfigBranches(b, factory))
	return h, live, b, url
}

// editBranch is the branch every branchEdit request is served on.
const editBranch = "jam/edit"

// branchEdit is a Jam following a served repo with editBranch open, its
// console editing that branch: config writes are made there, as an
// operator's are. Store is the branch workspace's store.
type branchEdit struct {
	t    *testing.T
	H    http.Handler // serves every request on editBranch
	Live jam.Store
	b    *jam.ConfigBranches
	url  string
}

// editSetup is what editOn builds: the branch holds seed's config (nil:
// none); the console's handlers are built with cred (nil: anyCred), log
// (nil: testLogger) and opts; pool is the serve's subscription pool, as the
// branch's planner sees it.
type editSetup struct {
	seed jam.Store
	cred func(string) bool
	log  *slog.Logger
	opts []adminui.Option
	pool bool
}

// editOn opens editBranch as s sets it up.
func editOn(t *testing.T, s editSetup) *branchEdit {
	t.Helper()
	seed, cred, log, opts := s.seed, s.cred, s.log, slices.Clone(s.opts)
	if cred == nil {
		cred = anyCred
	}
	if log == nil {
		log = testLogger()
	}
	ctx := context.Background()
	url, _, _ := gittest.Serve(t, map[string]string{"README.md": "the Jam's config is under config/\n"}, gittest.WithPush())
	repo := &configrepo.Repo{URL: url, Branch: "main", Path: filepath.Join(t.TempDir(), "repo")}
	head, err := repo.CreateBranch(ctx, editBranch)
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		exp, err := jam.ExportConfigTree(seed.ExportConfig(), nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		ch := configrepo.Changes{Put: map[string]configrepo.File{}}
		for p, c := range exp.Files {
			ch.Put["config/"+p] = configrepo.File{Data: []byte(c), Mode: filemode.Regular}
		}
		for dir, fs := range exp.Kits {
			for _, f := range fs {
				mode := filemode.Regular
				if f.Mode&0o111 != 0 {
					mode = filemode.Executable
				}
				ch.Put["config/"+dir+"/"+f.Path] = configrepo.File{Data: f.Data, Mode: mode}
			}
		}
		if _, err := repo.Commit(ctx, editBranch, head, ch, configrepo.Signature{Name: "seed", Email: "seed@jam"}, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	live := newStore(t)
	env := jam.ConfigEnv{CredExists: cred, PoolConfigured: s.pool, Followed: gitsource.Source{Repo: url, Ref: "main", Dir: "config"}}
	b := &jam.ConfigBranches{Repo: repo, Dir: "config", Env: env, Live: live}
	opts = append([]adminui.Option{adminui.WithConfigEnv(env)}, opts...)
	factory := func(st jam.Store) http.Handler {
		return adminui.Handler(st, log, nil, nil, cred, nil, opts...)
	}
	h := adminui.Handler(live, log, nil, nil, cred, nil, append(opts, adminui.WithConfigBranches(b, factory))...)
	return &branchEdit{t: t, H: modHandler{h, onBranch(editBranch)}, Live: live, b: b, url: url}
}

// Store is the branch's config as the console reads it now.
func (e *branchEdit) Store() jam.Store {
	e.t.Helper()
	ws, err := e.b.Workspace(context.Background(), editBranch)
	if err != nil {
		e.t.Fatal(err)
	}
	return ws.Store
}

// Edit changes the branch's config by hand (a push) with fn.
func (e *branchEdit) Edit(fn func(jam.Store)) {
	e.t.Helper()
	ws, err := e.b.Workspace(context.Background(), editBranch)
	if err != nil {
		e.t.Fatal(err)
	}
	ws.Lock()
	defer ws.Unlock()
	fn(ws.Store)
	if err := e.b.Save(context.Background(), ws, configrepo.Signature{Name: "hand", Email: "hand@jam"}, "by hand"); err != nil {
		e.t.Fatal(err)
	}
}

// apiDoWith is apiDo with the request adjusted by mod first.
func apiDoWith(t *testing.T, h http.Handler, mod func(*http.Request), method, path string, body, out any) *httptest.ResponseRecorder {
	t.Helper()
	return apiDo(t, modHandler{h, mod}, method, path, body, out)
}

type modHandler struct {
	h   http.Handler
	mod func(*http.Request)
}

func (m modHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mod(r); m.h.ServeHTTP(w, r) }

func onBranch(branch string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-Jam-Config-Branch", branch) }
}

func names(us []adminui.UserRow) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.Name)
	}
	return out
}

func TestBranchContextReadsAndWritesTheBranch(t *testing.T) {
	h, live, _, url := branchUI(t)
	var onB, onLive struct{ Users []adminui.UserRow }
	apiDoWith(t, h, onBranch("jam/alice"), "GET", "/app/admin/api/users", nil, &onB)
	if strings.Join(names(onB.Users), ",") != "bob" {
		t.Fatalf("branch users = %v", names(onB.Users))
	}
	apiDo(t, h, "GET", "/app/admin/api/users", nil, &onLive)
	if len(onLive.Users) != 0 {
		t.Fatalf("live users = %v", names(onLive.Users))
	}
	if rec := apiDoWith(t, h, onBranch("jam/alice"), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil); rec.Code != 201 {
		t.Fatalf("create on branch = %d %s", rec.Code, rec.Body)
	}
	if _, ok := gittest.Show(t, url, "jam/alice", "users/carol.yml"); !ok {
		t.Fatal("carol not committed")
	}
	if _, ok := live.LookupName(ident.User, "carol"); ok {
		t.Fatal("carol reached the live Jam")
	}
	if !strings.Contains(gittest.Git(t, url, "log", "-1", "--format=%s", "jam/alice"), "jam: POST users") {
		t.Fatal("commit message")
	}
	wantErr(t, apiDoWith(t, h, onBranch("main"), "GET", "/app/admin/api/users", nil, nil), 400, "bad_request")
	// A non-config path ignores the header.
	if rec := apiDoWith(t, h, onBranch("jam/alice"), "GET", "/app/admin/api/dashboard", nil, nil); rec.Code != 200 {
		t.Fatalf("dashboard = %d", rec.Code)
	}
}

// A push to the branch by hand is picked up before the next write, which
// keeps it.
func TestBranchPicksUpHandPushes(t *testing.T) {
	h, _, b, url := branchUI(t)
	apiDoWith(t, h, onBranch("jam/alice"), "GET", "/app/admin/api/users", nil, nil) // loads the workspace
	head := gittest.Head(t, url, "jam/alice")
	if _, err := b.Repo.Commit(context.Background(), "jam/alice", head, configrepo.Changes{Put: map[string]configrepo.File{"users/dan.yml": {Data: []byte("kind: user\nversion: 1\n"), Mode: 0o100644}}}, configrepo.Signature{Name: "x", Email: "x@x"}, "hand"); err != nil {
		t.Fatal(err)
	}
	if rec := apiDoWith(t, h, onBranch("jam/alice"), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	for _, f := range []string{"users/dan.yml", "users/carol.yml"} {
		if _, ok := gittest.Show(t, url, "jam/alice", f); !ok {
			t.Errorf("%s missing", f)
		}
	}
}

// A write names the head its page read (X-Jam-Config-Head): one on an older
// head (another tab saved since) is refused as stale; every branch answer
// carries the head after it.
func TestBranchWriteOnAStaleHead(t *testing.T) {
	h, _, _, url := branchUI(t)
	rec := apiDoWith(t, h, onBranch("jam/alice"), "GET", "/app/admin/api/users", nil, nil)
	read := rec.Header().Get("X-Jam-Config-Head")
	if read != gittest.Head(t, url, "jam/alice") {
		t.Fatalf("head header %q", read)
	}
	at := func(head string) func(*http.Request) {
		return func(r *http.Request) { onBranch("jam/alice")(r); r.Header.Set("X-Jam-Config-Head", head) }
	}
	rec = apiDoWith(t, h, at(read), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil)
	if rec.Code != 201 || rec.Header().Get("X-Jam-Config-Head") != gittest.Head(t, url, "jam/alice") || rec.Header().Get("X-Jam-Config-Head") == read {
		t.Fatalf("save = %d head %q", rec.Code, rec.Header().Get("X-Jam-Config-Head"))
	}
	// The other tab still has the first head.
	wantErr(t, apiDoWith(t, h, at(read), "POST", "/app/admin/api/users", map[string]string{"name": "dan"}, nil), 409, "stale")
	if _, ok := gittest.Show(t, url, "jam/alice", "users/dan.yml"); ok {
		t.Fatal("a stale write was saved")
	}
}

// Off a branch, config writes are refused: the config is in git.
func TestLiveConfigWritesRefused(t *testing.T) {
	h, live, _, _ := branchUI(t)
	rec := apiDo(t, h, "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil)
	wantErr(t, rec, 409, "conflict")
	if !strings.Contains(rec.Body.String(), "open a branch") {
		t.Fatalf("message %s", rec.Body)
	}
	if _, ok := live.LookupName(ident.User, "carol"); ok {
		t.Fatal("carol written live")
	}
	// Live state stays writable.
	if rec := apiDo(t, h, "POST", "/app/admin/api/users/x/oidc", map[string]string{"oidc": ""}, nil); strings.Contains(rec.Body.String(), "open a branch") {
		t.Fatal("OIDC refused")
	}
}

func TestBranchesAPI(t *testing.T) {
	h, live, _, url := branchUI(t)
	var list adminui.ConfigBranches
	apiDo(t, h, "GET", "/app/admin/api/config/branches", nil, &list)
	if list.Followed != "main" || len(list.Branches) != 2 || list.Branches[0].Name != "main" || list.DefaultName != "jam/operator" || list.CanPR {
		t.Fatalf("list = %+v", list)
	}
	var created adminui.ConfigBranchResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/config/branches", map[string]string{"name": "jam/bob"}, &created); rec.Code != 200 || created.Branch.Name != "jam/bob" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDoWith(t, h, onBranch("jam/bob"), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil); rec.Code != 201 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	head := gittest.Head(t, url, "jam/bob")
	var merged adminui.ConfigMergeResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/config/branches/merge", map[string]string{"branch": "jam/bob", "head": head}, &merged); rec.Code != 200 || !merged.Merge.FastForward {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	if gittest.Head(t, url, "main") != head {
		t.Fatal("main not merged")
	}
	_ = live
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/config/branches/pr", map[string]string{"branch": "jam/bob"}, nil), 501, "internal")
}

// A GitLab repo's branch page and its requests' name.
func TestBranchesAPIGitLab(t *testing.T) {
	_, _, b, _ := branchUI(t)
	b.PRs = configrepo.GitLab{URL: "https://gitlab.com/a/b"}
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithConfigBranches(b, func(jam.Store) http.Handler { return http.NotFoundHandler() }))
	var list adminui.ConfigBranches
	apiDo(t, h, "GET", "/app/admin/api/config/branches", nil, &list)
	if !strings.HasSuffix(list.TreeLink, "/-/tree/") || list.PRNoun != "merge request" || !list.CanPR {
		t.Fatalf("list = %+v", list)
	}
}

// A local repo has no web page and no pull requests.
func TestBranchesAPILocal(t *testing.T) {
	_, _, b, url := branchUI(t)
	bare := filepath.Join(t.TempDir(), "config.git")
	if out, err := exec.Command("git", "clone", "--bare", url, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	b.PRs = nil
	b.Repo.URL = bare
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithConfigBranches(b, func(jam.Store) http.Handler { return http.NotFoundHandler() }))
	var raw map[string]any
	apiDo(t, h, "GET", "/app/admin/api/config/branches", nil, &raw)
	if raw["canPR"] != false || raw["treeLink"] != "" || raw["repoLink"] != "" {
		t.Fatalf("list = %+v", raw)
	}
}

// The draft editor works on a branch: a draft opened, saved and published
// there is committed as the kit's new files.
func TestBranchKitDraftSaveAndPublish(t *testing.T) {
	seed := newStore(t)
	if _, _, err := jam.PushKitFiles(seed, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	e := editOn(t, editSetup{seed: seed})
	var res adminui.DraftResult
	if rec := apiDo(t, e.H, "POST", draftAPI, map[string]int{}, &res); rec.Code != 200 || res.Page.Draft == nil {
		t.Fatalf("open = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDo(t, e.H, "POST", draftAPI+"/save", map[string]any{"etag": res.Page.Draft.ETag, "path": "run.sh", "content": "echo two\n", "executable": true}, &res); rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDo(t, e.H, "POST", draftAPI+"/publish", map[string]any{"etag": res.Page.Draft.ETag}, &res); rec.Code != 200 || res.Page.Published == nil {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	e.b.Drop(editBranch) // read the branch back from the repo
	var d adminui.KitDetail
	apiDo(t, e.H, "GET", "/app/admin/api/kits/app?file=run.sh", nil, &d)
	if d.File == nil || d.File.Content != "echo two\n" {
		t.Fatalf("committed run.sh = %+v", d.File)
	}
	if len(e.Live.ListKits()) != 0 {
		t.Error("the draft reached the live Jam")
	}
}

// localCheckout is a non-bare local config repo (users/bob.yml on main,
// main checked out in it) with jam/alice checked out in a linked worktree,
// and a live Jam following it with the branch editor on.
func localCheckout(t *testing.T) (h http.Handler, dir, worktree string) {
	t.Helper()
	root := t.TempDir()
	dir, worktree = filepath.Join(root, "jam-config"), filepath.Join(root, "alice")
	git := func(cwd string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = cwd
		c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(root, "init", "-q", "-b", "main", dir)
	git(dir, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users", "bob.yml"), []byte("kind: user\nversion: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", "-A")
	git(dir, "commit", "-q", "-m", "init")
	git(dir, "worktree", "add", "-q", "-b", "jam/alice", worktree)
	live := newStore(t)
	env := jam.ConfigEnv{CredExists: anyCred, Followed: gitsource.Source{Repo: dir, Ref: "main"}, FollowedLabel: dir}
	repo := &configrepo.Repo{URL: dir, Branch: "main", Path: filepath.Join(root, "state", "config-repo")}
	b := &jam.ConfigBranches{Repo: repo, Env: env, Live: live}
	factory := func(st jam.Store) http.Handler {
		return adminui.Handler(st, testLogger(), nil, nil, anyCred, nil, adminui.WithConfigEnv(env))
	}
	h = adminui.Handler(live, testLogger(), nil, nil, anyCred, nil, adminui.WithConfigEnv(env), adminui.WithConfigBranches(b, factory))
	return h, dir, worktree
}

// A save onto a branch checked out in a local repo's worktree, and a Merge
// into the followed branch checked out in the repo itself, are refused 409
// naming the branch and the path; nothing is moved.
func TestBranchCheckedOutRefused(t *testing.T) {
	h, dir, worktree := localCheckout(t)
	rec := apiDoWith(t, h, onBranch("jam/alice"), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil)
	e := apiErr(t, rec)
	if want := "jam/alice is checked out in " + worktree + "; check out another branch, or merge it yourself"; rec.Code != 409 || e.Error != want {
		t.Fatalf("save on a checked-out branch = %d %q, want 409 %q", rec.Code, e.Error, want)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/config/branches", map[string]string{"name": "jam/bob"}, nil); rec.Code != 200 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDoWith(t, h, onBranch("jam/bob"), "POST", "/app/admin/api/users", map[string]string{"name": "carol"}, nil); rec.Code != 201 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	var list adminui.ConfigBranches
	apiDo(t, h, "GET", "/app/admin/api/config/branches", nil, &list)
	head := ""
	for _, br := range list.Branches {
		if br.Name == "jam/bob" {
			head = br.Head
		}
	}
	mainBefore := gitRev(t, dir, "main")
	rec = apiDo(t, h, "POST", "/app/admin/api/config/branches/merge", map[string]string{"branch": "jam/bob", "head": head}, nil)
	e = apiErr(t, rec)
	if want := "main is checked out in " + dir + "; check out another branch, or merge it yourself"; rec.Code != 409 || e.Error != want {
		t.Fatalf("merge into a checked-out main = %d %q, want 409 %q", rec.Code, e.Error, want)
	}
	if gitRev(t, dir, "main") != mainBefore {
		t.Error("the refused merge moved main")
	}
}

func apiErr(t *testing.T, rec *httptest.ResponseRecorder) adminui.APIError {
	t.Helper()
	var e adminui.APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body, err)
	}
	return e
}

func gitRev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// draftedBranch is a branch holding kit app with an open draft whose run.sh
// was saved as "echo draft"; it returns the edit and the draft's ETag.
func draftedBranch(t *testing.T) (*branchEdit, string) {
	t.Helper()
	seed := newStore(t)
	if _, _, err := jam.PushKitFiles(seed, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	e := editOn(t, editSetup{seed: seed})
	var res adminui.DraftResult
	if rec := apiDo(t, e.H, "POST", draftAPI, map[string]int{}, &res); rec.Code != 200 || res.Page.Draft == nil {
		t.Fatalf("open = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDo(t, e.H, "POST", draftAPI+"/save", map[string]any{"etag": res.Page.Draft.ETag, "path": "run.sh", "content": "echo draft\n", "executable": true}, &res); rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	return e, res.Page.Draft.ETag
}

// draftOf is the branch's open draft of app, nil when there is none.
func draftOf(t *testing.T, e *branchEdit) *adminui.DraftView {
	t.Helper()
	var p adminui.DraftPage
	if rec := apiDo(t, e.H, "GET", draftAPI, nil, &p); rec.Code != 200 {
		t.Fatalf("draft = %d %s", rec.Code, rec.Body)
	}
	return p.Draft
}

// push commits files to the branch behind the workspace's back (a hand push).
func (e *branchEdit) push(files map[string]string) {
	e.t.Helper()
	ctx := context.Background()
	bs, err := e.b.Repo.Branches(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	head := ""
	for _, b := range bs {
		if b.Name == editBranch {
			head = b.Head
		}
	}
	ch := configrepo.Changes{Put: map[string]configrepo.File{}}
	for p, c := range files {
		ch.Put[p] = configrepo.File{Data: []byte(c), Mode: filemode.Regular}
	}
	if _, err := e.b.Repo.Commit(ctx, editBranch, head, ch, configrepo.Signature{Name: "hand", Email: "hand@jam"}, "by hand"); err != nil {
		e.t.Fatal(err)
	}
}

// A refused write that changed nothing keeps the workspace: an open draft
// survives both a refused config write and a stale draft save.
func TestBranchRefusedWriteKeepsDraft(t *testing.T) {
	e, etag := draftedBranch(t)
	if rec := apiDo(t, e.H, "POST", "/app/admin/api/users", map[string]any{"name": "Not A Name!"}, nil); rec.Code/100 != 4 {
		t.Fatalf("bad user = %d %s", rec.Code, rec.Body)
	}
	if d := draftOf(t, e); d == nil {
		t.Fatal("the draft was lost to a refused config write")
	}
	if rec := apiDo(t, e.H, "POST", draftAPI+"/save", map[string]any{"etag": "stale", "path": "run.sh", "content": "echo other\n", "executable": true}, nil); rec.Code != 412 {
		t.Fatalf("stale save = %d %s", rec.Code, rec.Body)
	}
	if d := draftOf(t, e); d == nil || d.ETag != etag {
		t.Fatalf("draft after a stale save = %+v, want it kept at %s", d, etag)
	}
}

// A reload (the branch moved: a hand push) carries an open draft over when
// its kit is unchanged on the branch, and drops it when the kit changed.
func TestBranchReloadCarriesDraft(t *testing.T) {
	e, etag := draftedBranch(t)
	e.push(map[string]string{"config/users/carol.yml": "kind: user\nversion: 1\n"})
	d := draftOf(t, e)
	if d == nil || d.ETag != etag {
		t.Fatalf("draft after a push elsewhere = %+v, want it carried over at %s", d, etag)
	}
	var res adminui.DraftResult
	if rec := apiDo(t, e.H, "POST", draftAPI+"/publish", map[string]any{"etag": etag}, &res); rec.Code != 200 || res.Page.Published == nil {
		t.Fatalf("publish of the carried draft = %d %s", rec.Code, rec.Body)
	}
}

func TestBranchReloadDropsDraftOfChangedKit(t *testing.T) {
	e, _ := draftedBranch(t)
	e.push(map[string]string{"config/kits/app/kit.yml": "kind: studio\nprompt: changed by hand\n"})
	if d := draftOf(t, e); d != nil {
		t.Fatalf("draft = %+v, want it dropped: its kit changed on the branch", d)
	}
}

// A save that would leave the branch unable to load commits nothing.
func TestBranchUnloadableSaveRefused(t *testing.T) {
	seed := newStore(t)
	mustCreateProject(t, seed, "acme")
	e := editOn(t, editSetup{seed: seed})
	before := e.headOf()
	rec := apiDo(t, e.H, "POST", "/app/admin/api/projects/acme/escalation", map[string]string{"category": "", "tiers": "user:ghost@15m"}, nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "not saved: the branch would no longer load") {
		t.Fatalf("unloadable save = %d %s", rec.Code, rec.Body)
	}
	if after := e.headOf(); after != before {
		t.Fatalf("branch moved %s → %s on a refused save", before, after)
	}
	var d adminui.ProjectDetail
	apiDo(t, e.H, "GET", "/app/admin/api/projects/acme", nil, &d)
	if len(d.Escalation) != 0 {
		t.Errorf("the refused chain is in the workspace: %+v", d.Escalation)
	}
}

// headOf is the branch's head in the repo.
func (e *branchEdit) headOf() string {
	e.t.Helper()
	bs, err := e.b.Repo.Branches(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, b := range bs {
		if b.Name == editBranch {
			return b.Head
		}
	}
	return ""
}
