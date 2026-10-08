package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	spec := specWith(func(s *modelspec.Spec) {
		s.Model = modelspec.Choice{ID: "claude-opus-5-5", Effort: "high"}
		s.Claude.Plugins, s.Claude.Settings = nil, nil
	})
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
		t.Fatalf("no settings, no plugins → no --settings: %q", args)
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
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, &calls)
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
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
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
	if err := c.Validate(specWith(func(s *modelspec.Spec) { s.Claude.Plugins, s.Claude.Settings = nil, nil })); err != nil {
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
	spec := specWith(func(s *modelspec.Spec) {
		s.Name, s.Model.ID = "opus", "claude-opus-5-5"
		s.Claude.Provider = "vertex"
		s.Claude.ProviderEnv = map[string]string{"CLOUD_ML_REGION": "us-east5", "ANTHROPIC_BASE_URL": "https://elsewhere"}
	})
	conn := specConnector(spec)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w := specWL(t, dir, specClaude(t, dir, modelspec.DefaultClaudeVersion, nil), f, conn, &fakeSource{c: conn}, false)
	if err := runEpisodes(w, &recordHandle{}, 1); err != nil {
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
	w := specWL(t, dir, specClaude(t, dir, modelspec.DefaultClaudeVersion, &calls), f, first, src, true)
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
	w := specWL(t, dir, specClaude(t, dir, modelspec.DefaultClaudeVersion, nil), f, conn, &fakeSource{c: conn}, false)
	h := &recordHandle{}
	err := runEpisodes(w, h, 1)
	if err == nil || !strings.Contains(err.Error(), "requires claude 3.x") || !strings.Contains(err.Error(), modelspec.DefaultClaudeVersion) {
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
	w := specWL(t, dir, specClaude(t, dir, modelspec.DefaultClaudeVersion, nil), f, ok, src, true)
	h := &recordHandle{}
	done := runAsyncEpisodes(w, h, 2)
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

// The pre-flight refuses a policy.mode the harness can't run — an unknown one,
// or plan (a headless cove can never leave it) — before writing any config.
func TestClaudeValidateRejectsBadPolicyMode(t *testing.T) {
	for mode, want := range map[string]string{"yolo": `"yolo"`, "plan": "never leave plan mode"} {
		dir := t.TempDir()
		c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
		err := c.Validate(specWith(func(s *modelspec.Spec) { s.Name, s.Policy.Mode = "odd", mode }))
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `"odd"`) {
			t.Errorf("mode %q: err = %v, want one naming the spec and mentioning %q", mode, err, want)
		}
		if _, err := os.Stat(c.MCPConfigPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("mode %q: MCP config written despite a failed pre-flight: %v", mode, err)
		}
	}
	for _, mode := range append(modelspec.PermissionModes(), "") {
		dir := t.TempDir()
		if err := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil).Validate(specWith(func(s *modelspec.Spec) { s.Policy.Mode = mode })); err != nil {
			t.Errorf("mode %q refused: %v", mode, err)
		}
	}
}

// The policy flags sit right after the stream-json flags and before the MCP,
// model, settings and context flags, with --continue still second.
func TestClaudeCommandPolicyFlagPosition(t *testing.T) {
	spec := specWith(func(s *modelspec.Spec) {
		s.Policy = modelspec.Policy{Mode: "dontAsk", Allow: []string{"Read"}, Deny: []string{"WebFetch"}}
		s.Model.ID = "claude-opus-5-5"
		s.Claude.Settings = map[string]any{"theme": "dark"}
	})
	_, args, _ := Claude{SettingsPath: "/x/s.json"}.Command(Episode{Continued: true, ContextCore: "/c/CORE.md", Spec: spec})
	want := []string{"-p", "--continue", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-mode=dontAsk",
		"--allowedTools=mcp__messaging",
		"--allowedTools=Read", "--disallowedTools=WebFetch",
		"--mcp-config", claudeMCPConfigPath, "--strict-mcp-config",
		"--model", "claude-opus-5-5",
		"--settings", "/x/s.json",
		"--append-system-prompt-file", "/c/CORE.md", "--system-prompt-snapshot", "off"}
	if !slices.Equal(args, want) {
		t.Fatalf("argv:\n got %q\nwant %q", args, want)
	}
}

// A policy edit is picked up at the next episode like any other spec edit.
func TestRunPicksUpEditedPolicyNextEpisode(t *testing.T) {
	dir := t.TempDir()
	first := specConnector(specWith(func(*modelspec.Spec) {}))
	edited := specConnector(specWith(func(s *modelspec.Spec) {
		s.Policy = modelspec.Policy{Mode: "acceptEdits", Deny: []string{"WebFetch"}}
	}))
	src := &seqSource{connectors: []snippet.Connector{first, edited}}
	f := &scriptedSpawner{dir: dir}
	w := specWL(t, dir, specClaude(t, dir, modelspec.DefaultClaudeVersion, nil), f, first, src, true)
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
	if a := f.calls[0].args; !slices.Contains(a, "--dangerously-skip-permissions") || slices.ContainsFunc(a, func(s string) bool { return strings.HasPrefix(s, "--permission-mode") }) {
		t.Fatalf("episode 1 argv = %q, want bypass", a)
	}
	a := f.calls[1].args
	if slices.Contains(a, "--dangerously-skip-permissions") || !slices.Contains(a, "--permission-mode=acceptEdits") || !slices.Contains(a, "--disallowedTools=WebFetch") {
		t.Fatalf("episode 2 argv = %q (the policy edit must apply at the next episode)", a)
	}
}

// The version split (COV-242): version is the exact pin the image installed,
// and the runtime check is version-constraint — defaulting to == version.
func TestClaudeValidateRuntimeConstraint(t *testing.T) {
	for _, c := range []struct {
		version, constraint, have string
		ok                        bool
	}{
		{"2.1.287", "", "2.1.287", true},
		{"2.1.287", "", "2.1.288", false}, // default == version
		{"2.1.287", "2.x", "2.5.0", true}, // the explicit constraint wins
		{"2.1.287", "2.1.x", "2.2.0", false},
	} {
		spec := specWith(func(s *modelspec.Spec) { s.Version, s.VersionConstraint = c.version, c.constraint })
		err := specClaude(t, t.TempDir(), c.have, nil).Validate(spec)
		if (err == nil) != c.ok {
			t.Errorf("version %s constraint %q vs %s: err = %v, want ok=%v", c.version, c.constraint, c.have, err, c.ok)
		}
	}
}

// Plugin enablement follows the spec, not the image (COV-242): each
// claude.plugins id is enabled, and its marketplace declared, in the per-run
// settings — merged over claude.settings, so the file is written when either
// is set. No plugins and no settings → no file, no --settings.
func TestClaudeSettingsEnableSpecPlugins(t *testing.T) {
	dir := t.TempDir()
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
	spec := specWith(func(s *modelspec.Spec) {
		s.Claude.Settings = map[string]any{"theme": "dark"}
		s.Claude.Plugins = []string{"superpowers@claude-plugins-official"}
	})
	if err := c.Validate(spec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Theme                  string          `json:"theme"`
		EnabledPlugins         map[string]bool `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]struct {
			Source struct{ Source, Repo string } `json:"source"`
		} `json:"extraKnownMarketplaces"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	m := got.ExtraKnownMarketplaces["claude-plugins-official"]
	if got.Theme != "dark" || !got.EnabledPlugins["superpowers@claude-plugins-official"] || len(got.EnabledPlugins) != 1 ||
		m.Source.Source != "github" || m.Source.Repo != "anthropics/claude-plugins-official" {
		t.Fatalf("settings file = %s", b)
	}
	if spec.Claude.Settings["enabledPlugins"] != nil {
		t.Fatal("the spec's settings map was mutated")
	}
	// Plugins alone still write the file and pass --settings.
	only := specWith(func(s *modelspec.Spec) {
		s.Claude.Settings, s.Claude.Plugins = nil, []string{"superpowers@claude-plugins-official"}
	})
	if err := c.Validate(only); err != nil {
		t.Fatal(err)
	}
	if _, args, _ := c.Command(Episode{Spec: only}); argAfter(args, "--settings") != c.SettingsPath {
		t.Fatalf("plugins only → --settings expected: %q", args)
	}
	// Neither → no file, no flag: nothing enabled, nothing to auto-install.
	none := specWith(func(s *modelspec.Spec) { s.Claude.Plugins, s.Claude.Settings = nil, nil })
	if err := c.Validate(none); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.SettingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings file kept with no settings and no plugins: %v", err)
	}
	if _, args, _ := c.Command(Episode{Spec: none}); slices.Contains(args, "--settings") {
		t.Fatalf("argv = %q", args)
	}
}

// COV-245: claude-default's per-run settings file carries the preferences the
// sealed managed settings used to force (same values) plus its plugins'
// enablement — and nothing that is sandbox policy (the harness layer's managed
// settings keep that).
func TestClaudeDefaultSettingsFile(t *testing.T) {
	dir := t.TempDir()
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
	def := modelspec.Default("anthropic")
	if err := c.Validate(&def); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"agentPushNotifEnabled": true, "alwaysThinkingEnabled": true, "disableAgentView": true,
		"inputNeededNotifEnabled": true, "prefersReducedMotion": true, "showThinkingSummaries": true,
		"showTurnDuration": true, "spinnerTipsEnabled": false, "theme": "dark",
		"enabledPlugins": map[string]any{"superpowers@claude-plugins-official": true},
		"extraKnownMarketplaces": map[string]any{"claude-plugins-official": map[string]any{
			"source": map[string]any{"source": "github", "repo": "anthropics/claude-plugins-official"}}},
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("claude-default settings file:\n got %s\nwant %s", gj, wj)
	}
}

