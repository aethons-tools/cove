package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// specClaude is testClaude plus a settings file in dir and a fake
// `claude --version` printing version (counting calls).
func specClaude(t *testing.T, dir, version string, calls *int) Claude {
	t.Helper()
	c := testClaude(t, dir)
	c.SettingsPath = filepath.Join(dir, "settings.json")
	c.CLIVersion = func() (string, error) {
		if calls != nil {
			*calls++
		}
		return version + " (Claude Code)\n", nil
	}
	return c
}

func specWith(f func(*modelspec.Spec)) *modelspec.Spec {
	s := modelspec.Default("anthropic")
	f(&s)
	return &s
}

// argAfter returns the value following flag in args ("" when absent).
func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestClaudeCommandAppliesModelAndEffort(t *testing.T) {
	spec := specWith(func(s *modelspec.Spec) { s.Model = modelspec.Choice{ID: "claude-opus-5-5", Effort: "high"} })
	_, args, _ := Claude{}.Command(Episode{Spec: spec, ContextCore: "/c/CORE.md"})
	if argAfter(args, "--model") != "claude-opus-5-5" || argAfter(args, "--effort") != "high" {
		t.Fatalf("argv = %q", args)
	}
	// claude-default's bypassPermissions keeps today's flag; the MCP config is
	// not the spec's (yet).
	if !slices.Contains(args, "--dangerously-skip-permissions") || argAfter(args, "--mcp-config") != claudeMCPConfigPath {
		t.Fatalf("argv = %q", args)
	}
	if slices.Contains(args, "--settings") {
		t.Fatalf("no settings → no --settings: %q", args)
	}
	// Model flags sit before the context flags.
	if slices.Index(args, "--model") > slices.Index(args, "--append-system-prompt-file") {
		t.Fatalf("argv order = %q", args)
	}
}

func TestClaudeCommandSettingsFlagOnlyWhenSet(t *testing.T) {
	spec := specWith(func(s *modelspec.Spec) { s.Claude.Settings = map[string]any{"theme": "dark"} })
	_, args, _ := Claude{SettingsPath: "/x/s.json"}.Command(Episode{Spec: spec})
	if argAfter(args, "--settings") != "/x/s.json" {
		t.Fatalf("argv = %q", args)
	}
}

