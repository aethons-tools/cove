package jam

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// validSpec is a fully-populated, valid claude model-spec; tests mutate copies.
func validSpec() ModelSpec {
	return ModelSpec{
		Name:      "claude-default",
		Type:      HarnessClaude,
		Version:   "2.1.0",
		Principal: ModelPrincipal{Credential: "anthropic"},
		Model:     ModelChoice{ID: "claude-opus-5-5", Effort: "high"},
		Policy:    ModelPolicy{Mode: "bypassPermissions", Allow: []string{"Bash(go test:*)"}, Deny: []string{"WebFetch"}},
		Note:      "default Claude spec",
		Claude: &ClaudeSpec{
			Provider:    "vertex",
			ProviderEnv: map[string]string{"ANTHROPIC_VERTEX_PROJECT_ID": "proj", "CLOUD_ML_REGION": "us-east5"},
			Settings:    map[string]any{"theme": "dark", "attribution": map[string]any{"commit": ""}},
			Plugins:     []string{"superpowers@claude-plugins-official"},
		},
	}
}

func TestValidateModelSpecAcceptsValid(t *testing.T) {
	if err := ValidateModelSpec(validSpec(), credIs("anthropic"), false); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	// Every supported Claude permission mode is accepted, and an unset mode
	// (bypassPermissions, the legacy default).
	for _, mode := range []string{"", "default", "acceptEdits", "bypassPermissions", "dontAsk"} {
		m := validSpec()
		m.Policy.Mode = mode
		if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
			t.Errorf("mode %q refused: %v", mode, err)
		}
	}
	for _, p := range []string{"anthropic", "vertex", "bedrock"} {
		m := validSpec()
		m.Claude.Provider = p
		if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
			t.Errorf("provider %q refused: %v", p, err)
		}
	}
	// Minimal: only the required fields.
	minimal := ModelSpec{Name: "m", Type: HarnessClaude, Version: "2.1.0", Principal: ModelPrincipal{Credential: "anthropic"}, Claude: &ClaudeSpec{Provider: "anthropic"}}
	if err := ValidateModelSpec(minimal, credIs("anthropic"), false); err != nil {
		t.Fatalf("minimal spec refused: %v", err)
	}
	// `pool` is a principal only when a pool is configured.
	pooled := validSpec()
	pooled.Principal.Credential = PoolPrincipal
	if err := ValidateModelSpec(pooled, credIs(), true); err != nil {
		t.Fatalf("pool principal with a pool configured refused: %v", err)
	}
}

func TestValidateModelSpecRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*ModelSpec)
		want string
	}{
		"no name":            {func(m *ModelSpec) { m.Name = "" }, "name"},
		"no type":            {func(m *ModelSpec) { m.Type = "" }, "type"},
		"unknown type":       {func(m *ModelSpec) { m.Type = "codex" }, `"codex"`},
		"no version":         {func(m *ModelSpec) { m.Version = " " }, "version"},
		"no principal":       {func(m *ModelSpec) { m.Principal.Credential = "" }, "principal.credential"},
		"unknown credential": {func(m *ModelSpec) { m.Principal.Credential = "ghost" }, `"ghost"`},
		"pool without pool":  {func(m *ModelSpec) { m.Principal.Credential = PoolPrincipal }, "pool"},
		"bad mode":           {func(m *ModelSpec) { m.Policy.Mode = "yolo" }, `"yolo"`},
		"plan mode":          {func(m *ModelSpec) { m.Policy.Mode = "plan" }, "never leave plan mode"},
		"empty allow entry":  {func(m *ModelSpec) { m.Policy.Allow = []string{""} }, "allow"},
		"empty deny entry":   {func(m *ModelSpec) { m.Policy.Deny = []string{" "} }, "deny"},
		"padded allow rule":  {func(m *ModelSpec) { m.Policy.Allow = []string{"Read "} }, "whitespace"},
		"padded deny rule":   {func(m *ModelSpec) { m.Policy.Deny = []string{"\tBash"} }, "whitespace"},
		"long note":          {func(m *ModelSpec) { m.Note = strings.Repeat("n", MaxModelSpecNote+1) }, "note"},
		"missing body":       {func(m *ModelSpec) { m.Claude = nil }, "claude"},
		"no provider":        {func(m *ModelSpec) { m.Claude.Provider = "" }, "provider"},
		"unknown provider":   {func(m *ModelSpec) { m.Claude.Provider = "azure" }, `"azure"`},
		"protected env":      {func(m *ModelSpec) { m.Claude.ProviderEnv["HTTPS_PROXY"] = "http://evil" }, "HTTPS_PROXY"},
		"protected path":     {func(m *ModelSpec) { m.Claude.ProviderEnv["PATH"] = "/tmp" }, "PATH"},
		"secret env":         {func(m *ModelSpec) { m.Claude.ProviderEnv["ANTHROPIC_API_KEY"] = "x" }, "ANTHROPIC_API_KEY"},
		"aws secret env":     {func(m *ModelSpec) { m.Claude.ProviderEnv["AWS_SECRET_ACCESS_KEY"] = "x" }, "AWS_SECRET_ACCESS_KEY"},
		"reserved env":       {func(m *ModelSpec) { m.Claude.ProviderEnv["AT_JAM_X"] = "1" }, "reserved"},
		"bad env key":        {func(m *ModelSpec) { m.Claude.ProviderEnv["not-a-var"] = "1" }, "not-a-var"},
		"settings env":       {func(m *ModelSpec) { m.Claude.Settings["env"] = map[string]any{"A": "b"} }, `"env"`},
		"settings perms":     {func(m *ModelSpec) { m.Claude.Settings["permissions"] = map[string]any{} }, `"permissions"`},
		"settings helper":    {func(m *ModelSpec) { m.Claude.Settings["apiKeyHelper"] = "cat key" }, `"apiKeyHelper"`},
		"settings hooks":     {func(m *ModelSpec) { m.Claude.Settings["hooks"] = map[string]any{} }, `"hooks"`},
		"settings statusLine": {func(m *ModelSpec) {
			m.Claude.Settings["statusLine"] = map[string]any{"type": "command", "command": "id"}
		}, `"statusLine"`},
		"settings disableAllHooks":  {func(m *ModelSpec) { m.Claude.Settings["disableAllHooks"] = true }, `"disableAllHooks"`},
		"settings project mcp":      {func(m *ModelSpec) { m.Claude.Settings["enableAllProjectMcpServers"] = true }, `"enableAllProjectMcpServers"`},
		"settings enabled mcpjson":  {func(m *ModelSpec) { m.Claude.Settings["enabledMcpjsonServers"] = []any{"x"} }, `"enabledMcpjsonServers"`},
		"settings disabled mcpjson": {func(m *ModelSpec) { m.Claude.Settings["disabledMcpjsonServers"] = []any{"x"} }, `"disabledMcpjsonServers"`},
		"settings allowed mcp":      {func(m *ModelSpec) { m.Claude.Settings["allowedMcpServers"] = []any{} }, `"allowedMcpServers"`},
		"settings denied mcp":       {func(m *ModelSpec) { m.Claude.Settings["deniedMcpServers"] = []any{} }, `"deniedMcpServers"`},
		"settings enabledPlugins":   {func(m *ModelSpec) { m.Claude.Settings["enabledPlugins"] = map[string]any{"x@y": true} }, `"enabledPlugins"`},
		"settings marketplaces":     {func(m *ModelSpec) { m.Claude.Settings["extraKnownMarketplaces"] = map[string]any{} }, `"extraKnownMarketplaces"`},
		"settings non-json":         {func(m *ModelSpec) { m.Claude.Settings["bad"] = make(chan int) }, "settings"},
		"empty plugin":              {func(m *ModelSpec) { m.Claude.Plugins = []string{""} }, "plugin"},
		"duplicate plugin":          {func(m *ModelSpec) { m.Claude.Plugins = []string{"a", "a"} }, `"a"`},
	} {
		m := validSpec()
		tc.mut(&m)
		err := ValidateModelSpec(m, credIs("anthropic"), false)
		if err == nil {
			t.Errorf("%s: accepted, want a 400 mentioning %q", name, tc.want)
			continue
		}
		if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 400 mentioning %q", name, err, tc.want)
		}
	}
}

// The accepted modes are the leaf package's (one source of truth), and plan —
// which a headless cove can never leave — is not offered.
func TestClaudePermissionModesFromLeaf(t *testing.T) {
	got := ClaudePermissionModes()
	if !slices.Equal(got, modelspec.PermissionModes()) {
		t.Fatalf("ClaudePermissionModes() = %v, want modelspec.PermissionModes() = %v", got, modelspec.PermissionModes())
	}
	if slices.Contains(got, "plan") || !slices.Contains(got, modelspec.ModeBypassPermissions) || !slices.Contains(got, "dontAsk") {
		t.Fatalf("modes = %v", got)
	}
}

func TestValidateModelSpecNeverEchoesEnvValues(t *testing.T) {
	m := validSpec()
	m.Claude.ProviderEnv["ANTHROPIC_AUTH_TOKEN"] = "sk-secret-value"
	err := ValidateModelSpec(m, credIs("anthropic"), false)
	if err == nil || strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("err = %v; want a refusal that does not echo the value", err)
	}
}

func TestCreateUpdateGetDeleteModelSpec(t *testing.T) {
	s := NewMemStore()
	creds := credIs("anthropic")
	m := validSpec()
	if err := UpdateModelSpec(s, m, creds, false); WriteStatus(err, 0) != http.StatusNotFound {
		t.Fatalf("update of a missing spec = %v, want 404", err)
	}
	if err := CreateModelSpec(s, m, creds, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := CreateModelSpec(s, m, creds, false); WriteStatus(err, 0) != http.StatusConflict {
		t.Fatalf("duplicate create = %v, want 409", err)
	}
	m.Version = "2.1.1"
	if err := UpdateModelSpec(s, m, creds, false); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, ok := s.GetModelSpec(m.Name); !ok || got.Version != "2.1.1" {
		t.Fatalf("after update: %+v %v", got, ok)
	}
	bad := m
	bad.Type = "codex"
	if err := UpdateModelSpec(s, bad, creds, false); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("invalid update = %v, want 400", err)
	}
	if err := s.RemoveModelSpec(m.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetModelSpec(m.Name); ok {
		t.Fatal("spec still present after remove")
	}
}
