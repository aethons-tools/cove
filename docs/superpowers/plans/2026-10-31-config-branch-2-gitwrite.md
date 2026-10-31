# Config branch editor, slice 2a: writing to the config repo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a Go package, `internal/jam/configrepo`, that can do these on the followed config repo with go-git only (Jam has no git binary):
- list its branches;
- read a branch's files;
- create a branch from the followed one;
- commit changes to a branch with a stale-head check;
- merge a branch into the followed one, refusing on a per-file conflict;
- open or find a GitHub pull request.

**Architecture:**
- **Storage.** A persistent bare clone (go-git filesystem storage) at `<state-dir>/config-repo/`. It is fetched in full (`refs/heads/*`) before every operation, under one mutex.
- **Commits.** Built with go-git's plumbing, without a work tree:
  1. Flatten the parent's tree into `path → (blob, mode)`.
  2. Apply the changes.
  3. Write blobs and nested trees bottom-up.
  4. Write the commit object.
  5. Push `<sha>:refs/heads/<branch>`. A non-fast-forward is `ErrStale`.
- **Merge.** A 3-way at the file level. The base is the branch's and the followed head's merge base.
  - Merge also handles a fast-forward and a no-op.
  - Before anything is pushed, a callback lets the caller plan the merged config.
  - After the push, the branch is moved to the merge commit too, so it is level.
- **Pull requests.** A small client for the GitHub REST API: `POST /repos/{o}/{r}/pulls` and `GET …/pulls?head=&base=&state=open`. The API base is `https://api.github.com` for github.com, else `https://<host>/api/v3`.

**Tech Stack:** Go, go-git v5 (plumbing/object, storage/filesystem, transport/http), net/http.

