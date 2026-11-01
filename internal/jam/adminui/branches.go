package adminui

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/configrepo"
)

// The config branch editor (docs/usage/jam/config-branches.md): a config
// API request carrying BranchHeader is served by a handler over that
// branch's workspace (a scratch store), and a write that succeeds is saved
// as a commit to the branch before it is answered.

// BranchHeader names the branch a config request works on; absent = live.
// HeadHeader is the branch head a write's page read (refused when the
// branch moved on since: another tab saved), and on every branch answer the
// head after it.
const (
	BranchHeader = "X-Jam-Config-Branch"
	HeadHeader   = "X-Jam-Config-Head"
)

// WithConfigBranches turns the branch editor on: b's workspaces, served by
// handlers factory builds (the same Handler call, over a workspace's store,
// without this option).
func WithConfigBranches(b *jam.ConfigBranches, factory func(jam.Store) http.Handler) Option {
	return func(o *options) { o.branches, o.branchHandler = b, factory }
}

// branchPaths are the API paths (after apiPrefix) a branch serves: the
// config objects. Live state under them (a user's accounts, OIDC and app
// tokens; a project's agents; a role's Request; a kit draft's test build)
// a standing session's reset or upgrade; a kit from its own git source:
// import, track, sync) stays live: branchLive.
var (
	branchPaths = []string{"tree", "users", "connections", "projects", "roles", "destinations", "model-specs", "kits", "jam/context"}
	branchLive  = regexp.MustCompile(`^(users/[^/]+/(accounts|oidc|app-tokens)(/.*)?|projects/[^/]+/agents|projects/[^/]+/roles/[^/]+/(request|standing/[^/]+/(reset|upgrade))|kits/import|kits/[^/]+/(sync|source)|kits/[^/]+/draft/build)$`)
)

func onBranchPath(p string) bool {
	rest, ok := strings.CutPrefix(p, apiPrefix)
	if !ok || branchLive.MatchString(rest) {
		return false
	}
	for _, b := range branchPaths {
		if rest == b || strings.HasPrefix(rest, b+"/") {
			return true
		}
	}
	return false
}

// branchRouter serves branch requests from workspace handlers, the rest live.
type branchRouter struct {
	live    http.Handler
	b       *jam.ConfigBranches
	factory func(jam.Store) http.Handler

	mu       sync.Mutex
	handlers map[*jam.ConfigWorkspace]http.Handler
}

func (br *branchRouter) handlerFor(ws *jam.ConfigWorkspace) http.Handler {
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.handlers == nil {
		br.handlers = map[*jam.ConfigWorkspace]http.Handler{}
	}
	h, ok := br.handlers[ws]
	if !ok {
		// One workspace per branch is cached at a time: drop stale handlers.
		for old := range br.handlers {
			if old.Branch == ws.Branch {
				delete(br.handlers, old)
			}
		}
		h = br.factory(ws.Store)
		br.handlers[ws] = h
	}
	return h
}

// operatorSig is the commit author for r's operator.
func operatorSig(r *http.Request) configrepo.Signature {
	id := jam.OperatorID(r)
	if id == "" {
		if p, ok := appui.PrincipalFrom(r); ok {
			id = p.Operator.ID
		}
	}
	if id == "" {
		id = "operator"
	}
	return configrepo.Signature{Name: id, Email: id + "@jam"}
}

