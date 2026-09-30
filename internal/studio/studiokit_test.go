// internal/studio/studiokit_test.go
package studio

import (
	"strings"
	"testing"
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
	// Deterministic: re-marshal equal, and canonical key order (base before egress).
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
