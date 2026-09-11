package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultApp is the profile used when --app is not given.
const defaultApp = "default"

// appNameRe constrains an --app value to a safe single filename component, so it
// can be interpolated into the per-app token filename without escaping configDir.
var appNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateApp(app string) error {
	if !appNameRe.MatchString(app) {
		return fmt.Errorf("invalid --app %q: use letters, digits, '.', '_' or '-'", app)
	}
	return nil
}

// configDir is the host-side config directory for the at-harbor CLI, mirroring
// at-cove: $XDG_CONFIG_HOME/at-harbor, else ~/.config/at-harbor.
func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-harbor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-harbor")
}

// clientSettings is one app profile's endpoint defaults. Command flags override.
type clientSettings struct {
	AdminURL string `yaml:"admin-url"`
	BaseURL  string `yaml:"base-url"`
}

// allSettings is the whole ~/.config/at-harbor/settings.yml: a map of app name →
// endpoint defaults, e.g. {default: {...}, dev-app: {...}}. Non-secret.
type allSettings map[string]clientSettings

func settingsPath() string { return filepath.Join(configDir(), "settings.yml") }

// loadAllSettings reads settings.yml; an absent or unparseable file yields an
// empty map (defaults apply), never an error — settings are optional convenience.
func loadAllSettings() allSettings {
	m := allSettings{}
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return m
	}
	_ = yaml.Unmarshal(data, &m)
	return m
}

// loadSettings returns one app's endpoint defaults (zero value if the app or the
// file is absent).
func loadSettings(app string) clientSettings { return loadAllSettings()[app] }

// saveSettings upserts one app's settings, preserving every other app's block.
func saveSettings(app string, s clientSettings) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	m := loadAllSettings()
	m[app] = s
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), data, 0o644)
}

// cachedToken is one app's login-owned token, at ~/.config/at-harbor/{app}-admin-token.json
// (mode 0600). AdminURL records the harbor it was minted against, for display.
type cachedToken struct {
	AccessToken string    `json:"access_token"`
	Sub         string    `json:"sub"`
	Expiry      time.Time `json:"expiry"`
	AdminURL    string    `json:"admin_url"`
}

func tokenPath(app string) string { return filepath.Join(configDir(), app+"-admin-token.json") }

func saveToken(app string, t cachedToken) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(tokenPath(app), data, 0o600)
}

// loadToken returns the app's cached token if present and unexpired; ok=false otherwise.
func loadToken(app string) (cachedToken, bool) {
	data, err := os.ReadFile(tokenPath(app))
	if err != nil {
		return cachedToken{}, false
	}
	var t cachedToken
	if err := json.Unmarshal(data, &t); err != nil {
		return cachedToken{}, false
	}
	if !t.Expiry.IsZero() && time.Now().After(t.Expiry) {
		return cachedToken{}, false
	}
	return t, true
}

func clearToken(app string) error {
	if err := os.Remove(tokenPath(app)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// resolveToken applies the operator-token precedence for an app: an explicit
// flag/env value wins, else the app's cached login token (present and unexpired).
// Per-app token files are the scoping boundary — a token is never used under a
// different app than it was minted for.
//
// If the value came from AT_HARBOR_ADMIN_TOKEN (not an explicit --token) while a
// logged-in session also exists for this app, it warns to stderr — a stale env
// var silently shadowing `at-harbor login` is otherwise a baffling footgun.
func resolveToken(app, flagVal string, warn io.Writer) string {
	if flagVal != "" {
		if flagVal == os.Getenv("AT_HARBOR_ADMIN_TOKEN") {
			if _, ok := loadToken(app); ok {
				fmt.Fprintf(warn, "at-harbor: note: AT_HARBOR_ADMIN_TOKEN is set and overrides your `at-harbor login` session for --app %s; run `unset AT_HARBOR_ADMIN_TOKEN` to use the cached token\n", app)
			}
		}
		return flagVal
	}
	if ct, ok := loadToken(app); ok {
		return ct.AccessToken
	}
	return ""
}

// parseJWTClaims extracts sub + exp from a JWT payload WITHOUT verifying the
// signature — for local display/expiry only. harbor verifies tokens server-side.
func parseJWTClaims(token string) (sub string, exp time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, err
	}
	var c struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", time.Time{}, err
	}
	if c.Exp > 0 {
		exp = time.Unix(c.Exp, 0)
	}
	return c.Sub, exp, nil
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
