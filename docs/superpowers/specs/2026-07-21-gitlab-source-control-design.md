# GitLab source-control provider — Design

**Date:** 2026-07-21
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove`
**Epic:** TBD (to be filed on the board)
**Builds on:** the `source-control` union and the `AT_TASK_GIT_TOKEN` demand/supply model; the host-agnostic `worker.ShellGit` and the `worker.CodeHost` interface; `at-task`'s `complete`/`clone-workspace` verbs; and the `kit.RootDomains`/`ProviderDomains` egress-derivation helper added for the Vertex model-provider.

## 1. Purpose

Today `source-control` has exactly one member, `github`, and GitHub is assumed throughout: the clone URL is a hardcoded `https://github.com/<project>.git`, `at-task`'s `complete` verb constructs a GitHub PR client unconditionally, and `cmd/at-cove` reaches into `cfg.SourceControl.GitHub` directly in ~6 places. An org whose repos live on **GitLab** (gitlab.com or a self-hosted instance) cannot use at-cove.

This design adds **GitLab as a second `source-control` provider** at **full parity** with GitHub: interactive `chat` clone, and the dispatched worker (`at-cove work`/`dispatch`) — clone + push + open **Merge Requests**. A configurable **host** (default `gitlab.com`) supports self-hosted GitLab from the start. It is source-control only — Linear stays the dispatch tracker (an orthogonal union).

**Requirements:**
- A kit can declare `source-control.gitlab` (host, project path, main-branch, secrets) instead of `github`; the two are mutually exclusive.
- Both the interactive clone and the full worker flow (clone → push → open MR) work against GitLab, gitlab.com or self-hosted.
- The `host` drives the clone URL, the GitLab API base, **and** egress; a self-hosted host is reachable without a hand-maintained allow-list.
- No regression to the GitHub path; the change is *make provider-neutral + add GitLab*, not a rewrite.
- Auth for v1 is a **supplied** token (PAT / Project Access Token); GitLab token *minting* (the GitHub-App analog) is a tracked follow-up.
- Never weaken the threat model: the sealed hardening layer and the egress lock are unchanged in kind; a self-hosted host only *widens* the additive kit-root tier.

## 2. Why GitLab diverges from GitHub (the constraints that shape this)

- **The git layer is already host-agnostic.** `worker.ShellGit` clones/pushes any HTTPS remote with an env-only askpass (`GIT_ASKPASS` + `AT_TASK_GIT_TOKEN`). GitLab reuses it **unchanged** — clone/push need no GitLab-specific code.
- **The code-host API differs.** GitHub opens a PR at `api.github.com/repos/<owner/name>/pulls`; GitLab opens a Merge Request at `https://<host>/api/v4/projects/<url-encoded path>/merge_requests`. The `worker.CodeHost` interface (`OpenPR(ctx, repo, base, head, title, body)`) is already generic enough; GitLab implements it as an MR.
- **Self-hosted is the common case.** Unlike GitHub's fixed `github.com`, GitLab is frequently self-hosted, so **host** is a first-class field — it changes the clone URL, the API base URL, and (crucially) egress: a self-hosted host is not in the sealed allow-list.
- **No minting primitive.** GitHub mints minute-scoped App installation tokens per run (`at-mint github`). GitLab has no equivalent; its tokens (PAT / Project / Group Access Tokens) are day-granular and created via a privileged parent token. v1 therefore uses a **supplied** token; minting is deferred (§8).
- **Nested groups.** A GitLab project path can be `group/subgroup/name` (≥2 segments), where a GitHub project is always `owner/name` (exactly 2). Validation and API URL-encoding must allow the extra segments.

## 3. Principles

- **Provider-neutral core, provider-specific edges.** Push the GitHub assumption out of the shared paths (clone URL, the `dispatch`/`work` repo, the token-demand check) behind a small accessor; keep only the API client and the config member provider-specific.
- **Reuse, don't fork.** `ShellGit`, the `CodeHost` interface, the `AT_TASK_GIT_TOKEN` demand/supply, the `RootDomains` egress derivation, and the `at-task` bracket are all reused; GitLab adds one API client, one config member, and a routing switch.
- **Host drives everything.** One `host` field feeds the clone URL, the API base, and the derived egress domain — no separate knobs to keep in sync.
- **Additive, sealed-wins egress.** `gitlab.com` joins the sealed base (like `github.com`); a self-hosted host only widens the kit-root tier. The sealed layer is untouched.

