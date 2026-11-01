# Always a repo, slice 1: `config.git` and the one reader — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Jam can follow, and edit on branches, a config repo that is a local git repo or an SSH remote (`config.git`). The followed branch is read through Jam's bare clone for every kind.

**Architecture:**
- `configrepo.Repo` gains pluggable auth and a `file` transport served by go-git's built-in server, which runs inside the process.
- It refuses to move a branch that a local repo has checked out.
- cmd/jam reads `config.git`, writes the pinned host keys, and builds the Repo's auth.
- A `jam.FollowedFetcher` sends the followed source to the clone and every other source to the kit fetcher.

**Tech Stack:** Go, go-git v5.19.3 (`plumbing/transport/{server,client,ssh}`), `golang.org/x/crypto/ssh` + `ssh/knownhosts` (already in go.mod).

**Spec:** `docs/superpowers/specs/2026-10-31-always-a-repo-design.md` §"`config.git`" and §"One reader for the followed branch".

## Global Constraints

- Exactly one of `config.github`, `config.gitlab` and `config.git` is set. The error reads `config: set one of config.github, config.gitlab and config.git`.
- `config.git.url` accepts an absolute local path, `file:///abs/path`, `ssh://[user@]host[:port]/path`, or the scp form `user@host:path`. Anything else is refused with `config.git.url: want a local path, file://, ssh:// or user@host:path (for https use config.github or config.gitlab)`.
- `config.git.branch` is required. The error is `config.git.branch is required`.
- **SSH:** `cred` and `host-keys` are both required. The errors are `config.git.cred is required for ssh` and `config.git.host-keys is required for ssh`.
- **Local:** `cred` and `host-keys` must not be set. The error is `config.git.<field> is only for ssh`.
- A secret (the private key) never appears in argv, logs, the store, error text, or on disk. Host keys are public and may be written to `<state-dir>/config-repo-known_hosts`.
- No `git` binary at runtime. Tests may use the git CLI, and skip when it is missing, as `gittest` does.
- Jam never touches a local repo's working tree, index or `HEAD`.

## Review Focus

- A local repo with a branch checked out in a linked worktree (`git worktree add`): a save to that branch must be refused like `HEAD`'s. That's a test in Task 1.
- A local URL with a trailing slash or a `.git` suffix, such as `/srv/x/` or `/srv/x/.git`: it must open the same repo. That's a test in Task 1.
- An SSH host key that changed: the error carries the offered key as a `known_hosts` line, and never the private key. That's a test in Task 2.
- A `config.git` URL in the scp form (`git@host:x.git`) shown on Tracking and in logs: the label must show it, not `invalid`. That's a test in Task 4.
- A kit `.source.yml` in the repo naming a local path or SSH URL: it must still be refused by gitsource. That's a test in Task 4, covering the wrapper's fall-through.

---

### Task 1: The local `file` transport and the checked-out guard

**Files:**
- Create: `internal/jam/configrepo/local.go`
- Modify: `internal/jam/configrepo/repo.go`: `open` normalizes local URLs; `ErrCheckedOut`.
- Modify: `internal/jam/configrepo/write.go`: `push` calls the guard.
- Test: `internal/jam/configrepo/local_test.go`

**Interfaces:**
- Produces:
  - `var ErrCheckedOut = errors.New("the branch is checked out in the config repo")`. It is wrapped with the path and branch, matching via `errors.Is`.
  - `func IsLocal(url string) bool`: an absolute path or `file://`.
  - `func LocalPath(url string) string`: the cleaned absolute path, minus `file://`, a trailing `/` and a trailing `/.git`.
  - In `open`, a local `r.URL` is used as the remote URL `"file://" + LocalPath(r.URL)`.
  - `installLocal()`: guarded by `sync.Once`. It calls `client.InstallProtocol("file", server.NewClient(localLoader{}))`, where `localLoader.Load` runs `git.PlainOpenWithOptions(ep.Path, &git.PlainOpenOptions{DetectDotGit: false, EnableDotGitCommonDir: true})` and returns its `Storer`, or `transport.ErrRepositoryNotFound`. `open` calls it.
  - `func checkedOut(path string) (map[string]string, error)`: branch name → worktree dir, for the main worktree's `HEAD` and every `.git/worktrees/*/HEAD`. Read the `HEAD` files as text (`ref: refs/heads/x`), so no worktree logic is needed. A detached `HEAD` contributes nothing.
  - In `push`, for a local repo: if `branch` is in `checkedOut`, return `fmt.Errorf("%w: %s is checked out in %s; check out another branch, or merge it yourself", ErrCheckedOut, branch, dir)`.
