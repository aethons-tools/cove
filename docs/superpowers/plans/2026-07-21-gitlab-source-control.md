# GitLab source-control provider — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add GitLab as a second `source-control` provider at full parity with GitHub — interactive `chat` clone plus the dispatched worker's clone → push → open **Merge Request** — with a configurable host (default `gitlab.com`, self-hosted supported).

**Architecture:** Push the GitHub assumption out of the shared paths behind a provider-neutral `SourceControl.Repo()` accessor; add one GitLab MR API client (mirroring the GitHub one); route the provider to `at-task` via a new `TaskRepo.Provider` field (`Host` already exists). The host-agnostic `worker.ShellGit` is reused unchanged. Auth for v1 is a supplied token; GitLab token *minting* is deferred (Linear COV-79).

**Tech Stack:** Go (stdlib + `gopkg.in/yaml.v3`); hermetic tests (fake process runner / injected `http.Client` RoundTripper).

## Global Constraints

- **Design source:** `docs/superpowers/specs/2026-07-21-gitlab-source-control-design.md`.
- **No regression to the GitHub path.** A `github` kit must behave byte-for-byte as before (clone URL `https://github.com/<project>.git`, PR client, egress).
- **Reuse, don't fork:** `worker.ShellGit`, `worker.CodeHost` (`OpenPR` keeps its name — it abstracts PR/MR), the `AT_TASK_GIT_TOKEN` demand/supply, and the `RootDomains` egress derivation are reused.
- **Sealed hardening minimal touch:** the only sealed-layer edit is adding `gitlab.com` to `allowed_domains.txt`; do **not** change `/etc/gitconfig` or any other hardening file (the GitHub-specific gitconfig is out of scope — the core flows bypass it).
- **`gitlab.host` is a bare hostname** (`gitlab.com`, `gitlab.example.com`) — no scheme, no path. `TaskRepo.Host` is the URL prefix `https://<host>`.
- **Auth v1 = supplied token.** Do not add an `at-mint` gitlab provider (that's COV-79).
- **TDD, DRY, YAGNI.** Hermetic tests, pristine output. Full suite: `go test ./...` (or `just test`).
- **Commit trailers** (every commit):
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98
  ```

---

### Task 1: Config — the `gitlab` member, provider-neutral `Repo()`, and egress derivation

**Files:**
- Modify: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces:
  - `type GitLabSource struct { Host, Project, MainBranch string; Secrets map[string]SecretConfig }`
  - `SourceControl.GitLab *GitLabSource` (yaml `gitlab`)
  - `type Repo struct { Provider, Host, Project, MainBranch string }` + `func (Repo) CloneURL() string`
  - `func (s *SourceControl) Repo() (Repo, bool)`
  - `func SourceControlDomains(c Config) []string`
  - updated `SourceControl.Active()` (github|gitlab), `Config.GitTokenName()` (provider-aware), `RootDomains` (adds source-control domains)

- [ ] **Step 1: Write failing tests**

Add to `internal/kit/config_test.go`:

```go
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
```

Add this test helper to `config_test.go` if not already present (grep first — a `contains`/`containsStr` helper may exist):

```go
func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/kit/ -run 'GitLab|Repo_GitHub' -v`
Expected: FAIL (undefined `GitLabSource`, `Repo`, `SourceControlDomains`, etc.).

- [ ] **Step 3: Add the GitLab types**

In `internal/kit/config.go`, add the `GitLab` field to `SourceControl` (next to `GitHub`):

```go
type SourceControl struct {
	GitHub *GitHubSource `yaml:"github,omitempty"`
	GitLab *GitLabSource `yaml:"gitlab,omitempty"`
}
```

Add the `GitLabSource` type (next to `GitHubSource`):

```go
// GitLabSource names a GitLab repo — gitlab.com or a self-hosted host. The project
// path may be nested (group/subgroup/name, ≥2 segments), unlike GitHub's owner/name.
type GitLabSource struct {
	Host       string                  `yaml:"host,omitempty"`        // bare hostname; default gitlab.com
	Project    string                  `yaml:"project"`               // "group/.../name"
	MainBranch string                  `yaml:"main-branch,omitempty"` // default "main"
	Secrets    map[string]SecretConfig `yaml:"secrets,omitempty"`     // well-known: AT_TASK_GIT_TOKEN
}
```

- [ ] **Step 4: Provider-neutral accessor + updated Active/GitTokenName**

Replace `SourceControl.Active()` with:

```go
// Active returns the set host, or an error if not exactly one.
func (s *SourceControl) Active() (string, error) {
	n, name := 0, ""
	if s.GitHub != nil {
		n, name = n+1, "github"
	}
	if s.GitLab != nil {
		n, name = n+1, "gitlab"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github or gitlab)")
	}
	return name, nil
}