func TestClaudeEnvByProvider(t *testing.T) {
	cases := map[string]struct {
		provider string
		env      map[string]string
		want     map[string]string
	}{
		"anthropic": {"anthropic", nil, nil},
		"vertex": {"vertex", map[string]string{"ANTHROPIC_VERTEX_PROJECT_ID": "p", "CLOUD_ML_REGION": "us-east5"},
			map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_VERTEX_PROJECT_ID": "p", "CLOUD_ML_REGION": "us-east5"}},
		"bedrock": {"bedrock", map[string]string{"AWS_REGION": "us-west-2"},
			map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-west-2"}},
		// Defense in depth: what Jam refuses at write never reaches the env.
		"refused keys dropped": {"vertex", map[string]string{"PATH": "/evil", "ANTHROPIC_API_KEY": "k", "AT_JAM_X": "1", "OK": "1"},
			map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "OK": "1"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			spec := specWith(func(s *modelspec.Spec) { s.Claude.Provider, s.Claude.ProviderEnv = c.provider, c.env })
			_, _, env := Claude{}.Command(Episode{Spec: spec})
			if len(env) != len(c.want) {
				t.Fatalf("env = %v, want %v", env, c.want)
			}
			for k, v := range c.want {
				if env[k] != v {
					t.Fatalf("env = %v, want %v", env, c.want)
				}
			}
		})
	}
}

func TestClaudeValidateVersion(t *testing.T) {
	for _, c := range []struct {
		constraint, have string
		ok               bool
	}{
		{">=2.0.0", "2.1.287", true},
		{"2.x", "2.1.287", true},
		{"2.1.x", "2.1.287", true},
		{"2.1.287", "2.1.287", true},
		{"3.x", "2.1.287", false},
		{"2.1.288", "2.1.287", false},
		{">=2.2.0", "2.1.287", false},
	} {
		dir := t.TempDir()
		spec := specWith(func(s *modelspec.Spec) { s.Version = c.constraint })
		err := specClaude(t, dir, c.have, nil).Validate(spec)
		if (err == nil) != c.ok {
			t.Errorf("%q vs %s: err = %v, want ok=%v", c.constraint, c.have, err, c.ok)
		}
		if err != nil {
			for _, want := range []string{"claude-default", c.constraint, c.have} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		}
	}
}

func TestClaudeValidateVersionFailsLoud(t *testing.T) {
	spec := specWith(func(s *modelspec.Spec) { s.Version = "2.x" })
	dir := t.TempDir()
	c := testClaude(t, dir)
	c.CLIVersion = func() (string, error) { return "", errors.New(`exec: "claude": executable file not found in $PATH`) }
	if err := c.Validate(spec); err == nil || !strings.Contains(err.Error(), "claude --version") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing binary: err = %v", err)
	}
	c.CLIVersion = func() (string, error) { return "garbage\n", nil }
	if err := c.Validate(spec); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Fatalf("unparseable: err = %v", err)
	}
	// No MCP config is written when the version check fails first.
	if _, err := os.Stat(c.MCPConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MCP config written despite a failed pre-flight: %v", err)
	}
	if err := c.Validate(specWith(func(s *modelspec.Spec) { s.Type = "codex" })); err == nil {
		t.Fatal("a non-claude spec must be refused")
	}
}

func TestClaudeValidateSkipsVersionWithoutConstraint(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	c := specClaude(t, dir, "2.1.287", &calls)
	if err := c.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(specWith(func(s *modelspec.Spec) { s.Version = "*" })); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("claude --version ran %d times with no constraint to check", calls)
	}
}

func TestClaudeValidateWritesAndClearsSettings(t *testing.T) {
	dir := t.TempDir()
	c := specClaude(t, dir, "2.1.287", nil)
	if err := c.Validate(specWith(func(s *modelspec.Spec) { s.Claude.Settings = map[string]any{"theme": "dark"} })); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(b, &got) != nil || got["theme"] != "dark" {
		t.Fatalf("settings file = %s", b)
	}
	if err := c.Validate(specWith(func(*modelspec.Spec) {})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.SettingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale settings file kept: %v", err)
	}
}

// specConnector is a connector delivering spec (and one env key the
// connector owns, which the spec's provider-env may not override).
func specConnector(spec *modelspec.Spec) snippet.Connector {
	return snippet.Connector{Env: map[string]string{"ANTHROPIC_BASE_URL": "{base}/anthropic/"}, ModelSpec: spec}
}

func specWL(t *testing.T, dir string, c Claude, sp Spawner, initial snippet.Connector, src ConnectorSource, resident bool) *Workload {
	t.Helper()
	return New(Config{WorkDir: dir, Prompt: "p", MaxWait: time.Minute, Resident: resident, Harness: c, Spawner: sp,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t", Initial: initial,
			Environ: func() []string { return []string{"PATH=/bin", "CLOUD_ML_REGION=stale"} }}}, nil)
}

