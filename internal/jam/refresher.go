package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// Probed 2026-09-29 (see the design spec's "Refresh endpoint" section): the
// subscription-OAuth refresh request claude emits.
const (
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultScope    = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
)

// RefresherOptions configures the broker-owned pool token refresher. TokenURL,
// ClientID, and Scope default to the probed constants above.
type RefresherOptions struct {
	HTTPClient *http.Client
	TokenURL   string
	ClientID   string
	Scope      string
	Now        func() time.Time
	Margin     time.Duration // refresh when ExpiresAt is within this of Now
	Log        *slog.Logger
	// Conditions, when set, receives pool.account.refresh:<account> conditions
	// (warning; critical while every account is failing). nil = none.
	Conditions *condition.Tracker
}

// Refresher rotates pool account tokens ahead of expiry, out-of-band from any
// cove. It never logs token values.
type Refresher struct {
	store PoolStore
	opt   RefresherOptions
}

// NewRefresher builds a Refresher, filling unset options with defaults.
func NewRefresher(store PoolStore, opt RefresherOptions) *Refresher {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = http.DefaultClient
	}
	if opt.Margin == 0 {
		opt.Margin = 15 * time.Minute
	}
	if opt.TokenURL == "" {
		opt.TokenURL = defaultTokenURL
	}
	if opt.ClientID == "" {
		opt.ClientID = defaultClientID
	}
	if opt.Scope == "" {
		opt.Scope = defaultScope
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &Refresher{store: store, opt: opt}
}

// PoolFailThreshold is how many consecutive failed refresh passes of one
// account raise pool.account.refresh:<account>.
const PoolFailThreshold = 2

// RefreshDue refreshes every account whose ExpiresAt is within Margin of Now.
// Per-account failures are logged and do not stop the others; with
// Conditions set they raise pool.account.refresh:<account> (warning, or
// critical while every account is failing) and a success, a fresh token (re-seed) or removal of the account clears it. Never
// logs tokens.
func (r *Refresher) RefreshDue(ctx context.Context) error {
	accts, err := r.store.Accounts()
	if err != nil {
		return err
	}
	deadline := r.opt.Now().Add(r.opt.Margin)
	t := r.opt.Conditions
	for _, a := range accts {
		key := condition.Key("pool.account.refresh", a.Name)
		if a.ExpiresAt.After(deadline) {
			t.Ok(key) // a not-due account holds a fresh token (e.g. re-seeded)
			continue
		}
		if err := r.refreshOne(ctx, a); err != nil {
			r.opt.Log.Warn("pool token refresh failed", "account", a.Name, "error", err.Error())
			sev := condition.Warning
			if cur, ok := t.Get(key); ok {
				sev = cur.Severity // keep it; the all-failing pass below decides
			}
			t.Fail(condition.Condition{
				Key: key, Severity: sev,
				Summary: "pool account " + a.Name + " cannot refresh its token",
				Detail:  err.Error(), // OAuth error code + description only (see refreshOne)
				Fix:     "re-seed it: at-jam pool add --name " + a.Name + " --from-file <credentials.json>",
			}, PoolFailThreshold)
			continue
		}
		t.Ok(key)
	}
	// Accounts no longer in the store can never refresh again: drop their conditions.
	if t != nil {
		live := map[string]bool{}
		for _, a := range accts {
			live[condition.Key("pool.account.refresh", a.Name)] = true
		}
		for _, k := range t.OpenKeys("pool.account.refresh") {
			if !live[k] {
				t.Ok(k)
			}
		}
	}
	// Every account failing means the pool cannot serve: critical; otherwise warning.
	if t != nil && len(accts) > 0 {
		all := true
		for _, a := range accts {
			if !t.IsOpen(condition.Key("pool.account.refresh", a.Name)) {
				all = false
				break
			}
		}
		want := condition.Warning
		if all {
			want = condition.Critical
		}
		for _, a := range accts {
			if c, ok := t.Get(condition.Key("pool.account.refresh", a.Name)); ok && c.Severity != want {
				c.Severity = want
				t.Raise(c)
			}
		}
	}
	return nil
}

func (r *Refresher) refreshOne(ctx context.Context, a PoolAccount) error {
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": a.RefreshToken,
		"client_id":     r.opt.ClientID,
		"scope":         r.opt.Scope,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opt.TokenURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.opt.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// An OAuth error response carries a machine-readable `error` (and often
		// `error_description`) — neither is a secret, and both are what makes a
		// failure diagnosable. Parse just those, bounded, and never echo the raw
		// body (a 2xx body would carry tokens; an error body does not, but stay
		// strict). See RFC 6749 §5.2.
		var oe struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = json.Unmarshal(body, &oe)
		if oe.Error != "" {
			return fmt.Errorf("token endpoint %d: %s: %s", resp.StatusCode, oe.Error, oe.Description)
		}
		return fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if body.AccessToken == "" {
		return fmt.Errorf("token endpoint returned no access_token")
	}
	a.AccessToken = body.AccessToken
	if body.RefreshToken != "" { // some providers omit a new refresh token
		a.RefreshToken = body.RefreshToken
	}
	if body.ExpiresIn > 0 {
		a.ExpiresAt = r.opt.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return r.store.SetAccount(a)
}

// Run refreshes on a ticker until ctx is done.
func (r *Refresher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.RefreshDue(ctx); err != nil {
			r.opt.Log.Warn("pool refresh pass failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
