package agentrun

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("settings file = %s: %v", b, err)
	}
	return got
}

// The role's cache.ttl is merged into the per-run settings as
// promptCacheTtl, over any value the model-spec's claude.settings sets, and
// never into the spec itself.
func TestClaudeSettingsRoleCacheTTLOverSpec(t *testing.T) {
	dir := t.TempDir()
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
	spec := specWith(func(s *modelspec.Spec) {
		s.Claude.Settings = map[string]any{"theme": "dark", "promptCacheTtl": "5m"}
	})
	if err := c.Validate(spec, "1h"); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, c.SettingsPath)
	if got["promptCacheTtl"] != "1h" || got["theme"] != "dark" {
		t.Fatalf("settings = %v", got)
	}
	if spec.Claude.Settings["promptCacheTtl"] != "5m" {
		t.Fatal("the spec's settings map was mutated")
	}
	// Prepare (every spawn) applies it the same way.
	if err := c.Prepare(spec, "5m"); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, c.SettingsPath); got["promptCacheTtl"] != "5m" {
		t.Fatalf("settings = %v", got)
	}
}

// Unset ttl changes nothing: the settings file is byte-for-byte the spec's
// own, and with no spec settings there is no file and no --settings.
func TestClaudeSettingsUnsetCacheTTLUnchanged(t *testing.T) {
	dir := t.TempDir()
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
	spec := specWith(func(s *modelspec.Spec) {
		s.Claude.Plugins = nil
		s.Claude.Settings = map[string]any{"theme": "dark", "promptCacheTtl": "5m"}
	})
	if err := c.Prepare(spec, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(spec.Claude.Settings)
	if string(b) != string(want)+"\n" {
		t.Fatalf("settings file = %s, want %s", b, want)
	}
	if err := c.Prepare(nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.SettingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings file kept with nothing to set: %v", err)
	}
	if _, args, _ := c.Command(Episode{}); slices.Contains(args, "--settings") {
		t.Fatalf("argv = %q", args)
	}
}

// With no model-spec delivered, the role's ttl alone still writes the file
// and passes --settings.
func TestClaudeCacheTTLWithoutSpec(t *testing.T) {
	dir := t.TempDir()
	c := testClaude(t, dir)
	if err := c.Validate(nil, "1h"); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, c.SettingsPath); len(got) != 1 || got["promptCacheTtl"] != "1h" {
		t.Fatalf("settings = %v", got)
	}
	if _, args, _ := c.Command(Episode{CacheTTL: "1h"}); argAfter(args, "--settings") != c.SettingsPath {
		t.Fatalf("argv = %q", args)
	}
}

func cacheBundle(core, ttl string) sessionctx.Bundle {
	return sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n", Project: "p", Role: "r", CacheTTL: ttl}, Role: sessionctx.Layer{Core: core}})
}

// The session runs with the ttl its raise-time context carried: a refreshed
// context with another value doesn't change it (a change applies to sessions
// raised after it), and a value other than 5m or 1h is ignored.
func TestRunAppliesRaiseTimeCacheTTL(t *testing.T) {
	cases := map[string]struct{ raised, refreshed, want string }{
		"1h":             {"1h", "5m", "1h"},
		"5m":             {"5m", "1h", "5m"},
		"unset":          {"", "1h", ""},
		"invalid unsets": {"2h", "", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			h := testClaude(t, dir)
			f := &scriptedSpawner{dir: dir}
			one, two := cacheBundle("ONE", c.raised), cacheBundle("TWO", c.refreshed)
			w := New(Config{WorkDir: dir, Prompt: "p", Harness: h, Spawner: f,
				Context: &one, ContextDir: filepath.Join(dir, "context"), ContextSource: &seqContext{bundles: []sessionctx.Bundle{two}}}, nil)
			if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
				t.Fatal(err)
			}
			args := f.calls[0].args
			if c.want == "" {
				if slices.Contains(args, "--settings") {
					t.Fatalf("argv = %q", args)
				}
				if _, err := os.Stat(h.SettingsPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("settings file written: %v", err)
				}
				return
			}
			if argAfter(args, "--settings") != h.SettingsPath {
				t.Fatalf("argv = %q", args)
			}
			if got := readSettings(t, h.SettingsPath); got["promptCacheTtl"] != c.want {
				t.Fatalf("settings = %v", got)
			}
		})
	}
}
