package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/logging"
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

// configDir is the host-side config directory for the at-jam CLI, mirroring
// at-cove: $XDG_CONFIG_HOME/at-jam, else ~/.config/at-jam.
func configDir() string { return configDirNamed("at-jam") }

// legacyConfigDir is the pre-rename (at-harbor) config directory, read only by
// migrateConfigDir. See docs/usage/jam/renamed-from-harbor.md.
func legacyConfigDir() string { return configDirNamed("at-harbor") }

func configDirNamed(name string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, name)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", name)
}

// migrateConfigDir carries a pre-rename ~/.config/at-harbor/ (settings and
// cached login tokens) across to ~/.config/at-jam/ once: only when the new
// directory is missing and the old one exists. It copies (file modes kept, so
// token files stay 0600), leaves the old directory in place, and logs a notice
// naming the two paths — never file contents. A failed copy is logged and the
// partial new directory removed, so the next run tries again.
func migrateConfigDir(stderr io.Writer) {
	newDir, oldDir := configDir(), legacyConfigDir()
	if _, err := os.Stat(newDir); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if fi, err := os.Stat(oldDir); err != nil || !fi.IsDir() {
		return
	}
	lg, _ := logging.New(logging.Options{Mode: logging.ModeFrom(os.Getenv("JAM_LOG_MODE")), Stderr: stderr, Level: slog.LevelInfo})
	if err := copyDir(oldDir, newDir); err != nil {
		_ = os.RemoveAll(newDir)
		lg.Warn("could not copy the at-harbor config directory to at-jam; using defaults",
			slog.String("from", oldDir), slog.String("to", newDir), slog.String("err", err.Error()), slog.String("see", logging.RenameDoc))
		return
	}
	lg.Info("copied the at-harbor config directory to at-jam (the old one is left in place)",
		slog.String("from", oldDir), slog.String("to", newDir), slog.String("see", logging.RenameDoc))
}

// copyDir copies the regular files and directories under src to dst,
// preserving permission bits. Symlinks and other special files are skipped.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		default:
			return nil
		}
	})
}

// adminTokenEnv is the operator token from the environment
// (JAM_ADMIN_TOKEN). It is a flag default only; the value is never logged.
// The writer is unused since the pre-rename fallback (and its warning) went.
func adminTokenEnv(_ io.Writer) string {
	return os.Getenv("JAM_ADMIN_TOKEN")
}

// clientSettings is one app profile's endpoint defaults. Command flags override.
type clientSettings struct {
	AdminURL string `yaml:"admin-url"`
	BaseURL  string `yaml:"base-url"`
}

// allSettings is the whole ~/.config/at-jam/settings.yml: a map of app name →
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

// cachedToken is one app's login-owned token, at ~/.config/at-jam/{app}-admin-token.json
// (mode 0600). AdminURL records the Jam it was minted against, for display.
// A participant login lives in its own file (participantTokenPath) and holds
// only IDToken, with Expiry the ID token's exp.
type cachedToken struct {
	AccessToken string    `json:"access_token,omitempty"`
	IDToken     string    `json:"id_token,omitempty"`
	Sub         string    `json:"sub"`
	Expiry      time.Time `json:"expiry"`
	AdminURL    string    `json:"admin_url"`
}

func tokenPath(app string) string { return filepath.Join(configDir(), app+"-admin-token.json") }

// participantTokenPath is where `at-jam login --participant` caches app's
// participant ID token: ~/.config/at-jam/{app}-participant-token.json. The
// suffix differs from tokenPath's, so no app name maps one onto the other.
func participantTokenPath(app string) string {
	return filepath.Join(configDir(), app+"-participant-token.json")
}

func saveToken(app string, t cachedToken) error { return saveTokenAt(tokenPath(app), t) }

func saveTokenAt(path string, t cachedToken) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// loadToken returns the app's cached token if present and unexpired; ok=false otherwise.
func loadToken(app string) (cachedToken, bool) { return loadTokenAt(tokenPath(app)) }

func loadTokenAt(path string) (cachedToken, bool) {
	data, err := os.ReadFile(path)
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

func clearToken(app string) error { return clearTokenAt(tokenPath(app)) }

func clearTokenAt(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// resolveToken applies the operator-token precedence for an app: an explicit
// flag/env value wins, else the app's cached login token (present and unexpired).
// Per-app token files are the scoping boundary — a token is never used under a
// different app than it was minted for.
//
// If the value came from JAM_ADMIN_TOKEN rather than an explicit --token while a logged-in
// session also exists for this app, it warns to stderr — a stale env var
// silently shadowing `at-jam login` is otherwise a baffling footgun.
func resolveToken(app, flagVal string, warn io.Writer) string {
	if flagVal != "" {
		const name = "JAM_ADMIN_TOKEN"
		if flagVal == os.Getenv(name) {
			if _, ok := loadToken(app); ok {
				fmt.Fprintf(warn, "at-jam: note: %s is set and overrides your `at-jam login` session for --app %s; run `unset %s` to use the cached token\n", name, app, name)
			}
		}
		return flagVal
	}
	if ct, ok := loadToken(app); ok {
		return ct.AccessToken
	}
	return ""
}

// resolveParticipantToken returns app's cached participant ID token — the
// bearer for the /me/studios routes — or an error naming the login to run when
// there is none or it has expired.
func resolveParticipantToken(app string) (string, error) {
	path := participantTokenPath(app)
	if t, ok := loadTokenAt(path); ok && t.IDToken != "" {
		return t.IDToken, nil
	}
	login := "at-jam login --participant"
	if app != defaultApp {
		login += " --app " + app
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("participant login expired; run: %s", login)
	}
	return "", fmt.Errorf("not signed in as a participant; run: %s", login)
}

// parseJWTClaims extracts sub + exp from a JWT payload WITHOUT verifying the
// signature — for local display/expiry only. Jam verifies tokens server-side.
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