func (br *branchRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	branch := strings.TrimSpace(r.Header.Get(BranchHeader))
	write := r.Method != http.MethodGet && r.Method != http.MethodHead
	if branch == "" && write && onBranchPath(r.URL.Path) {
		// The config is in git: it changes on a branch (or in the repo), never live.
		writeAPIError(w, http.StatusConflict, "conflict", "the config is in git: open a branch to edit it")
		return
	}
	if branch == "" || !onBranchPath(r.URL.Path) {
		br.live.ServeHTTP(w, r)
		return
	}
	ws, err := br.b.Workspace(r.Context(), branch)
	if err != nil {
		status, msg := jam.ConfigStatus(err)
		if errors.Is(err, configrepo.ErrFollowedBranch) {
			msg = "the followed branch is read-only; create or open a branch"
		}
		writeAPIError(w, status, apiCode(status), msg)
		return
	}
	ws.Lock()
	defer ws.Unlock()
	if read := r.Header.Get(HeadHeader); write && read != "" && read != ws.HeadNow() {
		w.Header().Set(HeadHeader, ws.HeadNow())
		writeAPIError(w, http.StatusConflict, "stale", "this branch changed since the page was read (another tab or a push); reload it")
		return
	}
	rec := &bufferedWriter{header: http.Header{}, status: http.StatusOK}
	br.handlerFor(ws).ServeHTTP(rec, r)
	if write {
		if rec.status/100 != 2 {
			br.b.Drop(branch) // a failed write may have changed the scratch store
		} else if err := br.b.Save(r.Context(), ws, operatorSig(r), "jam: "+r.Method+" "+strings.TrimPrefix(r.URL.Path, apiPrefix)); err != nil {
			status, emsg := jam.ConfigStatus(err)
			code := apiCode(status)
			if errors.Is(err, configrepo.ErrStale) {
				code = "stale"
			}
			writeAPIError(w, status, code, emsg)
			return
		}
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.Header().Set(HeadHeader, ws.HeadNow())
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}

// bufferedWriter holds a response until the branch save says whether it stands.
type bufferedWriter struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header { return b.header }
func (b *bufferedWriter) WriteHeader(s int) {
	if !b.wrote {
		b.status, b.wrote = s, true
	}
}
func (b *bufferedWriter) Write(p []byte) (int, error) {
	b.wrote = true
	return b.body.Write(p)
}

// apiConfigBranches is the branch bar's API.
func apiConfigBranches(api *http.ServeMux, b *jam.ConfigBranches, log *slog.Logger) {
	api.HandleFunc("GET "+apiPrefix+"config/branches", func(w http.ResponseWriter, r *http.Request) {
		list, err := b.List(r.Context())
		if err != nil {
			branchFail(w, err)
			return
		}
		out := ConfigBranches{Followed: b.Repo.Branch, DefaultName: jam.DefaultBranchName(operatorSig(r).Name), CanPR: b.PRs != nil,
			RepoLink: repoLinkOf(b.Repo.URL), PRNoun: "pull request"}
		if out.RepoLink != "" {
			out.TreeLink = out.RepoLink + "/tree/"
			if _, gitlab := b.PRs.(configrepo.GitLab); gitlab {
				out.TreeLink, out.PRNoun = out.RepoLink+"/-/tree/", "merge request"
			}
		}
		for _, br := range list {
			out.Branches = append(out.Branches, ConfigBranch{Name: br.Name, Head: br.Head, Short: jam.ShortConfigCommit(br.Head), Followed: br.Followed})
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"config/branches", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		if !decodeJSON(w, r, &body) {
			return
		}
		info, err := b.Create(r.Context(), strings.TrimSpace(body.Name))
		if err != nil {
			branchFail(w, err)
			return
		}
		log.Info("ui config branch created", "operator", operatorSig(r).Name, "branch", info.Name)
		writeJSON(w, http.StatusOK, ConfigBranchResult{Branch: ConfigBranch{Name: info.Name, Head: info.Head, Short: jam.ShortConfigCommit(info.Head)},
			Message: "created branch " + info.Name})
	})
	api.HandleFunc("POST "+apiPrefix+"config/branches/merge", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Branch, Head string }
		if !decodeJSON(w, r, &body) {
			return
		}
		res, err := b.Merge(r.Context(), body.Branch, body.Head, operatorSig(r))
		if err != nil {
			branchFail(w, err)
			return
		}
		log.Info("ui config branch merged", "operator", operatorSig(r).Name, "branch", body.Branch, "head", res.Head)
		msg := "merged " + body.Branch + " into " + b.Repo.Branch + "; the Jam applies it now"
		if res.NoOp {
			msg = body.Branch + " has nothing to merge"
		}
		writeJSON(w, http.StatusOK, ConfigMergeResult{Merge: ConfigMerge{Head: jam.ShortConfigCommit(res.Head), FastForward: res.FastForward, NoOp: res.NoOp}, Message: msg})
	})
	api.HandleFunc("POST "+apiPrefix+"config/branches/pr", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Branch string }
		if !decodeJSON(w, r, &body) {
			return
		}
		pr, err := b.PR(r.Context(), body.Branch)
		if err != nil {
			branchFail(w, err)
			return
		}
		log.Info("ui config branch pr", "operator", operatorSig(r).Name, "branch", body.Branch, "pr", pr.Number)
		writeJSON(w, http.StatusOK, ConfigPR{URL: pr.URL})
	})
	api.HandleFunc("GET "+apiPrefix+"config/branches/pr", func(w http.ResponseWriter, r *http.Request) {
		pr, ok, err := b.FindPR(r.Context(), r.URL.Query().Get("branch"))
		if err != nil {
			branchFail(w, err)
			return
		}
		if !ok {
			pr.URL = ""
		}
		writeJSON(w, http.StatusOK, ConfigPR{URL: pr.URL})
	})
}

func branchFail(w http.ResponseWriter, err error) {
	status, msg := jam.ConfigStatus(err)
	writeAPIError(w, status, apiCode(status), msg)
}

// repoLinkOf is the repo's web page for an https GitHub or GitLab URL.
// A local or ssh remote has no web page: "".
func repoLinkOf(repo string) string {
	if !strings.HasPrefix(repo, "https://") && !strings.HasPrefix(repo, "http://") {
		return ""
	}
	return strings.TrimSuffix(repo, ".git")
}
