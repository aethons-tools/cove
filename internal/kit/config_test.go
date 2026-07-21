package kit

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigValid(t *testing.T) {
	data := []byte(`
name: claude-on-myrepo
secrets:
  GITHUB_TOKEN: {}
  SOME_OTHER_TOKEN:
    description: some other key
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "claude-on-myrepo" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Secrets) != 2 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets["SOME_OTHER_TOKEN"].Description != "some other key" {
		t.Fatalf("description not parsed: %+v", cfg.Secrets["SOME_OTHER_TOKEN"])
	}
}

func TestSecretConfigIsDemandOnly(t *testing.T) {
	// A command: under a kit secret is now an unknown field (KnownFields(true)).
	_, err := ParseConfig([]byte(`
name: k
secrets:
  FOO: { description: "d", command: ["x"] }
`))
	if err == nil {
		t.Fatal("want error: command: is no longer allowed under a kit secret")
	}
}

func TestParseConfigRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":        "secrets:\n  GITHUB_TOKEN: {}\n",
		"secret no name": "name: x\nsecrets:\n  \"\": {}\n",
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
	cfg, err := ParseConfig([]byte("name: k\nimage:\n  base: ghcr.io/x/y@sha256:abc\n  allowed-domains:\n    - .example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Base != "ghcr.io/x/y@sha256:abc" {
		t.Fatalf("Base = %q", cfg.Image.Base)
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
	if cfg.Image.Base != "" || len(cfg.Image.AllowedDomains) != 0 {
		t.Fatalf("absent image must be zero-valued, got %+v", cfg.Image)
	}
}

// The retired setup-scripts/env/paths fields (COV-34) are rejected as unknown,
// so a stale kit config fails loudly instead of being silently ignored.
func TestParseConfigImageRejectsRetiredFields(t *testing.T) {
	for _, field := range []string{"setup-scripts:\n    - x", "paths:\n    - /x", "env:\n    K: v"} {
		if _, err := ParseConfig([]byte("name: k\nimage:\n  " + field + "\n")); err == nil {
			t.Fatalf("image field %q must be rejected as unknown", field)
		}
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

func TestResolvedWorkerMergesCommonSecrets(t *testing.T) {
	cfg := Config{Name: "k", Workers: map[string]Worker{
		commonKey:     {Secrets: map[string]SecretConfig{"ANTHROPIC_AUTH_TOKEN": {}, "SHARED": {}}},
		"implementor": {Prompt: "impl", Secrets: map[string]SecretConfig{"SHARED": {Description: "own wins"}}},
	}}
	w, err := cfg.ResolvedWorker("implementor")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Secrets["ANTHROPIC_AUTH_TOKEN"]; !ok {
		t.Fatal("class must inherit <common> worker secret")
	}
	if w.Secrets["SHARED"].Description != "own wins" {
		t.Fatalf("own secret must override <common>; got %+v", w.Secrets["SHARED"])
	}
}

func TestWorkerBucketRejectsReservedName(t *testing.T) {
	yml := "name: k\nworkers:\n  implementor:\n    prompt: p\n    timeout: 30m\n    secrets:\n      AT_TASK_GIT_TOKEN: {}\n"
	if _, err := ParseConfig([]byte(yml)); err == nil {
		t.Fatal("a reserved subsystem name under workers.secrets must be rejected")
	}
}

// The Vertex ADC demand name must never be declarable as a general secret
// (root, worker, or collaborator bucket) — that would inject the resolved GCP
// credential into the agent's session env, breaking the air-gap invariant that
// it is only ever seeded as a file by connect.Connect.
func TestParseConfigRejectsGCPADCUnderRootSecrets(t *testing.T) {
	yml := "name: k\nsecrets:\n  GOOGLE_APPLICATION_CREDENTIALS_JSON: {}\n"
	_, err := ParseConfig([]byte(yml))
	if err == nil {
		t.Fatal("GOOGLE_APPLICATION_CREDENTIALS_JSON under root secrets must be rejected")
	}
	if !strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS_JSON") {
		t.Fatalf("error should mention the name; got %v", err)
	}
	if !strings.Contains(err.Error(), "session env") {
		t.Fatalf("error should explain the air-gap risk, not the generic subsystem message; got %v", err)
	}
}

func TestParseConfigRejectsGCPADCUnderCollaboratorSecrets(t *testing.T) {
	yml := "name: k\ncollaborators:\n  planner:\n    prompt: p\n    secrets:\n      GOOGLE_APPLICATION_CREDENTIALS_JSON: {}\n"
	_, err := ParseConfig([]byte(yml))
	if err == nil {
		t.Fatal("GOOGLE_APPLICATION_CREDENTIALS_JSON under collaborators.<class>.secrets must be rejected")
	}
	if !strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS_JSON") {
		t.Fatalf("error should mention the name; got %v", err)
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

func TestGitTokenNameDemandOnly(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
source-control:
  github:
    project: o/r
    secrets:
      AT_TASK_GIT_TOKEN: { description: "push+PR token" }
`))
	if err != nil {
		t.Fatal(err)
	}
	name, ok := cfg.GitTokenName()
	if !ok || name != "AT_TASK_GIT_TOKEN" {
		t.Fatalf("GitTokenName() = %q,%v", name, ok)
	}
}