## 4. Config schema — the `gitlab` member

```yaml
source-control:
  gitlab:
    host: gitlab.com                 # optional; default gitlab.com. self-hosted → gitlab.example.com
    project: group/subgroup/name     # ≥2 path segments (nested groups); no leading/trailing slash
    main-branch: main                # optional; default main
    secrets:
      AT_TASK_GIT_TOKEN:
        description: GitLab PAT / Project Access Token (api + write_repository scope)
```

Parsed/validated in `internal/kit/config.go`:
- `SourceControl` gains `GitLab *GitLabSource`. `Active()` returns `"gitlab"`; `github` and `gitlab` are **mutually exclusive** (the existing "exactly one host" check extends to count both).
- `GitLabSource{ Host, Project, MainBranch string; Secrets map[string]SecretConfig }`.
- Validation mirrors GitHub's: `project` must have ≥2 non-empty `/`-separated segments (vs GitHub's exactly 2); `host` defaults to `gitlab.com`; `main-branch` defaults to `main`; `secrets` is restricted to the well-known `AT_TASK_GIT_TOKEN` (reuse `checkWellKnownSecrets` for `source-control.gitlab.secrets`).
- `GitTokenName()` already keys off the presence of `AT_TASK_GIT_TOKEN` under source-control; extend it to check the active provider's `Secrets`.

## 5. Provider-neutral accessor — de-GitHub the shared paths

Add to `kit`:

```go
// Repo is the resolved, provider-neutral repo identity a run command needs.
type Repo struct {
    Provider   string // "github" | "gitlab"
    Host       string // github.com | gitlab.com | self-hosted
    Project    string // owner/name or group/.../name
    MainBranch string
}
func (r Repo) CloneURL() string { return "https://" + r.Host + "/" + r.Project + ".git" }

// Repo returns the active provider's repo identity, or ok=false when no
// source-control is configured.
func (s *SourceControl) Repo() (Repo, bool)
```

Replace the ~6 direct `cfg.SourceControl.GitHub` reads in `cmd/at-cove/main.go` (the workspace-clone URL at ~L937, the `repo` used by `dispatch`/`work` at ~L835/1359, the token-demand messages) with `SourceControl.Repo()`. `CloneURL()` supersedes the hardcoded `https://github.com/...`. For GitHub, `Host` is `github.com`, so the URL and behavior are byte-for-byte unchanged.

## 6. GitLab code-host client + `at-task` routing

**New `internal/dispatch/gitlab/gitlab.go`** — a small net/http client mirroring `internal/dispatch/github/github.go`, satisfying `worker.CodeHost`:
- `New(token, host string, httpc *http.Client) *Client` — base URL `https://<host>/api/v4`.
- `OpenPR(ctx, repo, base, head, title, body)`:
  - `POST /projects/<url-encoded repo>/merge_requests` with `PRIVATE-TOKEN: <token>` and body `{source_branch: head, target_branch: base, title, description: body}`; on success return `web_url`.
  - On a duplicate ("merge request already exists" — GitLab returns 409/`open merge request already exists`), find the open MR via `GET /projects/<enc>/merge_requests?source_branch=<head>&target_branch=<base>&state=opened` and return its `web_url` — the same recover-existing shape as the GitHub client.
  - `repo` is the project path; URL-encode it (`group%2Fsub%2Fname`) for the API.

**Route the provider to `at-task`.** `TaskRepo` (in `internal/dispatch/worker/taskv2.go`) gains `Provider string` and `Host string`, populated by the orchestrator (`internal/dispatchrun` / `at-cove work`) from the resolved source-control config and carried in the task JSON. `at-task`'s `complete` verb (`cmd/at-task/main.go`) selects the client:

```go
var ch worker.CodeHost
switch task.Repo.Provider {
case "gitlab":
    ch = gitlab.New(os.Getenv("AT_TASK_GIT_TOKEN"), task.Repo.Host, nil)
default: // "github" (and empty, for legacy tasks)
    ch = github.New(os.Getenv("AT_TASK_GIT_TOKEN"), nil)
}
```

The token stays the env `AT_TASK_GIT_TOKEN` (works as the git askpass **and** the MR-API `PRIVATE-TOKEN`). `worker.Complete`/`CodeHost` are unchanged — `OpenPR` keeps its name (the interface abstracts PR/MR).