- Consumes: `Repo.open`/`push` as they are.

- [ ] **Step 1: Write the failing tests.** Add a helper `localRepo(t, files map[string]string) (dir string)`: `git.PlainInit(dir, false)`, write the files, then `wt.Add`, then `wt.Commit`. Then `git` CLI `branch -M main`, or set `HEAD` to `refs/heads/main` with go-git before the commit.
  - `TestLocalBranchesFilesAndCreate`: `Repo{URL: dir, Branch: "main"}`.
    - `Branches` lists `main`, and `Files("main")` has the files.
    - `CreateBranch("jam/me")`, then the local repo has `refs/heads/jam/me` at main's head.
    - The checkout's `HEAD` file is byte-identical to before, a worktree file is unchanged, and `wt.Status()` is clean.
  - `TestLocalURLForms`: `dir+"/"`, `dir+"/.git"` and `"file://"+dir` all list the same branches.
  - `TestLocalCommitToCheckedOutRefused`:
    - Check out `jam/me` in the local repo (`wt.Checkout(&git.CheckoutOptions{Branch: "refs/heads/jam/me"})`).
    - `Commit(ctx, "jam/me", head, Changes{Put: {"a": …}}, …)` gives an error with `errors.Is(err, ErrCheckedOut)`, and the message contains `jam/me is checked out in`.
    - The local `refs/heads/jam/me` is unchanged.
  - `TestLocalMergeIntoCheckedOutRefused`: `main` is checked out, and `Merge` of `jam/me` with a new commit gives `ErrCheckedOut`.
  - `TestLocalLinkedWorktreeRefused`:
    - Skip without the git CLI (`exec.LookPath("git")`).
    - `git -C dir worktree add ../wt2 -b jam/other`; a `Commit` to `jam/other` gives `ErrCheckedOut`.
  - `TestLocalCommitAndMerge`: with `main` checked out, a `Commit` to `jam/me` succeeds and the local ref moves.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam/configrepo -run Local -v`
  Expected: compile errors (`ErrCheckedOut` undefined).

- [ ] **Step 3: Implement `local.go`, the `open` change and the `push` guard** as the Interfaces block says. Redaction: `explain` already runs on errors from `open`.

- [ ] **Step 4: Run the tests and see them pass.**
  Run: `go test ./internal/jam/configrepo/... -v -run 'Local|Branches|Merge|Commit'`
  Expected: PASS, and the existing tests still pass.

- [ ] **Step 5: Commit**
  `git add internal/jam/configrepo && git commit -m "configrepo: local repos via go-git's in-process server; refuse moving a checked-out branch"`

### Task 2: SSH auth with pinned host keys, and a test SSH server

**Files:**
- Create: `internal/jam/configrepo/ssh.go`
- Modify: `internal/jam/configrepo/repo.go`: `Repo.Auth`
- Create: `internal/jam/gitsource/gittest/ssh.go`: an SSH git server for tests.
- Test: `internal/jam/configrepo/ssh_test.go`

**Interfaces:**
- Produces:
  - `Repo.Auth func() (transport.AuthMethod, error)`. When it is non-nil, `open` uses it instead of `r.Creds.AuthFor(r.URL)`.
  - `func SSHAuth(user string, key func() (string, error), knownHostsFile string) func() (transport.AuthMethod, error)`:
    - It builds `gogitssh.NewPublicKeys(user, []byte(pem), "")` with a `HostKeyCallback` from `knownhosts.New(knownHostsFile)`, wrapped so that a `*knownhosts.KeyError` becomes `fmt.Errorf("config.git: the host key of %s is not in host-keys; if you trust it, add: %s", host, knownhosts.Line([]string{knownhosts.Normalize(host)}, key))`.
    - A key that fails to parse gives `config.git.cred: not an OpenSSH or PEM private key`. Never quote the key.
  - `func SSHUser(url string) string`: the URL's user; `git` when it has none.
  - `gittest.ServeSSH(t, files map[string]string) (url, knownHostsLine string, clientKeyPEM string)`:
    - An `x/crypto/ssh` server on 127.0.0.1 with a fresh ed25519 host key and client key.
    - On an `exec` of `git-upload-pack '<path>'` or `git-receive-pack '<path>'`, it runs the git CLI's `upload-pack`/`receive-pack` on a bare repo (made like `Serve` does), wiring the channel to stdin/stdout.
    - It accepts only the client key. The URL form is `ssh://git@127.0.0.1:<port>/repo.git`.
    - It skips without the git CLI.