func TestParseConfigRejectsUnknownSourceControlSecret(t *testing.T) {
	src := "name: k\nsource-control:\n  github:\n    project: a/b\n    secrets:\n      BOGUS: {}\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of an unknown source-control secret name")
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
      AT_DISPATCH_TRACKER_TOKEN:  {}
      AT_DISPATCH_WEBHOOK_SECRET: {}
dispatch:
  concurrency: 1
  reaper-timeout: 45m
collaborators:
  <common>:
    secrets:
      COMMON_TOKEN: {}
  triager:
    secrets:
      LINEAR_TOKEN: {}
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

func TestParseConfigWellKnownSecretMissingKeyRejected(t *testing.T) {
	// Dropping a required well-known key is still an error (typo protection).
	src := `
name: k
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
    secrets:
      AT_DISPATCH_TRACKER_TOKEN: {}
`
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("missing AT_DISPATCH_WEBHOOK_SECRET must be rejected")
	}
}

func TestParseConfigRejectsWellKnownNameInRootSecrets(t *testing.T) {
	for _, name := range []string{"AT_TASK_GIT_TOKEN", "AT_DISPATCH_TRACKER_TOKEN", "AT_DISPATCH_WEBHOOK_SECRET"} {
		src := "name: k\nsecrets:\n  " + name + ": {}\n"
		if _, err := ParseConfig([]byte(src)); err == nil {
			t.Fatalf("root secrets must not accept the reserved name %q", name)
		}
	}
}

func TestParseConfigRejectsWellKnownNameInCollaboratorSecrets(t *testing.T) {
	src := "name: k\ncollaborators:\n  triager:\n    secrets:\n      AT_TASK_GIT_TOKEN: {}\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("collaborator secrets must not accept a reserved subsystem name")
	}
}

func TestRootBearerIsRejectedWithMigrationNote(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		yml := "name: k\nsecrets:\n  " + name + ": {}\n"
		_, err := ParseConfig([]byte(yml))
		if err == nil {
			t.Fatalf("%s at root must be rejected", name)
		}
		if !strings.Contains(err.Error(), "workers") {
			t.Fatalf("%s error must point to workers.<class>.secrets; got %v", name, err)
		}
	}
	// The same name under workers is fine.
	ok := "name: k\nworkers:\n  implementor:\n    prompt: p\n    timeout: 30m\n    secrets:\n      ANTHROPIC_AUTH_TOKEN: {}\n"
	if _, err := ParseConfig([]byte(ok)); err != nil {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN under workers must load; got %v", err)
	}
}

func TestCollaboratorPromptAndDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
collaborators:
  <common>:
    secrets: {}
  triager:
    default: true
    prompt: "you are the steward"
    secrets:
      LINEAR_TOKEN: { description: "d" }
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cfg.ResolvedCollaborator("triager")
	if err != nil {
		t.Fatal(err)
	}
	if c.Prompt != "you are the steward" || !c.Default {
		t.Fatalf("resolved = %+v", c)
	}
}

func TestCollaboratorAtMostOneDefault(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
collaborators:
  a: { default: true, prompt: "x" }
  b: { default: true, prompt: "y" }
`))
	if err == nil {
		t.Fatal("want error: two collaborators marked default")
	}
}

func TestCollaboratorCommonNoRole(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
collaborators:
  <common>: { prompt: "nope" }
`))
	if err == nil {
		t.Fatal("want error: <common> must not set a prompt")
	}
}