## 7. Egress

- **`gitlab.com` → sealed base.** Add `gitlab.com` to `internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt` (mirrors `.github.com`), so the common case needs no per-kit widening.
- **Self-hosted host → kit-root (auto-derived).** Add a `SourceControlDomains(c Config) []string` that returns the `gitlab.host` when it is set and not `gitlab.com`, and fold it into `RootDomains` (`RootDomains = unionDomains(image.AllowedDomains, ProviderDomains(cfg), SourceControlDomains(cfg))`). So a self-hosted GitLab kit reaches its host without hand-editing `image.allowed-domains`, exactly as the Vertex provider auto-derives its GCP hosts. Sealed base and `squid.conf` unchanged.

## 8. Auth — supplied token (v1) + minting follow-up

v1: the `AT_TASK_GIT_TOKEN` for a GitLab kit is **supplied** machine-side via the normal demand/supply (`secrets.yml` `command`/`value`/`global`) — a GitLab PAT or Project/Group Access Token with `api` + `write_repository`. No `at-mint` GitLab provider; the GitHub minter (`cmd/at-mint/github.go`, `mint.Expander`'s github branch) is untouched and the `repo`-scoping arg it takes is GitHub-only (GitLab doesn't use it).

**Follow-up (tracked as its own ticket):** an `at-mint gitlab` provider that creates a short-lived Project Access Token via a privileged parent token — the GitHub-App analog, bringing GitLab auth to per-run-minting parity. Filed on the board; out of scope here.

## 9. Scoped out of v1 (deliberate)

- **The sealed `/etc/gitconfig` stays GitHub-specific.** Its `insteadOf` ssh→https rewrite and its `[credential "https://github.com"]` helper serve the *agent's own* in-VM git with a token. Every core flow — worker clone/push (via `ShellGit` askpass), MR open (via the GitLab API), and the chat clone (via `at-task clone-workspace`, also `ShellGit`) — bypasses it, so GitLab parity does **not** require it. Covering a self-hosted GitLab host for agent-initiated git would need a **generated per-kit gitconfig fragment** (the sealed layer is static and cannot know the kit's host) — deferred; the agent never pushes directly.
- **`CodeHost.OpenPR` keeps its name** (abstracts PR/MR) rather than a rename churning GitHub + tests.
- **GitLab Issues as a tracker** — out of scope; Linear stays the tracker.

## 10. Code touchpoints

| Area | Change |
|---|---|
| `internal/kit/config.go` | `GitLabSource` struct; `SourceControl.GitLab`; `Active()`/validation (≥2 segments, host/branch defaults, mutual exclusion); `GitTokenName()` provider-aware; new `Repo` type + `SourceControl.Repo()`; `SourceControlDomains` + fold into `RootDomains` |
| `cmd/at-cove/main.go` | Replace ~6 direct `.GitHub` reads with `SourceControl.Repo()`; clone URL via `CloneURL()`; populate `TaskRepo.Provider`/`Host` |
| `internal/dispatch/gitlab/` | **new** — the MR client (mirrors `github/`), `worker.CodeHost` |
| `internal/dispatch/worker/taskv2.go` | `TaskRepo.Provider`, `TaskRepo.Host` |
| `cmd/at-task/main.go` | provider switch in `complete` (github vs gitlab client) |
| `internal/assemble/hardening/.../allowed_domains.txt` | add `gitlab.com` to the sealed base |
| `docs/` | `at-cove-config.md` (the `gitlab` member + host/egress), `OVERVIEW.md` (source-control + egress notes) |

## 11. Open questions (resolve during planning/impl)

- **`TaskRepo` schema version:** adding `Provider`/`Host` — confirm legacy tasks (no provider) default to github cleanly (the `default:` switch arm covers it; verify any task JSON schema/version bump).
- **GitLab "existing MR" signal:** confirm the exact status/message GitLab returns for a duplicate MR across gitlab.com and recent self-hosted versions, to key the recover-existing path (the design assumes 409 / "already exists").
- **Token scope guidance:** the minimal GitLab token scope for clone+push+MR (`write_repository` + `api`, or `api` alone) — document the recommended scope.
- **Self-hosted host validation:** whether to validate `host` shape (bare host, no scheme/path) and reject an accidental `https://…`.
