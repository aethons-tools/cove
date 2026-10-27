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

	"github.com/aethons-tools/cove/internal/legacynames"
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

// configDir is jam's host-side config directory: $XDG_CONFIG_HOME/jam, else
// ~/.config/jam. It also holds the launcher's default SSH identity and
// known_hosts.d (validateLauncher).
func configDir() string { return filepath.Join(xdgConfigHome(), "jam") }

// xdgConfigHome is $XDG_CONFIG_HOME, else ~/.config.
func xdgConfigHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// xdgStateHome is $XDG_STATE_HOME, else ~/.local/state ("" when no home
// directory resolves).
func xdgStateHome() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return x
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state")
}

// migrateTempPrefix names the temp files a directory copy writes before
// renaming each into place; a run cut short may leave one, which the next
// run removes.
const migrateTempPrefix = ".jam-migrate-"

// dirMove is one pre-rename directory copied to its new home: every entry of
// from when entries is nil, else only the listed ones. marker, a file in to
// written after everything else, says the copy is complete.
type dirMove struct {
	from, to string
	entries  []string
	marker   string
}

// renamedDirs are the jam rename's directory moves
// (docs/usage/jam/renamed-from-at.md). The CLI config and the launcher's
// identity both land in configDir.
func renamedDirs() []dirMove {
	cfg := xdgConfigHome()
	moves := []dirMove{
		{from: filepath.Join(cfg, legacynames.ConfigDir), to: configDir(), marker: ".jam-migrated-config"},
		{from: filepath.Join(cfg, legacynames.LauncherDir), to: configDir(), marker: ".jam-migrated-launcher",
			entries: []string{"id_ed25519", "id_ed25519.pub", "known_hosts.d"}},
	}
	if s := xdgStateHome(); s != "" {
		moves = append(moves, dirMove{from: filepath.Join(s, legacynames.StateDir), to: filepath.Join(s, "jam"), marker: ".jam-migrated-state"})
	}
	return moves
}

// migrateDirs carries the pre-rename directories across once, at every jam
// invocation before any command runs (so serve finds the launcher key before
// keys.EnsureFile could mint a fresh one). It copies each file missing from
// the new directory (modes kept, so keys and tokens stay 0600; a file already
// there is never overwritten), each through a temp file renamed into place,
// then writes the move's marker last. A move whose marker exists is done; one
// without (a run cut short or failed) is finished by the next run. The old
// directories are left in place. It logs the two paths — never file contents.
func migrateDirs(stderr io.Writer) {
	var lg *logging.Logger
	logger := func() *logging.Logger {
		if lg == nil {
			var err error
			if lg, err = logging.New(logging.Options{Mode: logging.ModeFrom(os.Getenv("JAM_LOG_MODE")), Stderr: stderr, Level: slog.LevelInfo}); err != nil {
				lg = &logging.Logger{Logger: slog.New(slog.DiscardHandler)} // unreachable without a FilePath
			}
		}
		return lg
	}
	for _, m := range renamedDirs() {
		if fi, err := os.Stat(m.from); err != nil || !fi.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(m.to, m.marker)); err == nil {
			continue
		}
		copied, err := m.copy()
		if err != nil {
			logger().Warn("could not copy a pre-rename directory; it is retried on the next run",
				slog.String("from", m.from), slog.String("to", m.to), slog.String("err", err.Error()), slog.String("see", logging.RenamedFromAtDoc))
			continue
		}
		if copied > 0 {
			logger().Info("copied a pre-rename directory (the old one is left in place)",
				slog.String("from", m.from), slog.String("to", m.to), slog.Int("files", copied), slog.String("see", logging.RenamedFromAtDoc))
		}
	}
}

// copy copies m's missing files and then writes its marker, returning how
// many files it copied.
func (m dirMove) copy() (int, error) {
	fi, err := os.Stat(m.from)
	if err != nil {
		return 0, err
	}
	if err := mkdirTarget(m.to, fi.Mode().Perm()|0o700); err != nil {
		return 0, err
	}
	entries := m.entries
	if entries == nil {
		des, err := os.ReadDir(m.from)
		if err != nil {
			return 0, err
		}
		for _, de := range des {
			entries = append(entries, de.Name())
		}
	}
	n := 0
	for _, e := range entries {
		c, err := copyEntry(filepath.Join(m.from, e), filepath.Join(m.to, e))
		n += c
		if err != nil {
			return n, err
		}
	}
	return n, migrateWriteFile(filepath.Join(m.to, m.marker), nil, 0o600)
}

// copyEntry copies src — a regular file, a symlink or a directory tree; a
// missing src is nothing to copy — to dst, keeping permission bits. A symlink
// (top level or inside a tree) is recreated as a symlink to the same target,
// never followed. Paths already at dst are kept; special files are skipped.
// It returns how many files and links it copied.
func copyEntry(src, dst string) (int, error) {
	fi, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return copyLeafIfMissing(src, dst, fi)
	}
	n := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info() // WalkDir's entries are lstat'd: a link is a link
		if err != nil {
			return err
		}
		if d.IsDir() {
			return mkdirTarget(target, info.Mode().Perm())
		}
		c, err := copyLeafIfMissing(p, target, info)
		n += c
		return err
	})
	return n, err
}

// copyLeafIfMissing copies src (lstat info) to dst unless dst exists: a
// regular file through copyFileIfMissing, a symlink recreated with the same
// target. Anything else is skipped.
func copyLeafIfMissing(src, dst string, info fs.FileInfo) (int, error) {
	if info.Mode()&fs.ModeSymlink == 0 {
		return copyFileIfMissing(src, dst, info)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	target, err := os.Readlink(src)
	if err != nil {
		return 0, err
	}
	if err := os.Symlink(target, dst); err != nil {
		return 0, err
	}
	return 1, nil
}

// mkdirTarget creates dir (perm) when missing and removes any temp file an
// earlier, interrupted copy left in it — one older than a minute, so a jam
// running alongside (an IDE starts several ssh-proxy at once) keeps its own.
func mkdirTarget(dir string, perm fs.FileMode) error {
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	stale, _ := filepath.Glob(filepath.Join(dir, migrateTempPrefix+"*"))
	for _, p := range stale {
		if fi, err := os.Lstat(p); err != nil || time.Since(fi.ModTime()) < time.Minute {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// copyFileIfMissing copies the regular file src (info) to dst unless dst
// exists, returning 1 when it copied.
func copyFileIfMissing(src, dst string, info fs.FileInfo) (int, error) {
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	if err := migrateWriteFile(dst, data, info.Mode().Perm()); err != nil {
		return 0, err
	}
	return 1, nil
}

// migrateWriteFile writes data to path (perm) through a temp file in the same
// directory renamed into place, so a run cut short never leaves a truncated
// file at path.
func migrateWriteFile(path string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), migrateTempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// allSettings is the whole ~/.config/jam/settings.yml: a map of app name →
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

// cachedToken is one app's login-owned token, at ~/.config/jam/{app}-admin-token.json
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

// participantTokenPath is where `jam login --participant` caches app's
// participant ID token: ~/.config/jam/{app}-participant-token.json. The
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
// silently shadowing `jam login` is otherwise a baffling footgun.
func resolveToken(app, flagVal string, warn io.Writer) string {
	if flagVal != "" {
		const name = "JAM_ADMIN_TOKEN"
		if flagVal == os.Getenv(name) {
			if _, ok := loadToken(app); ok {
				fmt.Fprintf(warn, "jam: note: %s is set and overrides your `jam login` session for --app %s; run `unset %s` to use the cached token\n", name, app, name)
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
	login := "jam login --participant"
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
