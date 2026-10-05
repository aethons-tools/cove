package kit

import (
	"maps"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

const vertexSpecKit = `
name: k
model-spec:
  name: vertex-opus
  type: claude
  version: 2.1.287
  model: {id: claude-opus-4-8, effort: high}
  claude:
    provider: vertex
    provider-env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us-east5
      ANTHROPIC_MODEL: claude-opus-4-8
    plugins: [superpowers@claude-plugins-official, other@claude-plugins-official]
`

func TestParseConfig_ModelSpecBlock(t *testing.T) {
	cfg, err := ParseConfig([]byte(vertexSpecKit))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	s := cfg.ModelSpec
	if s == nil || s.Name != "vertex-opus" || s.Version != "2.1.287" || s.Model.ID != "claude-opus-4-8" ||
		s.Claude == nil || s.Claude.Provider != "vertex" || len(s.Claude.Plugins) != 2 {
		t.Fatalf("model-spec not parsed as written: %+v", s)
	}
	if !cfg.UsesVertex() {
		t.Fatal("UsesVertex() = false for claude.provider: vertex")
	}
	if got := cfg.EffectiveModelSpec(); got.Name != "vertex-opus" {
		t.Fatalf("EffectiveModelSpec = %q, want the kit's spec", got.Name)
	}
}

// A vertex model-spec renders exactly the session env the old
// model-provider.vertex block did: CLAUDE_CODE_USE_VERTEX=1 plus every env key.
func TestModelSpecVertexRendersOldModelProviderEnv(t *testing.T) {
	cfg, err := ParseConfig([]byte(vertexSpecKit))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := map[string]string{ // what VertexEnv() returned for the same env map
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "my-proj",
		"CLOUD_ML_REGION":             "us-east5",
		"ANTHROPIC_MODEL":             "claude-opus-4-8",
	}
	if got := cfg.ProviderEnv(); !maps.Equal(got, want) {
		t.Fatalf("ProviderEnv = %v, want %v", got, want)
	}
	if got := cfg.SessionEnv(); !maps.Equal(got, want) {
		t.Fatalf("SessionEnv = %v, want %v", got, want)
	}
	// One rendering: the cove-side harness renders the same env from the spec.
	if got := modelspec.ProviderEnv(cfg.ModelSpec); !maps.Equal(got, want) {
		t.Fatalf("modelspec.ProviderEnv = %v, want %v", got, want)
	}
}

func TestEffectiveModelSpec_DefaultIsClaudeDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	got, want := cfg.EffectiveModelSpec(), modelspec.Default("")
	if got.Name != want.Name || got.Version != want.Version || !slicesEqual(got.Claude.Plugins, want.Claude.Plugins) || got.Claude.Provider != "anthropic" {
		t.Fatalf("EffectiveModelSpec = %+v, want claude-default %+v", got, want)
	}
	if cfg.UsesVertex() || cfg.ProviderEnv() != nil {
		t.Fatal("a kit without model-spec targets the anthropic provider, with no provider env")
	}
}

func slicesEqual(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

// The kit loader runs the ONE model-spec validator (modelspec.Validate) —
// every Jam write-time refusal applies — plus the at-cove principal rule.
func TestParseConfig_ModelSpecValidation(t *testing.T) {
	base := "name: k\nmodel-spec:\n  name: s\n  type: claude\n  version: 2.1.287\n"
	for label, tc := range map[string]struct{ yaml, want string }{
		"no name":        {"name: k\nmodel-spec: {type: claude, version: 2.1.287, claude: {provider: anthropic}}\n", "name is required"},
		"range version":  {"name: k\nmodel-spec: {name: s, type: claude, version: '>=2.0.0', claude: {provider: anthropic}}\n", "version-constraint"},
		"no body":        {base, "requires a claude: body"},
		"bad provider":   {base + "  claude: {provider: openai}\n", "not supported"},
		"bedrock":        {base + "  claude: {provider: bedrock}\n", "bedrock"},
		"protected env":  {base + "  claude: {provider: anthropic, provider-env: {https_proxy: x}}\n", "https_proxy"},
		"credential env": {base + "  claude: {provider: anthropic, provider-env: {ANTHROPIC_API_KEY: x}}\n", "ANTHROPIC_API_KEY"},
		"bad plugin":     {base + "  claude: {provider: anthropic, plugins: [x@nowhere]}\n", "nowhere"},
		"plan mode":      {base + "  policy: {mode: plan}\n  claude: {provider: anthropic}\n", "plan"},
		"non-pref":       {base + "  claude: {provider: anthropic, settings: {hooks: {}}}\n", "hooks"},
		"principal":      {base + "  principal: {credential: pool}\n  claude: {provider: anthropic}\n", "principal"},
		"principal hdrs": {base + "  principal: {headers: [{name: X-A, set: b}]}\n  claude: {provider: anthropic}\n", "principal"},
		"vertex region":  {base + "  claude: {provider: vertex, provider-env: {ANTHROPIC_VERTEX_PROJECT_ID: p}}\n", "CLOUD_ML_REGION is required"},
		"unknown field":  {base + "  claude: {provider: anthropic, nope: 1}\n", "nope"},
	} {
		_, err := ParseConfig([]byte(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error containing %q, got %v", label, tc.want, err)
		}
		if err != nil && label != "unknown field" && !strings.Contains(err.Error(), "model-spec") {
			t.Errorf("%s: error should name the model-spec block: %v", label, err)
		}
	}
}

// model-provider: was replaced by model-spec: — loading a kit that still has
// it is a hard error naming the replacement, the doc, and the equivalent block.
func TestParseConfig_ModelProviderIsMigrationError(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us-east5
`))
	if err == nil {
		t.Fatal("a kit with model-provider: must not load")
	}
	for _, want := range []string{"model-provider", "model-spec", "docs/usage/at-cove-config.md", "provider: vertex", "provider-env", "ANTHROPIC_VERTEX_PROJECT_ID: my-proj", "CLOUD_ML_REGION: us-east5", modelspec.DefaultClaudeVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("migration error missing %q:\n%v", want, err)
		}
	}
	// The suggested block is itself a valid model-spec: block.
	_, after, ok := strings.Cut(err.Error(), "model-spec:\n")
	if !ok {
		t.Fatalf("no suggested block in %v", err)
	}
	if _, err := ParseConfig([]byte("name: k\nmodel-spec:\n" + after)); err != nil {
		t.Fatalf("the suggested model-spec block does not load: %v", err)
	}
	// Any shape of the old key is the same hard error (never "unknown field").
	_, err = ParseConfig([]byte("name: k\nmodel-provider: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "model-spec") {
		t.Fatalf("an empty model-provider must also get the migration hint, got %v", err)
	}
}