// Repo is the resolved, provider-neutral repo identity a run command needs.
type Repo struct {
	Provider   string // "github" | "gitlab"
	Host       string // github.com | gitlab.com | self-hosted host
	Project    string // owner/name or group/.../name
	MainBranch string
}

// CloneURL is the HTTPS clone URL for the repo.
func (r Repo) CloneURL() string { return "https://" + r.Host + "/" + r.Project + ".git" }

// Repo returns the active provider's repo identity, or ok=false when no
// source-control is configured. GitHub has no host field — it is always github.com.
func (s *SourceControl) Repo() (Repo, bool) {
	if s == nil {
		return Repo{}, false
	}
	switch {
	case s.GitHub != nil:
		return Repo{Provider: "github", Host: "github.com", Project: s.GitHub.Project, MainBranch: s.GitHub.MainBranch}, true
	case s.GitLab != nil:
		return Repo{Provider: "gitlab", Host: s.GitLab.Host, Project: s.GitLab.Project, MainBranch: s.GitLab.MainBranch}, true
	}
	return Repo{}, false
}
```

Replace `Config.GitTokenName()` with the provider-aware form:

```go
// GitTokenName reports the code-host token demand under the active provider's
// source-control.<provider>.secrets, if present.
func (c Config) GitTokenName() (string, bool) {
	if c.SourceControl == nil {
		return "", false
	}
	var secrets map[string]SecretConfig
	switch {
	case c.SourceControl.GitHub != nil:
		secrets = c.SourceControl.GitHub.Secrets
	case c.SourceControl.GitLab != nil:
		secrets = c.SourceControl.GitLab.Secrets
	default:
		return "", false
	}
	if _, ok := secrets["AT_TASK_GIT_TOKEN"]; !ok {
		return "", false
	}
	return "AT_TASK_GIT_TOKEN", true
}
```

- [ ] **Step 5: GitLab validation + egress derivation**

In `ParseConfig`, inside the existing `if cfg.SourceControl != nil { ... }` block, **after** the `if gh := cfg.SourceControl.GitHub; gh != nil { ... }` sub-block, add the GitLab sub-block:

```go
		if gl := cfg.SourceControl.GitLab; gl != nil {
			if strings.ContainsAny(gl.Host, "/:") {
				return Config{}, fmt.Errorf("config.yml: source-control.gitlab.host must be a bare hostname (no scheme or path), got %q", gl.Host)
			}
			segs := strings.Split(gl.Project, "/")
			if len(segs) < 2 {
				return Config{}, fmt.Errorf("config.yml: source-control.gitlab.project must be \"group/…/name\" (≥2 segments), got %q", gl.Project)
			}
			for _, s := range segs {
				if strings.TrimSpace(s) == "" {
					return Config{}, fmt.Errorf("config.yml: source-control.gitlab.project has an empty path segment: %q", gl.Project)
				}
			}
			if gl.Host == "" {
				gl.Host = "gitlab.com"
			}
			if gl.MainBranch == "" {
				gl.MainBranch = "main"
			}
			if len(gl.Secrets) > 0 {
				if err := checkWellKnownSecrets("source-control.gitlab.secrets", gl.Secrets, "AT_TASK_GIT_TOKEN"); err != nil {
					return Config{}, err
				}
			}
		}
