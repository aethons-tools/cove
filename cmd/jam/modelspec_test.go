package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"gopkg.in/yaml.v3"
)

const modelSpecYAML = `name: claude-default
type: claude
version: "2.1.0"
version-constraint: "2.x"
principal:
  credential: anthropic
  headers:
    - name: anthropic-beta
      ensure-list-item: oauth-2025-04-20
    - name: X-Foo
      set: bar
model:
  id: claude-opus-5-5
policy:
  mode: bypassPermissions
  allow: ["Bash(go test:*)"]
note: default Claude spec
claude:
  provider: vertex
  provider-env:
    ANTHROPIC_VERTEX_PROJECT_ID: my-proj
    CLOUD_ML_REGION: us-east5
  settings:
    theme: dark
    attribution:
      commit: ""
  plugins: [superpowers@claude-plugins-official]
`

func modelSpecServer(t *testing.T) (*httptest.Server, jam.Store) {
	t.Helper()
	store := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(n string) bool { return n == "anthropic" }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
		jam.WithModelSpecs(store)))
	t.Cleanup(ts.Close)
	return ts, store
}

func runJam(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

// `model-spec list|show` read what the store holds (specs change only in git).
func TestModelSpecReadCommands(t *testing.T) {
	ts, store := modelSpecServer(t)
	var m jam.ModelSpec
	if err := yaml.Unmarshal([]byte(modelSpecYAML), &m); err != nil {
		t.Fatal(err)
	}
	if err := store.PutModelSpec(m); err != nil {
		t.Fatal(err)
	}

	code, out, errs := runJam("model-spec", "list", "--admin-url", ts.URL)
	if code != 0 || !strings.Contains(out, "claude-default\tclaude\t2.1.0\tanthropic\tvertex") {
		t.Fatalf("list: code=%d out=%q err=%q", code, out, errs)
	}

	code, out, errs = runJam("model-spec", "show", "--admin-url", ts.URL, "claude-default")
	if code != 0 || !strings.Contains(out, "name: claude-default") || !strings.Contains(out, "provider-env:") || !strings.Contains(out, "CLOUD_ML_REGION: us-east5") ||
		!strings.Contains(out, "ensure-list-item: oauth-2025-04-20") {
		t.Fatalf("show: code=%d out=%q err=%q", code, out, errs)
	}
	if code, _, _ := runJam("model-spec", "show", "--admin-url", ts.URL, "ghost"); code != 1 {
		t.Fatalf("show of an absent spec: code=%d, want 1", code)
	}
	if code, _, _ := runJam("model-spec", "show", "--admin-url", ts.URL); code != 2 {
		t.Fatalf("show without a name: code=%d, want 2", code)
	}
	if code, _, _ := runJam("model-spec", "frob"); code != 2 {
		t.Fatalf("unknown subcommand: code=%d, want 2", code)
	}
	if code, _, _ := runJam("model-spec"); code != 2 {
		t.Fatalf("no subcommand: code=%d, want 2", code)
	}
}