- Consumes: Task 1's `open`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSSHBranchesAndCreate`:
    - `ServeSSH`, then write `knownHostsLine` to a temp file.
    - `Repo{URL: url, Branch: "main", Auth: SSHAuth("git", func() (string, error) { return pem, nil }, khFile)}`.
    - `Branches` lists `main`, `CreateBranch("jam/me")` succeeds, and `gittest.Head`-style inspection of the bare repo shows `jam/me`. Expose `gittest.SSHHead(t, url, branch)`.
  - `TestSSHUnknownHostKey`:
    - The known-hosts file holds another key's line.
    - The error contains `is not in host-keys` and the server's `ssh-ed25519 ` line, and does not contain any line of the PEM.
  - `TestSSHBadKey`: the key is `"nope"`, and the error is exactly `config.git.cred: not an OpenSSH or PEM private key`.
  - `TestSSHUser`: `ssh://bob@h/x` → `bob`, `git@h:x.git` → `git`, `ssh://h/x` → `git`.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam/configrepo -run SSH -v`
  Expected: compile errors.

- [ ] **Step 3: Implement `ssh.go`, `Repo.Auth` and `gittest.ServeSSH`.**

- [ ] **Step 4: Run the tests and see them pass.**
  Run: `go test ./internal/jam/configrepo/... ./internal/jam/gitsource/... -v -run SSH`
  Expected: PASS.

- [ ] **Step 5: Commit**
  `git commit -am "configrepo: ssh remotes with pinned host keys"` (add the new files first).

### Task 3: `config.git` in the serve config, wired

**Files:**
- Modify: `cmd/jam/config.go`:
  - `configRepoConfig.Git *configRepoHost` (`yaml:"git"`);
  - `configRepoHost.HostKeys string` (`yaml:"host-keys"`);
  - `configRepo()` for three kinds, plus `kind == "git"`;
  - `validateConfigRepo` with the Global Constraints' exact messages. For `git`, skip `gitsource.Parse`, and keep the gcp and pool cred check.
- Modify: `cmd/jam/main.go`:
  - `resolveKitFetcher` adds no gitsource cred for kind `git`.
  - The branches block, about :2996:
    - For `git`, set `repo.Auth`. For ssh it is `configrepo.SSHAuth(configrepo.SSHUser(url), resolve, khFile)`, after writing `HostKeys` to `filepath.Join(dir, "config-repo-known_hosts")` with mode 0644. For local it returns `nil, nil`.
    - Set `prs = nil` for `git`.
  - Build the Repo once, before the follower, so Task 4 can share it.
- Modify: `internal/jam/adminui/branches.go`: `repoLinkOf` returns `""` for a URL that isn't http(s), and `TreeLink` is `""` then. `BranchBar` already hides empty links, and `CanPR` is false when `PRs == nil`.
- Test: `cmd/jam/config_test.go`, next to the existing `config.github`/`gitlab` validation tests; also `internal/jam/adminui/branches_test.go`.

**Interfaces:**
- Produces: `serveConfig.configRepo().kind ∈ {"github","gitlab","git"}`, and `followedSource()` returns `gitsource.Source{Repo: g.URL, Dir: g.Dir, Ref: g.Branch}` for git. The default branch never applies to git, since it is required.
- Consumes: Tasks 1 and 2.

- [ ] **Step 1: Write the failing tests.**
  - A table in `TestValidateConfigRepo`, one row per Global Constraints message:
    - both `github` and `git` set;
    - an https URL in git;
    - a relative path;
    - a missing branch;
    - ssh with no cred, and ssh with no host-keys;
    - local with a cred;
    - valid: local `/srv/x`, `file:///srv/x`, `ssh://git@h/x.git` and `git@h:x.git`, each with what it needs.
  - In `branches_test.go`: a `ConfigBranches` with `PRs: nil` and a local URL gives `canPR: false` and `treeLink: ""`.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./cmd/jam -run ValidateConfigRepo -v; go test ./internal/jam/adminui -run Branches -v`
  Expected: FAIL.

- [ ] **Step 3: Implement it** as the Files block says.

- [ ] **Step 4: Run them and see them pass.** Same commands; expected PASS.

- [ ] **Step 5: Commit**
  `git commit -am "jam serve: config.git (a local or ssh config repo)"`

### Task 4: One reader for the followed branch

**Files:**
- Create: `internal/jam/config_followed_fetcher.go`
- Modify:
  - `internal/jam/config_plan.go`: `ConfigEnv.FollowedLabel string`.
  - `internal/jam/config_source.go`: `configSourceLabel` becomes a method, or takes the env, so the followed source shows `FollowedLabel`. Update its callers; grep `configSourceLabel(`.
  - `internal/jam/adminui/config_page.go:83`: don't mark the followed label `invalid`.
- Modify: `cmd/jam/main.go`:
  - Build `fetch := &jam.FollowedFetcher{Repo: repo, Followed: cfgEnv.Followed, Rest: kitFetcher}`.
  - Use it for the follower, `ConfigBranches.Fetcher`, and the config admin routes (`WithConfig`/`WithKitFetcher`, wherever the followed repo is fetched for sync and diff).
  - Set `cfgEnv.FollowedLabel`. For git it's `url` as written, plus `//dir` and `@branch`, the same shape as `Source.String()`. github and gitlab keep `Source.String()`.
- Test: `internal/jam/config_followed_fetcher_test.go`, and `internal/jam/config_follow_test.go` (one new case).

**Interfaces:**
- Produces: `type FollowedFetcher struct { Repo *configrepo.Repo; Followed gitsource.Source; Rest KitFetcher }`, implementing `KitFetcher`:
  - `Resolve(ctx, src)`: for `src == Followed`, the head of `src.Ref` from `Repo.Branches`. `src.Ref` is the followed branch, so `Repo.Files` would also do but costs more. Otherwise `Rest.Resolve`.
  - `Fetch(ctx, src, fn)`: for `src == Followed`, `Repo.Files(ctx, src.Ref)`, then write the files into a `os.MkdirTemp` dir with their modes, the way `ConfigBranches.materialize` does (reuse it; move it to a package func `materializeFiles(dir string, files map[string]configrepo.File) error` if needed). Then `fn(dir, head)`, and remove the dir. Otherwise `Rest.Fetch`. A nil `Rest` returns the existing "git sources are not available" error.
- Consumes: Task 3's single Repo.

- [ ] **Step 1: Write the failing tests.**
  - `TestFollowedFetcherRoutes`:
    - A local repo (Task 1's helper, exported for this package's tests as `configrepo` test helpers can't be imported; build the repo inline with go-git) with `config/users/bob.yml`.
    - `Followed = {Repo: dir, Dir: "config", Ref: "main"}`.
    - `Resolve` equals main's head; `Fetch` sees `root/config/users/bob.yml` and the right commit.
    - Another source goes to a fake `Rest` that records the call.
  - `TestFollowerAppliesLocalRepo`, in `config_follow_test.go`:
    - A ConfigFollower over a `FollowedFetcher` on a local repo, with a confirmed record.
    - A new commit to the local repo's main, then `Tick` gives `in-sync` with `Applied ==` the new head, and the user exists in the store.
  - `TestFollowedLabel`: with `Followed.Repo = "git@h:x.git"` and `FollowedLabel = "git@h:x.git@main"`, `Status().Source` is the label, not `invalid`.
  - `TestKitSourceStillRefusesLocal`: a tree whose `kits/k.source.yml` names `/etc//x@main`, applied through `FollowedFetcher`, is refused at parse (`config_files.go:914` path); the error mentions the source, not a fetch.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam -run 'FollowedFetcher|FollowerAppliesLocal|FollowedLabel|KitSourceStillRefusesLocal' -v`
  Expected: compile errors.

- [ ] **Step 3: Implement `FollowedFetcher`, the label, and the wiring.**

- [ ] **Step 4: Run them, and the config packages, and see them pass.**
  Run: `go test ./internal/jam/... ./cmd/jam/... 2>&1 | tail -30`
  Expected: `ok` for every package.

- [ ] **Step 5: Commit**
  `git commit -am "config: read the followed branch through the clone (all kinds)"`