```

Add the egress helper (near `ProviderDomains`/`RootDomains`):

```go
// SourceControlDomains returns the additive egress domain a self-hosted source
// control host needs. gitlab.com and github.com are in the sealed base, so only a
// non-default GitLab host is derived here.
func SourceControlDomains(c Config) []string {
	if c.SourceControl == nil || c.SourceControl.GitLab == nil {
		return nil
	}
	host := strings.TrimSpace(c.SourceControl.GitLab.Host)
	if host == "" || host == "gitlab.com" {
		return nil
	}
	return []string{host}
}
```

Update `RootDomains` to fold it in:

```go
func RootDomains(c Config) []string {
	return unionDomains(c.Image.AllowedDomains, ProviderDomains(c), SourceControlDomains(c))
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/kit/ -run 'GitLab|Repo_GitHub|ProviderDomains|Vertex' -v && go test ./internal/kit/`
Expected: PASS (new GitLab tests + existing github/vertex tests unchanged).

- [ ] **Step 7: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(config): add gitlab source-control member + provider-neutral Repo() accessor

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 2: Route the provider — `TaskRepo.Provider` + de-GitHub the shared call sites

**Files:**
- Modify: `internal/dispatch/worker/taskv2.go` (add `Provider`)
- Modify: `internal/dispatchrun/dispatchrun.go` (fill-repo block → `Repo()`)
- Modify: `cmd/at-cove/main.go` (the ~6 `.GitHub` reads + clone URL)
- Test: `internal/dispatchrun/dispatchrun_test.go`

**Interfaces:**
- Consumes: `kit.SourceControl.Repo()`, `Repo.CloneURL()` (Task 1).
- Produces: `TaskRepo.Provider string` (yaml/json `provider`), populated by `dispatchrun` from the resolved config.

- [ ] **Step 1: Add the `Provider` field**

In `internal/dispatch/worker/taskv2.go`, add to `TaskRepo` (above `Host`):

```go
type TaskRepo struct {
	Provider     string `json:"provider,omitempty" yaml:"provider,omitempty"` // "github" | "gitlab"; empty => github (legacy)
	Host         string `json:"host,omitempty" yaml:"host,omitempty"`
	Name         string `json:"name" yaml:"name"`
	SourceBranch string `json:"source-branch" yaml:"source-branch"`
	WorkBranch   string `json:"work-branch" yaml:"work-branch"`
}
```

- [ ] **Step 2: Write the failing dispatchrun test**

Add to `internal/dispatchrun/dispatchrun_test.go` (adapt to the file's existing Options/harness — grep for an existing test that builds `Options{Cfg: ...}` and mirror how it inspects the marshalled task; the assertion is what matters):

```go
func TestFillRepo_GitLabSetsProviderHost(t *testing.T) {
	cfg := kit.Config{
		Name: "k",
		SourceControl: &kit.SourceControl{
			GitLab: &kit.GitLabSource{Host: "gitlab.example.com", Project: "g/app", MainBranch: "main"},
		},
	}
	repo, ok := cfg.SourceControl.Repo()
	if !ok {
		t.Fatal("Repo() ok=false")
	}
	// The fill-repo logic under test (mirrored assertion): provider + https-prefixed host.
	if repo.Provider != "gitlab" || repo.Host != "gitlab.example.com" {
		t.Fatalf("repo=%+v", repo)
	}
	if want := "https://gitlab.example.com"; "https://"+repo.Host != want {
		t.Fatalf("task host prefix = %q", "https://"+repo.Host)
	}
}
```

> If `dispatchrun_test.go` already exercises the fill-repo path end-to-end against a `Fake` runner (grep for `Repo.Name`/`COVE_RUN_REPO`), extend that test instead to assert `task.Repo.Provider == "gitlab"` and `task.Repo.Host == "https://gitlab.example.com"` for a gitlab `Cfg`. Prefer extending the real path over the mirrored assertion above.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/dispatchrun/ -run 'FillRepo' -v`
Expected: FAIL (compile error until Task 1 types exist — they do — then the assertion drives the code change if you extended the real path).

- [ ] **Step 4: Rewrite the fill-repo block in dispatchrun**

In `internal/dispatchrun/dispatchrun.go`, replace the fill-repo block (the `if o.Cfg.SourceControl == nil || o.Cfg.SourceControl.GitHub == nil { ... }` through the `task.Repo.SourceBranch` default) with the provider-neutral form:

```go
	// Fill the repo from the kit's source-control — the single source of truth.
	repo, ok := o.Cfg.SourceControl.Repo()
	if !ok {
		return fmt.Errorf("kit %q declares no source-control (required for dispatch)", o.Cfg.Name)
	}
	task.Repo.Provider = repo.Provider
	task.Repo.Name = repo.Project
	task.Repo.Host = "https://" + repo.Host
	if task.Repo.SourceBranch == "" {
		task.Repo.SourceBranch = repo.MainBranch // defaulted to "main" at parse
	}
```

(`o.Cfg.SourceControl.Repo()` — `Repo()` is nil-safe on a nil `*SourceControl`, but the marshalling below still needs a valid repo, so the `!ok` guard preserves the "no source-control" error.)

In the `runEnv` map a few lines below, replace `o.Cfg.SourceControl.GitHub.Project` with `repo.Project`:

```go
		"COVE_RUN_REPO":    repo.Project,
```

- [ ] **Step 5: De-GitHub the `cmd/at-cove/main.go` call sites**

Grep first: `rg -n 'SourceControl\.GitHub' cmd/at-cove/main.go` (expect ~6). Replace each read with `SourceControl.Repo()`:

- **workspace clone plan** (~L935-937): replace
  ```go
  gh := cfg.SourceControl.GitHub
  ... RepoURL: "https://github.com/" + gh.Project + ".git",
  ```
  with
  ```go
  repo, _ := cfg.SourceControl.Repo() // presence already checked by the guard above
  ... RepoURL: repo.CloneURL(),
  ```
  (Keep the surrounding nil/`GitTokenName` guard — the clone is skipped when source-control/token is absent.)

- **the `repo` string for dispatch/work + dry-run message** (~L795-796, L834-835, L1358-1359): replace `cfg.SourceControl.GitHub.Project` with `r.Project` where `r, ok := cfg.SourceControl.Repo()` (guard `ok`). For the dry-run clone message (~L796) use `repo.Project`.

- **the token-demand error message** (~L1416): replace the `declares no source-control.github.secrets AT_TASK_GIT_TOKEN` text with a provider-neutral `declares no source-control.<provider>.secrets AT_TASK_GIT_TOKEN` (or use `cfg.GitTokenName()` which is already provider-aware to gate this).

For each site, keep the existing nil-guard shape; only the field access changes. Do **not** change control flow.

- [ ] **Step 6: Run to verify + build**

Run: `go build ./... && go test ./internal/dispatchrun/ ./internal/kit/ ./cmd/at-cove/ 2>&1 | tail -20`
Expected: clean build; new + existing tests pass. (A `github` kit still yields `Host: "https://github.com"`, `Provider: "github"`, same clone URL — no regression.)

- [ ] **Step 7: Commit**

```bash
git add internal/dispatch/worker/taskv2.go internal/dispatchrun/dispatchrun.go internal/dispatchrun/dispatchrun_test.go cmd/at-cove/main.go
git commit -m "feat(source-control): route provider via TaskRepo.Provider; neutralize the GitHub call sites

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 3: GitLab code-host client (Merge Requests)

**Files:**
- Create: `internal/dispatch/gitlab/gitlab.go`
- Test: `internal/dispatch/gitlab/gitlab_test.go`

**Interfaces:**
- Consumes: `worker.CodeHost` (the `OpenPR(ctx, repo, base, head, title, body) (string, error)` contract).
- Produces: `func New(token, host string, httpc *http.Client) *Client` implementing `worker.CodeHost`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dispatch/gitlab/gitlab_test.go` (mirrors the GitHub client's hermetic `rtFunc` style):

```go
package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestOpenMRCreates(t *testing.T) {
	var gotURL, gotTok string
	var body map[string]any
	c := New("tok", "gitlab.example.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotTok = r.Header.Get("PRIVATE-TOKEN")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		return resp(201, `{"web_url":"https://gitlab.example.com/g/app/-/merge_requests/5"}`), nil
	})})
	url, err := c.OpenPR(context.Background(), "g/sub/app", "main", "implement/AET-1", "AET-1: T", "the body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://gitlab.example.com/g/app/-/merge_requests/5" {
		t.Fatalf("url = %q", url)
	}
	if gotTok != "tok" {
		t.Fatalf("PRIVATE-TOKEN = %q", gotTok)
	}
	if !strings.Contains(gotURL, "https://gitlab.example.com/api/v4/projects/g%2Fsub%2Fapp/merge_requests") {
		t.Fatalf("request URL = %q", gotURL)
	}
	if body["source_branch"] != "implement/AET-1" || body["target_branch"] != "main" || body["title"] != "AET-1: T" || body["description"] != "the body" {
		t.Fatalf("body = %v", body)
	}
}

func TestOpenMRReturnsExistingOn409(t *testing.T) {
	calls := 0
	c := New("tok", "gitlab.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return resp(409, `{"message":["Another open merge request already exists for this source branch"]}`), nil
		}
		return resp(200, `[{"web_url":"https://gitlab.com/g/app/-/merge_requests/3"}]`), nil
	})})
	url, err := c.OpenPR(context.Background(), "g/app", "main", "implement/AET-1", "t", "b")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://gitlab.com/g/app/-/merge_requests/3" {
		t.Fatalf("url = %q", url)
	}
	if calls != 2 {
		t.Fatalf("expected create+lookup, got %d calls", calls)
	}
}

func TestOpenMRErrorSurfaces(t *testing.T) {
	c := New("tok", "gitlab.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(400, `{"message":"bad"}`), nil
	})})
	if _, err := c.OpenPR(context.Background(), "g/app", "main", "h", "t", "b"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want http 400 error, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dispatch/gitlab/ -v`
Expected: FAIL (package/`New`/`OpenPR` undefined).

- [ ] **Step 3: Implement the client**

Create `internal/dispatch/gitlab/gitlab.go`:

```go
// Package gitlab is at-task's GitLab CodeHost: a tiny Merge Request client over
// net/http, mirroring internal/dispatch/github. Live calls are exercised by the
// integration-tagged test (if added). It satisfies worker.CodeHost.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Client opens merge requests on a GitLab host (gitlab.com or self-hosted).
type Client struct {
	http *http.Client
	base string // https://<host>/api/v4
	tok  string
}

// New builds a client for host (a bare hostname, e.g. gitlab.com). token is a
// PAT / Project Access Token with api + write_repository.
func New(token, host string, httpc *http.Client) *Client {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{http: httpc, base: "https://" + host + "/api/v4", tok: token}
}

// OpenPR creates a Merge Request, or returns the URL of an existing open MR for
// the same source branch. repo is the project path (group/.../name).
func (c *Client) OpenPR(ctx context.Context, repo, base, head, title, body string) (string, error) {
	proj := url.QueryEscape(repo) // group/sub/name -> group%2Fsub%2Fname
	payload, _ := json.Marshal(map[string]string{
		"source_branch": head, "target_branch": base, "title": title, "description": body,
	})
	code, raw, err := c.do(ctx, http.MethodPost, c.base+"/projects/"+proj+"/merge_requests", payload)
	if err != nil {
		return "", err
	}
	if code == http.StatusCreated {
		return webURL(raw)
	}
	if code == http.StatusConflict { // an open MR already exists for this source branch
		return c.existing(ctx, proj, base, head)
	}
	return "", fmt.Errorf("gitlab: create MR: http %d: %s", code, strings.TrimSpace(string(raw)))
}

func (c *Client) existing(ctx context.Context, proj, base, head string) (string, error) {
	q := url.Values{"source_branch": {head}, "target_branch": {base}, "state": {"opened"}}
	code, raw, err := c.do(ctx, http.MethodGet, c.base+"/projects/"+proj+"/merge_requests?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("gitlab: list MRs: http %d", code)
	}
	var mrs []struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(raw, &mrs); err != nil {
		return "", err
	}
	if len(mrs) == 0 {
		return "", fmt.Errorf("gitlab: MR reported existing but none found for %s", head)
	}
	return mrs[0].WebURL, nil
}

func webURL(raw []byte) (string, error) {
	var out struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.WebURL, nil
}

func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	var r *bytes.Reader
	if payload != nil {
		r = bytes.NewReader(payload)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/dispatch/gitlab/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/gitlab/
git commit -m "feat(gitlab): add the GitLab Merge Request code-host client

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 4: Route the code-host in `at-task`'s `complete` verb

**Files:**
- Modify: `cmd/at-task/main.go`
- Test: `cmd/at-task/main_test.go`

**Interfaces:**
- Consumes: `TaskRepo.Provider`/`Host` (Task 2), `github.New`, `gitlab.New` (Task 3).
- Produces: `func codeHostFor(provider, token, host string) worker.CodeHost` (unit-testable selection helper).

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-task/main_test.go` (create the file if absent; package `main`):

```go
func TestCodeHostFor(t *testing.T) {
	gh := codeHostFor("github", "tok", "https://github.com")
	if _, ok := gh.(*github.Client); !ok {
		t.Fatalf("github provider -> %T, want *github.Client", gh)
	}
	empty := codeHostFor("", "tok", "https://github.com") // legacy task: default github
	if _, ok := empty.(*github.Client); !ok {
		t.Fatalf("empty provider -> %T, want *github.Client", empty)
	}
	gl := codeHostFor("gitlab", "tok", "https://gitlab.example.com")
	if _, ok := gl.(*gitlab.Client); !ok {
		t.Fatalf("gitlab provider -> %T, want *gitlab.Client", gl)
	}
}
```

Ensure the test imports `github.com/aethons-tools/cove/internal/dispatch/github` and `.../internal/dispatch/gitlab`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-task/ -run 'CodeHostFor' -v`
Expected: FAIL (`codeHostFor` undefined).

- [ ] **Step 3: Add the selection helper and use it in `complete`**

In `cmd/at-task/main.go`, add the helper and replace the hardcoded `ch := github.New(...)` in the `complete` verb. The GitLab client needs the bare host; `task.Repo.Host` is the `https://<host>` prefix, so strip the scheme:

```go
// codeHostFor selects the code-host client for the task's provider. host is the
// task.Repo.Host URL prefix (https://<host>); empty provider defaults to github
// (legacy tasks predating TaskRepo.Provider).
func codeHostFor(provider, token, host string) worker.CodeHost {
	if provider == "gitlab" {
		bare := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		bare = strings.TrimSuffix(bare, "/")
		if bare == "" {
			bare = "gitlab.com"
		}
		return gitlab.New(token, bare, nil)
	}
	return github.New(token, nil)
}
```

In the `complete` verb, replace:

```go
	ch := github.New(os.Getenv("AT_TASK_GIT_TOKEN"), nil)
```

with:

```go
	ch := codeHostFor(task.Repo.Provider, os.Getenv("AT_TASK_GIT_TOKEN"), task.Repo.Host)
```

Add the `gitlab` import (and `strings` if not already imported) to `cmd/at-task/main.go`.

- [ ] **Step 4: Run to verify pass + build**

Run: `go test ./cmd/at-task/ -run 'CodeHostFor' -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-task/main.go cmd/at-task/main_test.go
git commit -m "feat(at-task): select github vs gitlab code-host by TaskRepo.Provider

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 5: Egress — add `gitlab.com` to the sealed base allow-list

**Files:**
- Modify: `internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt`
- Test: `internal/assemble/assemble_test.go`

**Interfaces:** none (a sealed-layer data change; self-hosted hosts are already covered by Task 1's `SourceControlDomains`).

- [ ] **Step 1: Write the failing test**

Add to `internal/assemble/assemble_test.go` (uses the embedded hardening FS via `HardeningFS()`):

```go
func TestSealedBaseAllowsGitLab(t *testing.T) {
	b, err := fs.ReadFile(HardeningFS(), "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		t.Fatalf("read sealed allow-list: %v", err)
	}
	if !strings.Contains(string(b), "gitlab.com") {
		t.Fatalf("sealed base must allow gitlab.com:\n%s", b)
	}
}
```

> Verify the embed path first: grep the test file / `embed.go` for how the hardening FS is rooted (`HardeningFS()` may return an FS rooted at `hardening` or at the repo — adjust the `fs.ReadFile` path to match, e.g. drop the `hardening/` prefix if the FS is already rooted there). Use the path that the existing `HashTree(assemble.HardeningFS())` walks.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/assemble/ -run 'SealedBaseAllowsGitLab' -v`
Expected: FAIL (gitlab.com not present).

- [ ] **Step 3: Add gitlab.com to the sealed base**

In `internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt`, add after the GitHub entries:

```
# GitLab (gitlab.com) — git over HTTPS + the v4 API. Self-hosted hosts are added
# per-kit via source-control.gitlab.host (kit-root list), not here.
gitlab.com
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/assemble/ -run 'GitLab|Assemble' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt internal/assemble/assemble_test.go
git commit -m "feat(egress): allow gitlab.com in the sealed base list

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/usage/at-cove-config.md` (the `gitlab` member)
- Modify: `docs/OVERVIEW.md` (source-control + egress notes)

Uses the **docs-author** skill to route each change to the owning doc and keep `docs/INDEX.md`/cross-links valid; **docs-audit** before committing. No code.

- [ ] **Step 1: Author the docs**

Content to land (match each doc's progressive-disclosure style):
- **`docs/usage/at-cove-config.md`** — document `source-control.gitlab` alongside `github`: `host` (default gitlab.com; self-hosted supported), `project` (nested groups, ≥2 segments), `main-branch`, `secrets.AT_TASK_GIT_TOKEN` (a supplied PAT / Project Access Token with `api` + `write_repository`). Note the two are mutually exclusive; note a self-hosted `host` auto-widens the kit-root egress; note token *minting* is a follow-up (COV-79).
- **`docs/OVERVIEW.md`** — in the source-control mention, note GitHub **or** GitLab (repo identity, clone, PR/MR); in the egress section note `gitlab.com` is in the sealed base and a self-hosted GitLab host is auto-derived into the kit-root list (like Vertex).

- [ ] **Step 2: docs-audit + commit**

Run the docs-audit checker (clean vs. baseline), then:

```bash
git add docs/
git commit -m "docs: document the gitlab source-control provider

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BCsNHPh8Ps2aSSpxadqm98"
```

---

## Out of scope (tracked separately)

- **GitLab token minting** — `at-mint gitlab` (Project Access Token via a parent token). Filed as **Linear COV-79**.
- **The sealed `/etc/gitconfig`** (GitHub-specific ssh→https rewrite + credential helper). The core flows bypass it; covering a self-hosted GitLab host for agent-initiated git needs a generated per-kit gitconfig fragment — deferred (design §9).
- **GitLab Issues as a tracker** — Linear stays the tracker.

## Self-Review

- **Spec coverage:** §4 config `gitlab` member → Task 1; §5 provider-neutral `Repo()`/`CloneURL()` + call-site de-GitHub → Tasks 1–2; §6 GitLab MR client + `TaskRepo` routing + `at-task` switch → Tasks 2–4; §7 egress (sealed `gitlab.com` + self-hosted derivation) → Tasks 1 & 5; §8 supplied-token v1 (no minting) → honored (COV-79 out of scope); §9 scope-outs (gitconfig, `OpenPR` name) → respected; docs → Task 6. §11 open questions resolved: legacy task defaults to github (empty `Provider` → github arm, Tasks 2 & 4); duplicate-MR keyed on **409** (Task 3); token scope documented (Task 6); host shape validated (Task 1, `ContainsAny(host, "/:")`).
- **Placeholders:** none — every code/test step is complete. The two "grep first / adapt to the existing harness" notes (dispatchrun test shape, `HardeningFS()` embed path) are verification instructions, not placeholders — the assertions and file contents are given.
- **Type consistency:** `Repo`/`CloneURL`/`SourceControlDomains` (kit), `TaskRepo.Provider` (worker), `gitlab.New(token, host, httpc)` (Task 3) consumed with matching signatures in `codeHostFor` (Task 4) and `dispatchrun` (Task 2).
