package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ContextSource fetches the session's current context bundle (GET /context).
type ContextSource interface {
	Fetch(ctx context.Context) (sessionctx.Bundle, error)
}

// HTTPContextSource is the production ContextSource: GET <base>/context with the
// identity bearer, through the cove's proxy (http.ProxyFromEnvironment via the
// default transport).
func HTTPContextSource(baseURL, token string) ContextSource {
	return httpContextSource{hc: &http.Client{Timeout: 10 * time.Second}, base: baseURL, token: token}
}

type httpContextSource struct {
	hc          *http.Client
	base, token string
}

func (s httpContextSource) Fetch(ctx context.Context) (sessionctx.Bundle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.base, "/")+"/context", nil)
	if err != nil {
		return sessionctx.Bundle{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.hc.Do(req)
	if err != nil {
		return sessionctx.Bundle{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return sessionctx.Bundle{}, fmt.Errorf("GET /context: %s", resp.Status)
	}
	var b sessionctx.Bundle
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return sessionctx.Bundle{}, fmt.Errorf("GET /context: %w", err)
	}
	return b, nil
}

// contextRefresher re-fetches the bundle and, when it changed, rewrites the
// context dir. Single-goroutine (Run's loop and its episode) — no locking.
type contextRefresher struct {
	src  ContextSource
	last sessionctx.Bundle
	dir  string
	log  *slog.Logger
}

func newContextRefresher(src ContextSource, initial sessionctx.Bundle, dir string, log *slog.Logger) *contextRefresher {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &contextRefresher{src: src, last: initial, dir: dir, log: log}
}

// refresh returns the layers that changed and were written; nil when nothing
// changed or the refresh failed (the last bundle stays in effect). Logs carry
// fingerprints and layer names, never content.
func (r *contextRefresher) refresh(ctx context.Context) []string {
	cur, err := r.src.Fetch(ctx)
	if err != nil {
		r.log.Warn("agentrun: session context refresh failed; keeping the last bundle", "err", err.Error())
		return nil
	}
	changed := sessionctx.ChangedLayers(r.last, cur)
	if len(changed) == 0 {
		return nil
	}
	if err := writeContext(r.dir, cur); err != nil {
		r.log.Warn("agentrun: session context not rewritten; keeping the last bundle", "err", err.Error())
		return nil
	}
	r.last = cur
	r.log.Info("agentrun: session context refreshed", "fingerprint", short(cur.Fingerprint), "layers", strings.Join(changed, ","))
	return changed
}

// contextNotice is the line added to what the agent is sent when its context
// changed; live = written into a running episode (system prompt still old).
func contextNotice(changed []string, live bool) string {
	if len(changed) == 0 {
		return ""
	}
	layers := strings.Join(changed, ", ")
	if live {
		return "\n\nSession context changed (" + layers + ") — re-read /agent-data/context/CORE.md now; your system prompt catches up at your next episode."
	}
	return "\n\nSession context changed (" + layers + ") since your last turn — your system prompt is current; re-open any leaf you rely on."
}
