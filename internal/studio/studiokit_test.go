// internal/studio/studiokit_test.go
package studio

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kit"
)

func TestParseStudioKitRoundTrip(t *testing.T) {
	src := []byte(`kind: studio
name: web
base:
  image: ghcr.io/acme/web@sha256:abc
egress:
  - github.com
  - pkg.go.dev
build-args:
  NODE_VERSION: "20"
secrets:
  AT_TASK_GIT_TOKEN:
    description: git push token
prompt: |
  You maintain the web service.
`)
	sk, err := ParseStudioKit(src)
	if err != nil {
		t.Fatalf("ParseStudioKit: %v", err)
	}
	if sk.LegacyName != "web" || sk.Base.Image != "ghcr.io/acme/web@sha256:abc" {
		t.Fatalf("bad parse: %+v", sk)
	}
	if sk.BuildArgs["NODE_VERSION"] != "20" || sk.Secrets["AT_TASK_GIT_TOKEN"].Description == "" {
		t.Fatalf("bad parse: %+v", sk)
	}
	b, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic: re-marshal equal.
	b2, _ := sk.ToJSON()
	if string(b) != string(b2) {
		t.Fatal("ToJSON not deterministic")
	}
	if !strings.Contains(string(b), `"kind":"studio"`) {
		t.Fatalf("canonical JSON missing kind: %s", b)
	}
}

func TestParseStudioKitRejectsUnknownField(t *testing.T) {
	_, err := ParseStudioKit([]byte("kind: studio\nname: web\nworkers: {}\n"))
	if err == nil {
		t.Fatal("want strict-decode error on an unknown field")
	}
}

func TestParseStudioKitRequiresKind(t *testing.T) {
	if _, err := ParseStudioKit([]byte("egress: [a.com]\n")); err == nil {
		t.Fatal("want error when kind is missing")
	}
	// The name is the registry's (see CheckName), so a kit without one is valid.
	if _, err := ParseStudioKit([]byte("kind: studio\n")); err != nil {
		t.Fatalf("nameless kit refused: %v", err)
	}
}

func TestParseStudioKitRejectsBuildArgCollisions(t *testing.T) {
	// Test table: each case should be rejected by Validate.
	cases := []struct {
		name    string
		desc    string
		data    []byte
		wantErr string // substring of expected error message
	}{
		{
			name: "build-arg collides with secret demand",
			desc: "a custom secret name present in both secrets: and build-args:",
			data: []byte(`kind: studio
name: web
secrets:
  MYSECRET:
    description: a custom secret
build-args:
  MYSECRET: value
`),
			wantErr: "collides with a secret demand",
		},
		{
			name: "build-arg is a reserved secret name",
			desc: "build-arg uses AT_TASK_GIT_TOKEN, a reserved name",
			data: []byte(`kind: studio
name: web
build-args:
  AT_TASK_GIT_TOKEN: value
`),
			wantErr: "reserved secret name",
		},
		{
			name: "base image and context-files both set",
			desc: "more than one base form is set (mutually exclusive)",
			data: []byte(`kind: studio
name: web
base:
  image: ghcr.io/acme/web:latest
  context-files:
    dockerfile: "FROM x"
`),
			wantErr: "exactly one",
		},
		{
			name: "context-files without a dockerfile",
			desc: "base.context-files is missing its required dockerfile",
			data: []byte(`kind: studio
name: web
base:
  context-files:
    readme.txt: hello
`),
			wantErr: "dockerfile` file is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStudioKit(tc.data)
			if err == nil {
				t.Errorf("%s: ParseStudioKit should reject this config, got nil", tc.desc)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: error %q does not contain %q", tc.desc, err, tc.wantErr)
			}
		})
	}
}

