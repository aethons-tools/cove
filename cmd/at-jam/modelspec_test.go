package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

const modelSpecYAML = `name: claude-default
type: claude
version: "2.1.0"
version-constraint: "2.x"
principal:
  credential: anthropic
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
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithModelSpecs(store, creds, false, log)))
	t.Cleanup(ts.Close)
	return ts, store
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runJam(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestModelSpecCommandLifecycle(t *testing.T) {
	ts, store := modelSpecServer(t)
	path := writeSpec(t, modelSpecYAML)

	if code, out, errs := runJam("model-spec", "add", "--admin-url", ts.URL, path); code != 0 || !strings.Contains(out, "added model-spec claude-default") {
		t.Fatalf("add: code=%d out=%q err=%q", code, out, errs)
	}
	got, ok := store.GetModelSpec("claude-default")
	if !ok || got.Claude == nil || got.Claude.ProviderEnv["CLOUD_ML_REGION"] != "us-east5" ||
		got.Claude.Settings["attribution"].(map[string]any)["commit"] != "" || got.Policy.Mode != "bypassPermissions" || got.VersionConstraint != "2.x" {
		t.Fatalf("stored spec = %+v", got)
	}
	if code, _, errs := runJam("model-spec", "add", "--admin-url", ts.URL, path); code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatalf("duplicate add: code=%d err=%q", code, errs)
	}

	code, out, errs := runJam("model-spec", "list", "--admin-url", ts.URL)
	if code != 0 || !strings.Contains(out, "claude-default\tclaude\t2.1.0\tanthropic\tvertex") {
		t.Fatalf("list: code=%d out=%q err=%q", code, out, errs)
	}

	code, out, errs = runJam("model-spec", "show", "--admin-url", ts.URL, "claude-default")
	if code != 0 || !strings.Contains(out, "name: claude-default") || !strings.Contains(out, "provider-env:") || !strings.Contains(out, "CLOUD_ML_REGION: us-east5") {
		t.Fatalf("show: code=%d out=%q err=%q", code, out, errs)
	}
	// show's output is itself a valid spec file (round-trips through update).
	shown := writeSpec(t, strings.Replace(out, `version: 2.1.0`, `version: 2.1.1`, 1))
	if code, out, errs := runJam("model-spec", "update", "--admin-url", ts.URL, shown); code != 0 || !strings.Contains(out, "updated model-spec claude-default") {
		t.Fatalf("update: code=%d out=%q err=%q", code, out, errs)
	}
	if got, _ := store.GetModelSpec("claude-default"); got.Version != "2.1.1" {
		t.Fatalf("after update version = %q", got.Version)
	}

	if code, out, errs := runJam("model-spec", "delete", "--admin-url", ts.URL, "claude-default"); code != 0 || !strings.Contains(out, "deleted model-spec claude-default") {
		t.Fatalf("delete: code=%d out=%q err=%q", code, out, errs)
	}
	if code, _, _ := runJam("model-spec", "show", "--admin-url", ts.URL, "claude-default"); code != 1 {
		t.Fatalf("show after delete: code=%d, want 1", code)
	}
	if code, _, _ := runJam("model-spec", "update", "--admin-url", ts.URL, path); code != 1 {
		t.Fatalf("update of a deleted spec: code=%d, want 1", code)
	}
}

func TestModelSpecCommandRefusals(t *testing.T) {
	ts, store := modelSpecServer(t)
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"unknown field":   {strings.Replace(modelSpecYAML, "note:", "notes:", 1), "notes"},
		"unknown type":    {strings.Replace(modelSpecYAML, "type: claude", "type: codex", 1), `"codex"`},
		"unknown cred":    {strings.Replace(modelSpecYAML, "credential: anthropic", "credential: ghost", 1), `"ghost"`},
		"pool, no pool":   {strings.Replace(modelSpecYAML, "credential: anthropic", "credential: pool", 1), "pool"},
		"bad mode":        {strings.Replace(modelSpecYAML, "mode: bypassPermissions", "mode: yolo", 1), `"yolo"`},
		"protected env":   {strings.Replace(modelSpecYAML, "CLOUD_ML_REGION: us-east5", "HTTPS_PROXY: http://x", 1), "HTTPS_PROXY"},
		"missing body":    {modelSpecYAML[:strings.Index(modelSpecYAML, "claude:\n")], "claude"},
		"missing version": {strings.Replace(modelSpecYAML, "version: \"2.1.0\"\n", "", 1), "version"},
		"range version":   {strings.Replace(modelSpecYAML, "version: \"2.1.0\"", "version: \"2.x\"", 1), "version-constraint"},
	} {
		code, _, errs := runJam("model-spec", "add", "--admin-url", ts.URL, writeSpec(t, tc.body))
		if code == 0 || !strings.Contains(errs, tc.want) {
			t.Errorf("%s: code=%d err=%q, want a failure mentioning %q", name, code, errs, tc.want)
		}
	}
	if n := len(store.ListModelSpecs()); n != 0 {
		t.Fatalf("refused specs reached the store: %d", n)
	}
	if code, _, _ := runJam("model-spec", "add", "--admin-url", ts.URL); code != 2 {
		t.Fatalf("add without a file: code=%d, want 2", code)
	}
	if code, _, _ := runJam("model-spec", "frob"); code != 2 {
		t.Fatalf("unknown subcommand: code=%d, want 2", code)
	}
	if code, _, _ := runJam("model-spec"); code != 2 {
		t.Fatalf("no subcommand: code=%d, want 2", code)
	}
}
