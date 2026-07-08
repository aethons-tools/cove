package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionPrintsStampedValue(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"

	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr: %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "1.2.3" {
		t.Fatalf("stdout = %q; want %q", got, "1.2.3")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "at-dispatch.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodConfig = `
tracker:
  provider: linear
  team: AET
  token:          { command: ["true"] }
  webhook-secret: { command: ["true"] }
  poll-interval: 60s
  states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
repo:
  slug: aethons-tools/cove
  source-branch: main
classes:
  implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m }
concurrency: 1
reaper-timeout: 45m
`

func TestServeTokenResolveFailure(t *testing.T) {
	// Valid config, but the tracker token resolver command fails → serve exits 1
	// before constructing the Linear client (no network needed).
	cfg := strings.Replace(goodConfig, `token:          { command: ["true"] }`, `token:          { command: ["false"] }`, 1)
	p := writeConfig(t, cfg)
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--config", p}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q; want a token-resolution error", errOut.String())
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	p := writeConfig(t, "repo:\n  slug: not-a-slug\n")
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--config", p}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1", code)
	}
	if !strings.Contains(errOut.String(), "config:") {
		t.Fatalf("stderr = %q; want a config error", errOut.String())
	}
}

func TestServeRequiresConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"serve"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "--config") {
		t.Fatalf("stderr = %q; want mention of --config", errOut.String())
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"bogus"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") ||
		!strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want 'unknown command' and 'Usage:'", errOut.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want usage text", errOut.String())
	}
}
