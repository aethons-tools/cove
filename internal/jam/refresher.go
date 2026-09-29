package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
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

// RefreshDue refreshes every account whose ExpiresAt is within Margin of Now.
// Per-account failures are logged and do not stop the others. Never logs tokens.
func (r *Refresher) RefreshDue(ctx context.Context) error {
	accts, err := r.store.Accounts()
	if err != nil {
		return err
	}
	deadline := r.opt.Now().Add(r.opt.Margin)
	for _, a := range accts {
		if a.ExpiresAt.After(deadline) {
			continue
		}
		if err := r.refreshOne(ctx, a); err != nil {
			r.opt.Log.Warn("pool token refresh failed", "account", a.Name, "error", err.Error())
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
		return fmt.Errorf("token endpoint status %d", resp.StatusCode) // body not logged (may echo secrets)
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