// clobberSpawner records whether the harness's generated files exist at each
// spawn, then deletes them — as an agent running arbitrary code in the cove
// might (e.g. this package's own tests, once, against the real /dev/shm).
type clobberSpawner struct {
	scriptedSpawner
	paths   []string
	present [][]bool
}

func (f *clobberSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	var seen []bool
	for _, p := range f.paths {
		_, err := os.Stat(p)
		seen = append(seen, err == nil)
		_ = os.Remove(p)
	}
	f.mu.Lock()
	f.present = append(f.present, seen)
	f.mu.Unlock()
	return f.scriptedSpawner.Spawn(ctx, bin, args, dir, env, stdout)
}

// The files a spawn's argv names (--settings, --mcp-config) are regenerated
// before every episode, not only when the spec changes: one deleted mid-session
// would otherwise fail every later `claude --continue` at startup.
func TestRunRegeneratesHarnessFilesEachEpisode(t *testing.T) {
	dir := t.TempDir()
	conn := specConnector(specWith(func(s *modelspec.Spec) { s.Claude.Settings = map[string]any{"theme": "dark"} }))
	c := specClaude(t, dir, modelspec.DefaultClaudeVersion, nil)
	f := &clobberSpawner{scriptedSpawner: scriptedSpawner{dir: dir}, paths: []string{c.SettingsPath, c.MCPConfigPath}}
	w := specWL(t, dir, c, f, conn, &fakeSource{c: conn}, true)
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
	if len(f.present) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(f.present))
	}
	for i, seen := range f.present {
		for j, ok := range seen {
			if !ok {
				t.Errorf("episode %d spawned without %s", i+1, f.paths[j])
			}
		}
	}
}