func TestStudioKitJSONRoundTrip(t *testing.T) {
	// Regression test: a StudioKit with build-args must survive a JSON round-trip
	// (ToJSON → ParseStudioKit). This guards against JSON/YAML tag mismatches.
	sk := StudioKit{
		Kind: "studio",
		Base: Base{
			Image: "r",
		},
		Egress: []string{"github.com"},
		BuildArgs: map[string]string{
			"NODE_VERSION": "20",
		},
		Secrets: map[string]kit.SecretConfig{
			"MYSECRET": {Description: "x"},
		},
		Prompt: "p",
	}

	// Marshal to JSON.
	jsonBytes, err := sk.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	// Parse back from JSON.
	parsed, err := ParseStudioKit(jsonBytes)
	if err != nil {
		t.Fatalf("ParseStudioKit: %v", err)
	}

	// Verify critical fields survived the round-trip.
	if parsed.BuildArgs["NODE_VERSION"] != "20" {
		t.Errorf("BuildArgs[NODE_VERSION] = %q, want 20", parsed.BuildArgs["NODE_VERSION"])
	}
	if parsed.Base.Image != "r" {
		t.Errorf("Base.Image = %q, want r", parsed.Base.Image)
	}
	if len(parsed.Egress) != 1 || parsed.Egress[0] != "github.com" {
		t.Errorf("Egress = %v, want [github.com]", parsed.Egress)
	}
	if sec, ok := parsed.Secrets["MYSECRET"]; !ok || sec.Description != "x" {
		t.Errorf("Secrets[MYSECRET] = %+v, want Description=x", sec)
	}
	if parsed.Prompt != "p" {
		t.Errorf("Prompt = %q, want p", parsed.Prompt)
	}
}

func TestCheckPromptRejectsOverBudget(t *testing.T) {
	sk := StudioKit{Kind: Kind, Prompt: strings.Repeat("x", sessionctx.BudgetKit+1)}
	if err := sk.CheckPrompt(); err == nil || !strings.Contains(err.Error(), "801 bytes") {
		t.Fatalf("want a budget error naming the size, got %v", err)
	}
	sk.Prompt = strings.Repeat("x", sessionctx.BudgetKit)
	if err := sk.CheckPrompt(); err != nil {
		t.Fatalf("at budget must pass: %v", err)
	}
}

// The budget is an authoring rule: a kit stored before it existed must still
// parse (and so still raise, its prompt truncated by Compile).
func TestValidateAcceptsOverBudgetPrompt(t *testing.T) {
	sk := StudioKit{Kind: Kind, Prompt: strings.Repeat("x", sessionctx.BudgetKit+1)}
	if err := sk.Validate(); err != nil {
		t.Fatalf("Validate must not enforce the authoring budget: %v", err)
	}
}

func TestParseStudioKitMCPServers(t *testing.T) {
	sk, err := ParseStudioKit([]byte(`kind: studio
mcp-servers:
  linear:
    type: http
    url: "${LINEAR_MCP_URL}"
    headers: {Authorization: "Bearer ${LINEAR_TOKEN}"}
`))
	if err != nil {
		t.Fatalf("ParseStudioKit: %v", err)
	}
	l := sk.MCPServers["linear"]
	if l.Type != "http" || l.URL != "${LINEAR_MCP_URL}" || l.Headers["Authorization"] != "Bearer ${LINEAR_TOKEN}" {
		t.Fatalf("bad parse: %+v", sk.MCPServers)
	}
	b, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"mcp-servers":{"linear":{"type":"http","url":"${LINEAR_MCP_URL}","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}}}`) {
		t.Fatalf("canonical JSON: %s", b)
	}
}

func TestParseStudioKitRejectsBadMCPServers(t *testing.T) {
	cases := map[string]string{
		"messaging is reserved": "kind: studio\nmcp-servers:\n  messaging: {type: stdio, command: evil}\n",
		"literal authorization": "kind: studio\nmcp-servers:\n  linear: {type: http, url: \"https://x\", headers: {Authorization: \"Bearer lin_api_x\"}}\n",
		"unknown server field":  "kind: studio\nmcp-servers:\n  s: {type: stdio, command: c, env: {A: b}}\n",
	}
	for name, src := range cases {
		if _, err := ParseStudioKit([]byte(src)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