**Spec:** `docs/superpowers/specs/2026-10-31-config-branch-editor-design.md` (phase 2: the branch editor's git side).

## Global Constraints

- No git binary at run time. Tests may use `gittest`, which uses the git CLI and git-http-backend and skips without them.
- Never force-push, and never rewrite a branch someone else moved: a moved head is `ErrStale`.
- No credential or URL userinfo in any error. Wrap errors with `gitsource`'s redaction (`redactURL`, exported as `gitsource.Redact`).
- Branch names are validated with `plumbing.ReferenceName("refs/heads/"+name).Validate()` and must not be the followed branch for Create or Commit.
- The commit author is the operator, as `{Name, Email}`; the committer is `Jam <jam@localhost>`.
- Paths in `Changes` are slash paths relative to the repo root. The caller prefixes `dir`.

## Review Focus

1. **The followed branch moves between a read and a commit** (someone pushed). `Commit` returns `ErrStale`, and nothing is pushed (Task 3 test `stale head`).
2. **A commit with no actual change** (the export equals the branch). It makes no empty commit and returns the same head (Task 3 test `no-op`).
3. **Merge when the followed branch already contains the branch** (merged already). It's a no-op with the same followed head (Task 4 test `already merged`).
4. **A file deleted on one side and edited on the other.** That's a conflict (Task 4 test `delete vs edit`).
5. **A private repo whose credential is wrong.** The error is redacted and says auth failed (Task 2 test `bad auth`).

---

### Task 1: gittest can take pushes

**Files:** Modify `internal/jam/gitsource/gittest/server.go`; Test `internal/jam/gitsource/gittest/server_test.go` (create).

**Interfaces:**
- Produces:
  - `func WithPush() Option`, which sets `http.receivepack true` on the bare repo. With `WithAuth`, http-backend sees `REMOTE_USER`.
  - `func Show(t testing.TB, url, ref, path string) (string, bool)`, which runs `git show <ref>:<path>` in the bare repo the url names.
  - `func Head(t testing.TB, url, branch string) string`.
  - Serve keeps a map from URL to bare path for these.

- [ ] Test: Serve with WithPush; push a commit with the git CLI from a clone; `Head` and `Show` see it. Run (fail), implement, pass. Commit `test(gittest): pushes, Show and Head`.

### Task 2: Open, fetch, list branches, read a branch

**Files:** Create `internal/jam/configrepo/repo.go`, `repo_test.go`. Modify `internal/jam/gitsource/creds.go` to export `func (c Creds) AuthFor(repo string) (transport.AuthMethod, error)` (nil, nil when there is no cred) and `func Redact(s string) string`.

**Interfaces:**
- Produces:
  ```go
  type Repo struct { URL, Branch string; Creds gitsource.Creds; Path string /* bare storage dir */ }
  type BranchInfo struct { Name, Head string }
  type File struct { Data []byte; Mode filemode.FileMode } // Regular or Executable
  var ErrStale, ErrNoBranch, ErrBranchExists error
  func (r *Repo) Branches(ctx) ([]BranchInfo, error)                      // sorted by name; ls-remote heads
  func (r *Repo) Files(ctx, branch string) (head string, files map[string]File, err error) // whole tree; ErrNoBranch
  ```
- `fetch(ctx)` inits the bare storage on first use (`git.PlainInit(Path, true)` with the remote "origin"), then fetches `+refs/heads/*:refs/remotes/origin/*` (`NoErrAlreadyUpToDate` is fine).

- [ ] Tests (gittest):
  - Branches lists main.
  - Files returns the committed files with their modes (WithExec).
  - An unknown branch is ErrNoBranch.
  - **bad auth**: WithAuth plus a wrong cred gives an error with no token in it.
  - A second Repo on the same Path reuses the storage.
- [ ] Fail, implement, pass. Commit `feat(configrepo): read a config repo's branches`.

### Task 3: Create a branch; commit to it

**Files:** `internal/jam/configrepo/write.go`, `write_test.go`.

**Interfaces:**
- Produces:
  ```go
  type Signature struct { Name, Email string }
  type Changes struct { Put map[string]File; Delete []string }
  func (r *Repo) CreateBranch(ctx, name string) (head string, err error)   // at the followed head; ErrBranchExists; refuses the followed branch and an invalid name
  func (r *Repo) Commit(ctx, branch, wantHead string, ch Changes, author Signature, msg string) (head string, err error)
  ```
- Commit:
  - The remote head must equal `wantHead`, else `ErrStale`.
  - Applying `ch` to the flattened tree; a result identical to the parent returns `wantHead` with no commit.
  - Deleting a path that doesn't exist is ignored.
  - A non-fast-forward push is ErrStale.
  - The followed branch is refused (errors.New "the followed branch is read-only").

- [ ] Tests:
  - Create, then gittest.Head matches main.
  - Create twice gives ErrBranchExists.
  - Creating `main` is refused.
  - Creating `bad..name` is refused.
  - Commit puts a nested file (`projects/a/roles/r.yml`), and gittest.Show sees it; the author is right (`git log -1 --format=%an`).
  - Commit deletes a file.
  - Commit with an executable mode.
  - **no-op**: Commit of identical content returns wantHead, and Head is unchanged.
  - **stale head**: Commit with an old wantHead is ErrStale.
  - Commit to main is refused.
- [ ] Fail, implement, pass. Commit `feat(configrepo): create branches and commit to them`.

### Task 4: Merge

**Files:** `internal/jam/configrepo/merge.go`, `merge_test.go`.

**Interfaces:**
- Produces:
  ```go
  type ConflictError struct{ Paths []string }       // sorted; Error() "merge conflict in: a, b"
  type MergeResult struct{ Head string; FastForward, NoOp bool }
  func (r *Repo) Merge(ctx, branch, wantHead string, author Signature, msg string, check func(files map[string]File) error) (MergeResult, error)
  ```
- Merge:
  - The branch head must equal wantHead (ErrStale).
  - base = MergeBase(branch head, followed head).
  - If the branch head is an ancestor of followed → NoOp (followed head).
  - If followed is an ancestor of the branch → fast-forward: check(branch files), then push branch head to followed.
  - Else a file-level 3-way over all paths:
    - same on both sides → keep;
    - only one side changed relative to base → take it;
    - both changed differently, including delete vs edit → conflict.
    - Conflicts give `*ConflictError` and push nothing.
    - Otherwise check(merged files); its error is returned as is, with nothing pushed.
    - Then a merge commit with parents [followed head, branch head]; push it to followed (non-ff → ErrStale) and to the branch.

- [ ] Tests:
  - A fast-forward.
  - Both sides changed different files → a merge commit with two parents (`git log -1 --format=%p`), and the branch is level with main.
  - The same file changed differently → ConflictError{[path]}.
  - **delete vs edit** → conflict.
  - The same change on both sides → no conflict.
  - **already merged** → NoOp.
  - A check error pushes nothing (main is unchanged).
  - A stale wantHead → ErrStale.
- [ ] Fail, implement, pass. Commit `feat(configrepo): merge a branch into the followed one`.

### Task 5: GitHub pull requests

**Files:** `internal/jam/configrepo/github.go`, `github_test.go`.

**Interfaces:**
- Produces:
  ```go
  type GitHub struct { URL string; Token func() (string, error); Client *http.Client; APIBase string /* "" = derived */ }
  type PullRequest struct { Number int; URL string }   // URL = html_url
  func (g GitHub) OpenPR(ctx, head, base, title, body string) (PullRequest, error) // 422 "already exists" → FindPR's
  func (g GitHub) FindPR(ctx, head, base string) (PullRequest, bool, error)
  func apiBase(repoURL string) (base, owner, repo string, err error)            // github.com → api.github.com; else https://host/api/v3; trims .git
  ```
- Requests send `Authorization: Bearer <token>`, `Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2022-11-28`. Errors carry the status and GitHub's `message`, never the token.

- [ ] Tests (httptest):
  - apiBase table (github.com, a GHE host, `.git`, a bad URL).
  - OpenPR posts `{head, base, title, body}` and returns html_url.
  - A 422 already-exists falls back to FindPR.
  - FindPR with none gives ok=false.
  - A 401 error has no token in it.
- [ ] Fail, implement, pass. Commit `feat(configrepo): GitHub pull requests`.
