package adminui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
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
