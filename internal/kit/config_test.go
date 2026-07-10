package kit

import (
	"strings"
	"testing"
)

func TestParseConfigValid(t *testing.T) {
	data := []byte(`
name: claude-on-myrepo
secrets:
  GITHUB_TOKEN:
    command: ["op", "read", "x"]
  ANTHROPIC_API_KEY:
    description: Anthropic key
    command: ["pass", "show", "y"]
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "claude-on-myrepo" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Secrets) != 2 || len(cfg.Secrets["GITHUB_TOKEN"].Command) != 3 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets["ANTHROPIC_API_KEY"].Description != "Anthropic key" {
		t.Fatalf("description not parsed: %+v", cfg.Secrets["ANTHROPIC_API_KEY"])
	}
}

func TestParseConfigRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":        "secrets:\n  GITHUB_TOKEN: {}\n",
		"secret no name": "name: x\nsecrets:\n  \"\":\n    command: [\"a\"]\n",
	}
	for label, data := range cases {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigRejectsUnknownField(t *testing.T) {
	if _, err := ParseConfig([]byte("name: x\nbogus: 1\n")); err == nil {
		t.Error("expected error on unknown field")
	}
}

func TestParseConfigAllowsCommandlessSecret(t *testing.T) {
	data := []byte("name: x\nsecrets:\n  GITHUB_TOKEN: {}\n")
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("name-only secret should be valid: %v", err)
	}
	s, ok := cfg.Secrets["GITHUB_TOKEN"]
	if len(cfg.Secrets) != 1 || !ok || len(s.Command) != 0 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
}

// Regression guard: literal secret values must NOT be declarable in the kit;
// they belong only in the user's ~/.config/at-cove/secrets.yml. KnownFields(true)
// rejects the unknown `value:` key, so this passes from the start.
func TestParseConfigRejectsSecretValueField(t *testing.T) {
	data := []byte("name: x\nsecrets:\n  T:\n    value: ghp_secret\n")
	if _, err := ParseConfig(data); err == nil {
		t.Fatal("a literal value: in config.yml must be rejected")
	}
}

func TestParseConfigRejectsRemovedSetupField(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nsetup: \"git clone https://x .\"\n"))
	if err == nil {
		t.Fatal("expected error: setup is a removed/unknown field")
	}
}

func TestParseConfigImage(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
image:
  setup-scripts:
    - .install-files/install.sh
  paths:
    - /usr/local/go/bin
  env:
    GOROOT: /usr/local/go
  allowed-domains:
    - .example.com
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScripts) != 1 || cfg.Image.SetupScripts[0] != ".install-files/install.sh" {
		t.Fatalf("SetupScripts = %v", cfg.Image.SetupScripts)
	}
	if len(cfg.Image.Paths) != 1 || cfg.Image.Paths[0] != "/usr/local/go/bin" {
		t.Fatalf("Paths = %v", cfg.Image.Paths)
	}
	if cfg.Image.Env["GOROOT"] != "/usr/local/go" {
		t.Fatalf("Env = %v", cfg.Image.Env)
	}
	if len(cfg.Image.AllowedDomains) != 1 || cfg.Image.AllowedDomains[0] != ".example.com" {
		t.Fatalf("AllowedDomains = %v", cfg.Image.AllowedDomains)
	}
}

func TestParseConfigImageAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScripts) != 0 || len(cfg.Image.Paths) != 0 || len(cfg.Image.Env) != 0 || len(cfg.Image.AllowedDomains) != 0 {
		t.Fatalf("absent image must be zero-valued, got %+v", cfg.Image)
	}
}

func TestParseConfigImageRejectsEmptyScript(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  setup-scripts:\n    - \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), "setup-scripts") {
		t.Fatalf("expected empty setup-scripts error, got %v", err)
	}
}

func TestParseConfigImageRejectsReservedEnvKey(t *testing.T) {
	// PATH is a base-owned key; overriding it would produce a second PATH= line.
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    PATH: /evil\n"))
	if err == nil {
		t.Fatal("expected error for reserved PATH env key, got nil")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("error should mention PATH, got: %v", err)
	}
	if !strings.Contains(err.Error(), "overridden") {
		t.Fatalf("error should mention 'overridden', got: %v", err)
	}

	// Proxy keys are also base-owned (egress gate).
	_, err = ParseConfig([]byte("name: k\nimage:\n  env:\n    https_proxy: http://x\n"))
	if err == nil {
		t.Fatal("expected error for reserved https_proxy env key, got nil")
	}
	if !strings.Contains(err.Error(), "https_proxy") {
		t.Fatalf("error should mention https_proxy, got: %v", err)
	}
}

func TestParseConfigImageRejectsEnvValueNewline(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    FOO: \"a\\nb\"\n"))
	if err == nil {
		t.Fatal("expected error for env value with newline, got nil")
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Fatalf("error should mention 'newline', got: %v", err)
	}
}

func TestParseConfigImageRejectsPathNewline(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  paths:\n    - \"a\\nb\"\n"))
	if err == nil {
		t.Fatal("expected error for path with newline, got nil")
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Fatalf("error should mention 'newline', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyPath(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  paths:\n    - \"\"\n"))
	if err == nil {
		t.Fatal("expected error for empty path entry, got nil")
	}
	if !strings.Contains(err.Error(), "paths") {
		t.Fatalf("error should mention 'paths', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyEnvKey(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    \"\": x\n"))
	if err == nil {
		t.Fatal("expected error for empty env key, got nil")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Fatalf("error should mention 'env', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyDomain(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  allowed-domains:\n    - \"\"\n"))
	if err == nil {
		t.Fatal("expected error for empty domain entry, got nil")
	}
	if !strings.Contains(err.Error(), "allowed-domains") {
		t.Fatalf("error should mention 'allowed-domains', got: %v", err)
	}
}

func TestParseConfigRejectsDispatch(t *testing.T) {
	_, err := ParseConfig([]byte("name: w\ndispatch:\n  command: [\"run-worker.sh\"]\n"))
	if err == nil {
		t.Fatal("expected error for unknown field dispatch, got nil")
	}
}

func TestParseConfigWorkers(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nworkers:\n  implement:\n    prompt: do the thing\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workers["implement"].Prompt != "do the thing" {
		t.Fatalf("workers not parsed: %+v", cfg.Workers)
	}
}

func TestParseConfigRejectsWorkerWithoutPrompt(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nworkers:\n  implement: {}\n"))
	if err == nil {
		t.Fatal("a worker class with no prompt must be rejected")
	}
}

func TestResolvedWorkerMergesCommon(t *testing.T) {
	src := `
name: k
workers:
  <common>:
    timeout: 30m
    concurrency: 2
  implement:
    prompt: "do the thing"
    timeout: 40m
  audit:
    prompt: "check the thing"
    concurrency: 1
`
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	impl, err := cfg.ResolvedWorker("implement")
	if err != nil {
		t.Fatalf("ResolvedWorker(implement): %v", err)
	}
	if impl.Prompt != "do the thing" || impl.Timeout != "40m" || impl.Concurrency != 2 {
		t.Fatalf("implement merge = %+v; want prompt/40m(own)/2(common)", impl)
	}
	aud, err := cfg.ResolvedWorker("audit")
	if err != nil {
		t.Fatalf("ResolvedWorker(audit): %v", err)
	}
	if aud.Timeout != "30m" || aud.Concurrency != 1 {
		t.Fatalf("audit merge = %+v; want 30m(common)/1(own)", aud)
	}
	if _, err := cfg.ResolvedWorker("<common>"); err == nil {
		t.Fatal("ResolvedWorker(<common>) should error (not a real class)")
	}
	if _, err := cfg.ResolvedWorker("nope"); err == nil {
		t.Fatal("ResolvedWorker(nope) should error (absent)")
	}
}

func TestParseConfigRejectsUnknownAngleKey(t *testing.T) {
	src := "name: k\nworkers:\n  <bogus>:\n    timeout: 30m\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of reserved-looking key <bogus>")
	}
}

func TestParseConfigWorkerRequiresPrompt(t *testing.T) {
	src := "name: k\nworkers:\n  implement:\n    timeout: 30m\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected a real worker to require a prompt")
	}
}

func TestParseConfigOrigin(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nsource-control:\n  github:\n    project: acme/myrepo\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.SourceControl == nil || cfg.SourceControl.GitHub == nil || cfg.SourceControl.GitHub.Project != "acme/myrepo" {
		t.Fatalf("source-control not parsed: %+v", cfg.SourceControl)
	}
	if cfg.SourceControl.GitHub.MainBranch != "main" { // default
		t.Fatalf("main-branch default = %q; want main", cfg.SourceControl.GitHub.MainBranch)
	}
}

func TestParseConfigRejectsBadOriginProject(t *testing.T) {
	if _, err := ParseConfig([]byte("name: k\nsource-control:\n  github:\n    project: nope\n")); err == nil {
		t.Fatal("origin.github.project must be owner/name")
	}
}

func TestParseConfigMainBranchOverride(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nsource-control:\n  github:\n    project: acme/myrepo\n    main-branch: develop\n"))
	if cfg.SourceControl.GitHub.MainBranch != "develop" {
		t.Fatalf("main-branch = %q; want develop", cfg.SourceControl.GitHub.MainBranch)
	}
}

const trackerKit = `
name: k
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["true"] }
dispatch:
  concurrency: 1
  reaper-timeout: 45m
collaborators:
  <common>:
    secrets:
      COMMON_TOKEN: { command: ["true"] }
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["true"] }
`

func TestParseConfigTrackerDispatchCollaborators(t *testing.T) {
	cfg, err := ParseConfig([]byte(trackerKit))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Tracker.Linear.Team != "COV" {
		t.Fatalf("tracker not parsed: %+v", cfg.Tracker)
	}
	if cfg.Tracker.Linear.ClassLabelPrefix != "class:" { // default
		t.Fatalf("class-label-prefix default = %q; want class:", cfg.Tracker.Linear.ClassLabelPrefix)
	}
	if cfg.Dispatch == nil || cfg.Dispatch.DispatchOverhead != "15m" { // default
		t.Fatalf("dispatch-overhead default = %+v; want 15m", cfg.Dispatch)
	}
	col, err := cfg.ResolvedCollaborator("triager")
	if err != nil {
		t.Fatalf("ResolvedCollaborator(triager): %v", err)
	}
	if _, ok := col.Secrets["COMMON_TOKEN"]; !ok { // from <common>
		t.Fatalf("collaborator secrets missing COMMON_TOKEN: %+v", col.Secrets)
	}
	if _, ok := col.Secrets["LINEAR_TOKEN"]; !ok { // own
		t.Fatalf("collaborator secrets missing LINEAR_TOKEN: %+v", col.Secrets)
	}
}

func TestParseConfigRejectsUnknownTrackerSecret(t *testing.T) {
	src := strings.Replace(trackerKit, "AT_DISPATCH_TRACKER_TOKEN", "BOGUS_TOKEN", 1)
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of an unknown tracker secret name")
	}
}

func TestParseConfigRejectsMissingTrackerState(t *testing.T) {
	src := strings.Replace(trackerKit, "      blocked: Backlog\n", "", 1)
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection when a tracker state is missing")
	}
}
