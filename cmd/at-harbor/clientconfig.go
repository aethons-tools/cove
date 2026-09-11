package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// configDir is the host-side config directory for the at-harbor CLI, mirroring
// at-cove: $XDG_CONFIG_HOME/at-harbor, else ~/.config/at-harbor.
func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-harbor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-harbor")
}

// clientSettings is the operator-authored ~/.config/at-harbor/settings.yml —
// non-secret client endpoint defaults. Command flags override these.
type clientSettings struct {
	AdminURL string `yaml:"admin-url"`
	BaseURL  string `yaml:"base-url"`
}

// loadSettings reads settings.yml; an absent or unparseable file yields zero
// values (defaults apply), never an error — settings are optional convenience.
func loadSettings() clientSettings {
	var s clientSettings
	data, err := os.ReadFile(filepath.Join(configDir(), "settings.yml"))
	if err != nil {
		return s
	}
	_ = yaml.Unmarshal(data, &s)
	return s
}

// cachedToken is the login-owned ~/.config/at-harbor/token.json (mode 0600).
// AdminURL records the harbor the token was minted against, so it is never
// replayed to a different admin-url than the operator logged in to.
type cachedToken struct {
	AccessToken string    `json:"access_token"`
	Sub         string    `json:"sub"`
	Expiry      time.Time `json:"expiry"`
	AdminURL    string    `json:"admin_url"`
}

func tokenPath() string { return filepath.Join(configDir(), "token.json") }

func saveToken(t cachedToken) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(tokenPath(), data, 0o600)
}

// loadToken returns the cached token if present and unexpired; ok=false otherwise.
func loadToken() (cachedToken, bool) {
	data, err := os.ReadFile(tokenPath())
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

func clearToken() error {
	if err := os.Remove(tokenPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
