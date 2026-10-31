package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// Config in git (docs/usage/jam/config-branches.md): with a followed config
// repo, the repo is the only source of the Jam's configuration. Every admin
// API write to a config object is refused; edit on a branch in the admin
// console, or in the repo. Live state stays writable: sessions, grants,
// accounts, OIDC identities, enrollments, standing resets and upgrades, kit
// drafts (until publish) and kits from their own git sources.

// ErrConfigInGit is a live config write on a Jam whose config is in git.
var ErrConfigInGit = writeErr(http.StatusConflict, "config is in git (config.github): edit it on a branch in the admin console, or in the repo")

var (
	configWrite = regexp.MustCompile(`^/admin/(destinations|model-specs|connections|projects|roles|jam/context|kits)(/.*)?$|^/admin/users(/[^/]+(/(logins|name))?)?$|^/admin/config(/managed/.*)?$`)
	liveWrite   = regexp.MustCompile(`^/admin/roles/[^/]+/[^/]+/standing/[^/]+/(reset|upgrade)$|^/admin/kits/import$|^/admin/kits/[^/]+/(sync|source)$|^/admin/kits/[^/]+/draft(/files/.*|/build)?$`)
)

// WithConfigInGit refuses live config writes: the Jam follows followed.
func WithConfigInGit(followed gitsource.Source) AdminOption {
	return func(m *adminMux) { m.inGit, m.followed = true, followed }
}

// configInGitGuard is next with live config writes refused, and an apply
// only of the followed repo.
func configInGitGuard(followed gitsource.Source, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		refuse := false
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
		case p == "/admin/config/apply":
			refuse = !appliesFollowed(r, followed)
		case configWrite.MatchString(p) && !liveWrite.MatchString(p):
			refuse = true
		}
		if refuse {
			http.Error(w, ErrConfigInGit.Error(), http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// appliesFollowed reports whether an apply request names the followed repo
// (a {source} body that is empty or it), leaving the body readable.
func appliesFollowed(r *http.Request, followed gitsource.Source) bool {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		return false // a --dir tree
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigSourceBody+1))
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var b ConfigSourceBody
	if json.Unmarshal(body, &b) != nil {
		return true // the handler answers the bad body
	}
	if b.Source == "" {
		return true
	}
	src, err := gitsource.Parse(b.Source)
	return err == nil && src == followed
}
