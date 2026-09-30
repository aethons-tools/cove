// internal/studio/studiokit_test.go
package studio

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestParseStudioKitRoundTrip(t *testing.T) {
	src := []byte(`kind: studio
name: web
base:
  ref: ghcr.io/acme/web@sha256:abc
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
	if sk.Name != "web" || sk.Base.Ref != "ghcr.io/acme/web@sha256:abc" {
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

func TestParseStudioKitRequiresKindAndName(t *testing.T) {
	if _, err := ParseStudioKit([]byte("name: web\n")); err == nil {
		t.Fatal("want error when kind is missing")
	}
	if _, err := ParseStudioKit([]byte("kind: studio\n")); err == nil {
		t.Fatal("want error when name is missing")
	}
	if _, err := ParseStudioKit([]byte("kind: studio\nname: bad/name\n")); err == nil {
		t.Fatal("want error on a non-tag-safe name")
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
			name: "base.ref and base.dockerfile both set",
			desc: "both base.ref and base.dockerfile are set (mutually exclusive)",
			data: []byte(`kind: studio
name: web
base:
  ref: ghcr.io/acme/web:latest
  dockerfile: path/to/Dockerfile
`),
			wantErr: "mutually exclusive",
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
		Name: "web",
		Base: Base{
			Ref: "r",
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
	if parsed.Name != "web" {
		t.Errorf("Name = %q, want web", parsed.Name)
	}
	if parsed.Base.Ref != "r" {
		t.Errorf("Base.Ref = %q, want r", parsed.Base.Ref)
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