// share-repo-dir is a per-class opt-in (like prompt/default): the selected
// collaborator's VM shares the kit's repo dir instead of an isolated volume.
func TestCollaboratorShareRepoDir(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
collaborators:
  triager:
    prompt: "be triager"
    share-repo-dir: true
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cfg.ResolvedCollaborator("triager")
	if err != nil {
		t.Fatal(err)
	}
	if !c.ShareRepoDir {
		t.Fatalf("resolved collaborator should carry share-repo-dir: %+v", c)
	}
}

// share-repo-dir on the <common> base is a hard config error — it is per-class
// opt-in only, never inherited.
func TestCollaboratorCommonRejectsShareRepoDir(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
collaborators:
  <common>: { share-repo-dir: true }
  triager: { prompt: "be triager" }
`))
	if err == nil {
		t.Fatal("want error: <common> must not set share-repo-dir")
	}
}

func selCfg(t *testing.T, body string) Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSelectCollaborator(t *testing.T) {
	none := selCfg(t, "name: k\n")
	sole := selCfg(t, "name: k\ncollaborators:\n  triager: { prompt: x }\n")
	multi := selCfg(t, "name: k\ncollaborators:\n  a: { prompt: x }\n  b: { prompt: y, default: true }\n")
	multiNoDef := selCfg(t, "name: k\ncollaborators:\n  a: { prompt: x }\n  b: { prompt: y }\n")

	// none defined -> plain session
	if class, ok, err := none.SelectCollaborator(""); err != nil || ok || class != "" {
		t.Fatalf("none: %q,%v,%v", class, ok, err)
	}
	// sole, no explicit -> that one
	if class, ok, err := sole.SelectCollaborator(""); err != nil || !ok || class != "triager" {
		t.Fatalf("sole: %q,%v,%v", class, ok, err)
	}
	// multi, no explicit -> the default
	if class, ok, err := multi.SelectCollaborator(""); err != nil || !ok || class != "b" {
		t.Fatalf("multi-default: %q,%v,%v", class, ok, err)
	}
	// multi, no default, no explicit -> error
	if _, _, err := multiNoDef.SelectCollaborator(""); err == nil {
		t.Fatal("multi-no-default: want error")
	}
	// explicit match
	if class, ok, err := multi.SelectCollaborator("a"); err != nil || !ok || class != "a" {
		t.Fatalf("explicit: %q,%v,%v", class, ok, err)
	}
	// explicit miss -> error
	if _, _, err := sole.SelectCollaborator("nope"); err == nil {
		t.Fatal("explicit-miss: want error")
	}
	// <common> is not selectable
	if _, _, err := sole.SelectCollaborator(commonKey); err == nil {
		t.Fatal("<common> must not be selectable")
	}
}

func TestResolvedWorkerDomainsUnion(t *testing.T) {
	cfg := Config{Name: "k", Workers: map[string]Worker{
		commonKey: {AllowedDomains: []string{"github.com", "pypi.org"}},
		"deploy":  {Prompt: "p", AllowedDomains: []string{"registry.example.com", "github.com"}},
		"docs":    {Prompt: "p"},
	}}
	// <common> ∪ class, deduped + order-normalized (sorted).
	got, err := cfg.ResolvedWorkerDomains("deploy")
	if err != nil {
		t.Fatalf("ResolvedWorkerDomains(deploy): %v", err)
	}
	want := []string{"github.com", "pypi.org", "registry.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deploy domains = %v; want %v", got, want)
	}
	// A class with no own list gets exactly <common>.
	got, err = cfg.ResolvedWorkerDomains("docs")
	if err != nil {
		t.Fatalf("ResolvedWorkerDomains(docs): %v", err)
	}
	if want := []string{"github.com", "pypi.org"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("docs domains = %v; want %v", got, want)
	}
	// Root (image.allowed-domains) is NOT part of the per-class resolver.
	if _, err := cfg.ResolvedWorkerDomains(commonKey); err == nil {
		t.Fatal("ResolvedWorkerDomains(<common>) should error")
	}
	if _, err := cfg.ResolvedWorkerDomains("nope"); err == nil {
		t.Fatal("ResolvedWorkerDomains(nope) should error (absent)")
	}
	if _, err := cfg.ResolvedWorkerDomains(""); err == nil {
		t.Fatal("ResolvedWorkerDomains(\"\") should error")
	}
}

func TestResolvedWorkerDomainsEmpty(t *testing.T) {
	cfg := Config{Name: "k", Workers: map[string]Worker{
		"docs": {Prompt: "p"},
	}}
	got, err := cfg.ResolvedWorkerDomains("docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("no <common> and no own list should yield empty; got %v", got)
	}
}

func TestResolvedCollaboratorDomainsUnion(t *testing.T) {
	cfg := Config{Name: "k", Collaborators: map[string]Collaborator{
		commonKey: {AllowedDomains: []string{"docs.internal"}},
		"planner": {AllowedDomains: []string{"linear.app", "docs.internal"}},
		"triager": {},
	}}
	got, err := cfg.ResolvedCollaboratorDomains("planner")
	if err != nil {
		t.Fatalf("ResolvedCollaboratorDomains(planner): %v", err)
	}
	if want := []string{"docs.internal", "linear.app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planner domains = %v; want %v", got, want)
	}
	got, err = cfg.ResolvedCollaboratorDomains("triager")
	if err != nil {
		t.Fatalf("ResolvedCollaboratorDomains(triager): %v", err)
	}
	if want := []string{"docs.internal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("triager domains = %v; want %v", got, want)
	}
	if _, err := cfg.ResolvedCollaboratorDomains(commonKey); err == nil {
		t.Fatal("ResolvedCollaboratorDomains(<common>) should error")
	}
	if _, err := cfg.ResolvedCollaboratorDomains("nope"); err == nil {
		t.Fatal("ResolvedCollaboratorDomains(nope) should error (absent)")
	}
}

func TestParseConfigWorkerDomainsParsedAndValidated(t *testing.T) {
	src := "name: k\nworkers:\n  <common>:\n    allowed-domains: [github.com]\n  deploy:\n    prompt: p\n    allowed-domains: [registry.example.com]\n"
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Workers["deploy"].AllowedDomains; len(got) != 1 || got[0] != "registry.example.com" {
		t.Fatalf("deploy.allowed-domains = %v", got)
	}
	if got := cfg.Workers[commonKey].AllowedDomains; len(got) != 1 || got[0] != "github.com" {
		t.Fatalf("<common>.allowed-domains = %v", got)
	}
	// Empty entry rejected, mirroring image.allowed-domains.
	bad := "name: k\nworkers:\n  deploy:\n    prompt: p\n    allowed-domains: [\"\"]\n"
	if _, err := ParseConfig([]byte(bad)); err == nil || !strings.Contains(err.Error(), "allowed-domains") {
		t.Fatalf("empty worker domain must be rejected mentioning allowed-domains; got %v", err)
	}
}

func TestParseConfigCollaboratorDomainsParsedAndValidated(t *testing.T) {
	src := "name: k\ncollaborators:\n  <common>:\n    allowed-domains: [docs.internal]\n  planner:\n    allowed-domains: [linear.app]\n"
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Collaborators["planner"].AllowedDomains; len(got) != 1 || got[0] != "linear.app" {
		t.Fatalf("planner.allowed-domains = %v", got)
	}
	bad := "name: k\ncollaborators:\n  planner:\n    allowed-domains: [\"  \"]\n"
	if _, err := ParseConfig([]byte(bad)); err == nil || !strings.Contains(err.Error(), "allowed-domains") {
		t.Fatalf("empty collaborator domain must be rejected mentioning allowed-domains; got %v", err)
	}
}

func TestParseConfig_VertexValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us-east5
      ANTHROPIC_MODEL: claude-opus-4-8
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	v, ok := cfg.Vertex()
	if !ok {
		t.Fatalf("Vertex() ok = false, want true")
	}
	if v.Env["ANTHROPIC_VERTEX_PROJECT_ID"] != "my-proj" {
		t.Fatalf("project id = %q", v.Env["ANTHROPIC_VERTEX_PROJECT_ID"])
	}
	env := cfg.VertexEnv()
	if env["CLAUDE_CODE_USE_VERTEX"] != "1" {
		t.Fatalf("VertexEnv missing CLAUDE_CODE_USE_VERTEX=1: %v", env)
	}
	if env["ANTHROPIC_MODEL"] != "claude-opus-4-8" || env["CLOUD_ML_REGION"] != "us-east5" {
		t.Fatalf("VertexEnv passthrough wrong: %v", env)
	}
}

func TestParseConfig_VertexMissingRequired(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
`))
	if err == nil || !strings.Contains(err.Error(), "CLOUD_ML_REGION is required") {
		t.Fatalf("want CLOUD_ML_REGION required error, got %v", err)
	}
}

