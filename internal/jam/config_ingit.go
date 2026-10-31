package jam

import (
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
	configWrite = regexp.MustCompile(`^/admin/(destinations|model-specs|connections|projects|roles|jam/context|kits|users)(/.*)?$|^/admin/config(/managed/.*)?$`)
	liveWrite   = regexp.MustCompile(`^/admin/roles/[^/]+/[^/]+/standing/[^/]+/(reset|upgrade)$|^/admin/users/[^/]+/oidc$|^/admin/kits/[^/]+/(sync|source)$|^/admin/kits/[^/]+/draft(/files/.*|/build)?$`)
)

// WithConfigInGit refuses live config writes: the Jam follows followed.
func WithConfigInGit(followed gitsource.Source) AdminOption {
	return func(m *adminMux) { m.inGit, m.followed = true, followed }
}

// configInGitGuard is next with live config writes refused. An apply of
// anything but the followed repo is refused by WithConfig itself, on the
// source it parsed (refusedApply).
func configInGitGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		refuse := false
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
		case p == "/admin/kits/import" && r.Method == http.MethodPost: // a kit from its own git source
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

// refusedApply reports whether, with config in git (env.Followed set), an
// apply of src is a live edit: a --dir tree, or a source not the followed repo.
func refusedApply(env ConfigEnv, tree bool, src gitsource.Source) bool {
	if env.Followed.Repo == "" {
		return false
	}
	return tree || (src.Repo != "" && src != env.Followed)
}