// A cove under a role bound to a spec with model.id launches claude with that
// model, and the spec's provider env reaches the agent — never over a key the
// connector owns.
func TestRunLaunchesWithSpecModel(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	spec := specWith(func(s *modelspec.Spec) {
		s.Name, s.Model.ID = "opus", "claude-opus-5-5"
		s.Claude.Provider = "vertex"
		s.Claude.ProviderEnv = map[string]string{"CLOUD_ML_REGION": "us-east5", "ANTHROPIC_BASE_URL": "https://elsewhere"}
	})
	conn := specConnector(spec)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := specWL(t, dir, specClaude(t, dir, "2.1.287", nil), f, conn, &fakeSource{c: conn}, false)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil {
		t.Fatal(err)
	}
	if f.bin != "claude" || argAfter(f.args, "--model") != "claude-opus-5-5" {
		t.Fatalf("spawned %s %q", f.bin, f.args)
	}
	m := envMap(f.env)
	if m["CLAUDE_CODE_USE_VERTEX"] != "1" || m["CLOUD_ML_REGION"] != "us-east5" || m["PATH"] != "/bin" {
		t.Fatalf("env = %v", m)
	}
	if m["ANTHROPIC_BASE_URL"] != "https://jam.example/anthropic/" {
		t.Fatalf("provider-env overrode a connector-owned key: %v", m)
	}
	if n := strings.Count(strings.Join(f.env, "\n"), "CLOUD_ML_REGION="); n != 1 {
		t.Fatalf("CLOUD_ML_REGION set %d times: %v", n, f.env)
	}
}

// An edited spec is picked up at the cove's next episode: the refreshed
// connector carries it, the harness re-validates it, and the next spawn uses it.
func TestRunPicksUpEditedSpecNextEpisode(t *testing.T) {
	dir := t.TempDir()
	first := specConnector(specWith(func(s *modelspec.Spec) { s.Model.ID = "claude-sonnet-5" }))
	edited := specConnector(specWith(func(s *modelspec.Spec) { s.Model.ID = "claude-opus-5-5"; s.Version = "2.1.x" }))
	src := &seqSource{connectors: []snippet.Connector{first, edited}}
	calls := 0
	f := &scriptedSpawner{dir: dir}
	w := specWL(t, dir, specClaude(t, dir, "2.1.287", &calls), f, first, src, true)
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 2 })
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(f.calls))
	}
	if got := argAfter(f.calls[0].args, "--model"); got != "claude-sonnet-5" {
		t.Fatalf("episode 1 model = %q", got)
	}
	if got := argAfter(f.calls[1].args, "--model"); got != "claude-opus-5-5" {
		t.Fatalf("episode 2 model = %q (the edit must apply at the next episode)", got)
	}
	// Validated at start (raise-time spec) and once more for the edit; the
	// unchanged first fetch is not re-checked.
	if calls != 2 {
		t.Fatalf("claude --version ran %d times, want 2", calls)
	}
}

// A version mismatch fails the run with a clear error before any spawn.
func TestRunFailsOnVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	conn := specConnector(specWith(func(s *modelspec.Spec) { s.Version = "3.x" }))
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := specWL(t, dir, specClaude(t, dir, "2.1.287", nil), f, conn, &fakeSource{c: conn}, false)
	h := &recordHandle{}
	err := w.Run(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "requires claude 3.x") || !strings.Contains(err.Error(), "2.1.287") {
		t.Fatalf("Run err = %v", err)
	}
	if f.bin != "" || h.count(covemaster.Running) != 0 {
		t.Fatal("the agent was spawned despite the version mismatch")
	}
}

// An edit to a spec this image can't satisfy fails the run at the next
// episode rather than launching the wrong CLI.
func TestRunFailsWhenEditedSpecMismatches(t *testing.T) {
	dir := t.TempDir()
	ok := specConnector(specWith(func(*modelspec.Spec) {}))
	bad := specConnector(specWith(func(s *modelspec.Spec) { s.Version = "9.x" }))
	src := &seqSource{connectors: []snippet.Connector{ok, bad}}
	f := &scriptedSpawner{dir: dir}
	w := specWL(t, dir, specClaude(t, dir, "2.1.287", nil), f, ok, src, true)
	h := &recordHandle{}
	done := runAsync(context.Background(), w, h)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "requires claude 9.x") {
			t.Fatalf("Run err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not fail on the mismatched edit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("spawned %d episodes, want 1", len(f.calls))
	}
}
