package agentrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// ConnectorSource fetches the studio's current connector (GET /connector).
type ConnectorSource interface {
	Fetch(ctx context.Context) (snippet.Connector, error)
}

// GitRouter rewrites the global git config when the connector's git route
// changes: https://github.com/ → baseURL+newRoute (no rewrite when newRoute is
// ""), dropping oldRoute's rewrite; the credential helper is kept iff newRoute != "".
type GitRouter interface {
	Route(baseURL, oldRoute, newRoute string) error
}

// ConnectorConfig turns on the per-turn connector refresh. Nil Config.Connector
// (an older launcher that sets no AT_JAM_BASE_URL) keeps today's behavior: every
// spawn inherits cove-master's env.
type ConnectorConfig struct {
	Source  ConnectorSource
	Git     GitRouter // nil → execGit
	BaseURL string    // https://<jam host>
	Token   string    // the identity token; expanded in memory, never logged
	Initial snippet.Connector
	Environ func() []string // nil → os.Environ
}

// HTTPConnectorSource is the production ConnectorSource: snippet.Fetch through
// the cove's proxy (http.ProxyFromEnvironment via the default transport) with
// the system trust store.
func HTTPConnectorSource(baseURL, token string) ConnectorSource {
	return httpSource{hc: &http.Client{Timeout: 10 * time.Second}, base: baseURL, token: token}
}

type httpSource struct {
	hc          *http.Client
	base, token string
}

func (s httpSource) Fetch(ctx context.Context) (snippet.Connector, error) {
	return snippet.Fetch(s.hc, s.base, s.token)
}

// connectorRefresher applies the current connector before each agent spawn. It
// is single-goroutine (Run's turn loop) — no locking.
type connectorRefresher struct {
	cfg      ConnectorConfig
	log      *slog.Logger
	last     snippet.Connector // last applied (initially the raise-time one)
	route    string            // git route currently configured
	reported string            // last fingerprint reported up
}

func newConnectorRefresher(cfg ConnectorConfig, log *slog.Logger) *connectorRefresher {
	if cfg.Git == nil {
		cfg.Git = execGit{}
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &connectorRefresher{cfg: cfg, log: log, last: cfg.Initial, route: cfg.Initial.GitRoute}
}

// prepare fetches the current connector (falling back to the last applied one
// on any error), applies its git route if changed, and returns the spawn env,
// the applied connector's fingerprint, and whether that fingerprint is new
// since the last report. Logs carry key counts and fingerprints, never values.
func (r *connectorRefresher) prepare(ctx context.Context) (env []string, fp string, changed bool) {
	cur, err := r.cfg.Source.Fetch(ctx)
	if err != nil {
		r.log.Warn("agentrun: connector refresh failed; using the last applied connector", "err", err.Error())
		cur = r.last
	} else if cur.GitRoute != r.route {
		if gerr := r.cfg.Git.Route(r.cfg.BaseURL, r.route, cur.GitRoute); gerr != nil {
			// Env still applies; the route is retried next turn (r.route unchanged).
			r.log.Warn("agentrun: git route refresh failed", "err", gerr.Error())
		} else {
			r.route = cur.GitRoute
		}
	}
	env = r.spawnEnv(cur)
	r.last = cur
	fp = snippet.Fingerprint(cur)
	if fp != r.reported {
		r.log.Info("agentrun: connector applied", "fingerprint", fp[:12], "env_keys", len(cur.Env))
		r.reported, changed = fp, true
	}
	return env, fp, changed
}

// spawnEnv is cove-master's env minus every key the raise-time (Initial) or
// previous connector owned, or the current one sets (incl. the identity-token vars Expand re-emits with
// the same value), plus the current connector expanded in memory — so a key a
// destination dropped is gone, and no key appears twice.
func (r *connectorRefresher) spawnEnv(cur snippet.Connector) []string {
	exp := cur.Expand(r.cfg.BaseURL, r.cfg.Token)
	owned := map[string]bool{}
	// Initial's keys are always owned: cove-master's process env carries the
	// raise-time values for its whole lifetime, so a key dropped later must stay
	// stripped on every subsequent turn, not just the turn it was dropped.
	for k := range r.cfg.Initial.Env {
		owned[k] = true
	}
	for k := range r.last.Env {
		owned[k] = true
	}
	for k := range exp {
		owned[k] = true
	}
	var env []string
	for _, kv := range r.cfg.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !owned[k] {
			env = append(env, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(exp)) {
		env = append(env, k+"="+exp[k])
	}
	return env
}

// execGit is the production GitRouter: `git config --global` via argv (no shell),
// mirroring what snippet.Connector.GitConfig renders at raise.
type execGit struct{}

func (execGit) Route(base, oldRoute, newRoute string) error {
	base = strings.TrimRight(base, "/")
	if oldRoute != "" {
		// Exit 5 = key absent: fine, nothing to remove.
		if err := exec.Command("git", "config", "--global", "--unset-all", "url."+base+oldRoute+".insteadOf").Run(); err != nil && !isExit(err, 5) {
			return err
		}
	}
	if newRoute == "" {
		if err := exec.Command("git", "config", "--global", "--unset-all", "credential."+base+".helper").Run(); err != nil && !isExit(err, 5) {
			return err
		}
		return nil
	}
	if err := exec.Command("git", "config", "--global", "url."+base+newRoute+".insteadOf", "https://github.com/").Run(); err != nil {
		return err
	}
	return exec.Command("git", "config", "--global", "credential."+base+".helper", snippet.GitHelper()).Run()
}

func isExit(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}