func TestParseConfig_VertexRejectsProtectedKey(t *testing.T) {
	_, err := ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-proj
      CLOUD_ML_REGION: us
      https_proxy: http://evil:3128
`))
	if err == nil || !strings.Contains(err.Error(), "https_proxy") {
		t.Fatalf("want protected-key rejection for https_proxy, got %v", err)
	}
}

func TestParseConfig_ModelProviderEmptyUnionRejected(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nmodel-provider: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "must set exactly one provider") {
		t.Fatalf("empty model-provider union must be rejected mentioning 'must set exactly one provider'; got %v", err)
	}
}

func TestVertexEnv_NilWhenNoProvider(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.VertexEnv() != nil {
		t.Fatalf("VertexEnv should be nil for a non-vertex kit")
	}
	if _, ok := cfg.Vertex(); ok {
		t.Fatalf("Vertex() ok = true for a non-vertex kit")
	}
}

func TestProviderDomains_Vertex(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
image:
  allowed-domains: [example.com]
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	root := RootDomains(cfg)
	joined := strings.Join(root, ",")
	for _, want := range []string{
		"example.com",                        // kit root preserved
		"aiplatform.googleapis.com",          // base vertex host
		"us-east5-aiplatform.googleapis.com", // regional host
		"oauth2.googleapis.com",              // ADC refresh
		"sts.googleapis.com",
		"iamcredentials.googleapis.com",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RootDomains missing %q; got %v", want, root)
		}
	}
}

func TestProviderDomains_NilWhenNoProvider(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nimage:\n  allowed-domains: [only.example]\n"))
	if ProviderDomains(cfg) != nil {
		t.Fatalf("ProviderDomains should be nil for a non-vertex kit")
	}
	if got := RootDomains(cfg); len(got) != 1 || got[0] != "only.example" {
		t.Fatalf("RootDomains = %v, want [only.example]", got)
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestParseConfig_GitLabValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
source-control:
  gitlab:
    project: group/subgroup/app
    secrets:
      AT_TASK_GIT_TOKEN: { description: pat }
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	repo, ok := cfg.SourceControl.Repo()
	if !ok {
		t.Fatalf("Repo() ok=false")
	}
	if repo.Provider != "gitlab" || repo.Host != "gitlab.com" || repo.Project != "group/subgroup/app" || repo.MainBranch != "main" {
		t.Fatalf("repo = %+v", repo)
	}
	if repo.CloneURL() != "https://gitlab.com/group/subgroup/app.git" {
		t.Fatalf("clone url = %q", repo.CloneURL())
	}
	if name, ok := cfg.GitTokenName(); !ok || name != "AT_TASK_GIT_TOKEN" {
		t.Fatalf("GitTokenName = %q,%v", name, ok)
	}
}

func TestParseConfig_GitLabSelfHostedDomainDerived(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nsource-control:\n  gitlab:\n    host: gitlab.example.com\n    project: g/app\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := SourceControlDomains(cfg); len(got) != 1 || got[0] != "gitlab.example.com" {
		t.Fatalf("SourceControlDomains = %v", got)
	}
	if root := RootDomains(cfg); !containsStr(root, "gitlab.example.com") {
		t.Fatalf("RootDomains missing self-hosted host: %v", root)
	}
}

func TestParseConfig_GitLabDotComNotDerived(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nsource-control:\n  gitlab:\n    project: g/app\n"))
	if got := SourceControlDomains(cfg); got != nil {
		t.Fatalf("gitlab.com must not be derived (it's in the sealed base): %v", got)
	}
}

func TestParseConfig_GitLabProjectNeedsTwoSegments(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nsource-control:\n  gitlab:\n    project: solo\n"))
	if err == nil || !strings.Contains(err.Error(), "≥2 segments") {
		t.Fatalf("want ≥2-segment error, got %v", err)
	}
}

func TestParseConfig_GitHubAndGitLabMutuallyExclusive(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nsource-control:\n  github:\n    project: o/r\n  gitlab:\n    project: g/app\n"))
	if err == nil || !strings.Contains(err.Error(), "exactly one host") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}
}

func TestRepo_GitHubUnchanged(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nsource-control:\n  github:\n    project: o/r\n"))
	repo, _ := cfg.SourceControl.Repo()
	if repo.Provider != "github" || repo.Host != "github.com" || repo.CloneURL() != "https://github.com/o/r.git" {
		t.Fatalf("github repo = %+v", repo)
	}
}
