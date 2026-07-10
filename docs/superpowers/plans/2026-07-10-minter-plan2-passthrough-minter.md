# `COVE_RUN_*` passthrough + per-task token minter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the run's parameters (`COVE_RUN_*`) to secret resolver commands during dispatch, and mint the code-host token **fresh before each `at-work` git step** — turning the kit's `AT_WORK_GIT_TOKEN` resolver into a per-task, single-repo, short-lived minter, with no code-host logic in at-cove.

**Architecture:** Three green tasks. (1) Generic plumbing: `runner.OutputEnv` + `secret.Resolve(extraEnv, …)` — resolvers can receive extra env; all callers pass it (`connect` → nil). (2) `dispatchrun` derives `COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` from the task + kit `origin`, resolves the **base** (non-token) secrets once up front, and **mints the token fresh before `prepare` and again before `complete`** (the agent step holds no token). (3) The reference `mint-github-token.sh` (a host-side GitHub App minter) + kit wiring + docs (minter shipped).

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**. POSIX `sh` + `openssl`/`curl`/`jq` (host tools) for the reference minter.

**Scope note:** Plan 2 of 2 for [AET-24](https://linear.app/aethons-tools/issue/AET-24), on branch `feat/minter-passthrough` (builds on Plan 1's `origin`/repo-single-sourcing). Design: [`2026-07-10-minter-run-param-passthrough-design.md`](../specs/2026-07-10-minter-run-param-passthrough-design.md) §5.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies** (`go.mod` stays `gopkg.in/yaml.v3` only; `encoding/json` is stdlib).
- **The AET-29 air-gap holds, strengthened:** `AT_WORK_GIT_TOKEN` is present only in the `prepare`/`complete` envs, absent from the agent's env — and now minted **fresh per git step** (the agent step never holds any token). Secret values flow via the tmpfs env-file stdin only — never on argv/logs.
- **Fail-closed:** base (non-token) secrets are resolved before the VM is built; a base-secret failure aborts before `BuildImage`. (Token mints happen just before their git steps — a minter misconfig surfaces after the build, torn down on error; accepted per spec §8.)
- **at-cove stays code-host-agnostic:** the minter is a kit resolver *command* (a reference script), not Go code. The GitHub App is operator-provisioned.
- **Every commit builds + `go test ./...` green.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.

---

## Task 1: `runner.OutputEnv` + `secret.Resolve(extraEnv, …)`

**Files:** `internal/runner/runner.go`, `internal/runner/runner_test.go` (or wherever runner tests live), `internal/secret/secret.go`, `internal/secret/secret_test.go`, `internal/connect/connect.go`, `internal/dispatchrun/dispatchrun.go`

**Interfaces:** Produces `runner.Runner.OutputEnv(extraEnv []string, name string, args ...string) (string, error)`; `secret.Resolve(r runner.Runner, extraEnv map[string]string, specs []Spec) (map[string]string, error)`.

- [ ] **Step 1: Add `OutputEnv` to the runner (interface + OS + Fake)**

In `internal/runner/runner.go`, add to the `Runner` interface (next to `Output`):
```go
	// OutputEnv is Output with extra "KEY=VALUE" entries appended to the child env.
	OutputEnv(extraEnv []string, name string, args ...string) (string, error)
```
Implement on `OS` (mirror the existing `Output`, but set `cmd.Env = append(os.Environ(), extraEnv...)` — match `Output`'s stdout capture + `*ExitError` translation). Implement on `Fake` (mirror `Output`, recording the env):
```go
func (f *Fake) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...), Env: append([]string(nil), extraEnv...)})
	if f.out < len(f.Outputs) {
		r := f.Outputs[f.out]
		f.out++
		return r.Stdout, r.Err
	}
	return "", f.Err
}
```
Add a runner test that `OutputEnv` records the extra env on the `Fake` call and returns the queued stdout (mirror the existing `Output`/`RunEnv` tests).

- [ ] **Step 2: `secret.Resolve` gains `extraEnv` (fail first)**

In `internal/secret/secret_test.go`, update the existing `Resolve(f, specs)` calls to `Resolve(f, nil, specs)`, and add a test that the extra env reaches the command:
```go
func TestResolvePassesExtraEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}}}
	_, err := Resolve(f, map[string]string{"COVE_RUN_REPO": "acme/x"}, []Spec{{Name: "T", Command: []string{"mint"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || !contains(f.Calls[0].Env, "COVE_RUN_REPO=acme/x") {
		t.Fatalf("resolver env = %v; want COVE_RUN_REPO", f.Calls[0].Env)
	}
}
```
(add a small `contains` helper if absent). Run `go test ./internal/secret/` → FAIL (signature mismatch).

- [ ] **Step 3: Implement `Resolve(extraEnv, …)`**

In `internal/secret/secret.go`, change the signature and route non-literal specs through `OutputEnv`:
```go
func Resolve(r runner.Runner, extraEnv map[string]string, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	extra := flattenEnv(extraEnv)
	for _, s := range specs {
		if s.Literal {
			env[s.Name] = s.Value
			continue
		}
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("secret %q: empty command", s.Name)
		}
		out, err := r.OutputEnv(extra, s.Command[0], s.Command[1:]...)
		if err != nil {
			return nil, fmt.Errorf("secret %q: resolver command failed: %w", s.Name, err)
		}
		env[s.Name] = strings.TrimSuffix(out, "\n")
	}
	return env, nil
}

// flattenEnv turns a map into sorted "KEY=VALUE" entries (deterministic).
func flattenEnv(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k + "=" + m[k]
	}
	return out
}
```
(add `sort` to imports.)

- [ ] **Step 4: Update the two callers (pass nil for now)**

`internal/connect/connect.go:57`: `secret.Resolve(r, o.Secrets)` → `secret.Resolve(r, nil, o.Secrets)`. `internal/dispatchrun/dispatchrun.go:105`: `secret.Resolve(o.R, o.Secrets)` → `secret.Resolve(o.R, nil, o.Secrets)` (Task 2 replaces this with the `COVE_RUN_*` + minting logic).

- [ ] **Step 5: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "feat(runner,secret): OutputEnv + Resolve(extraEnv) for resolver env passthrough"
```

---

## Task 2: `dispatchrun` — `COVE_RUN_*` + per-git-step minting

**Files:** `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`

- [ ] **Step 1: Rewrite the air-gap/bracket tests (fail first)**

In `internal/dispatchrun/dispatchrun_test.go`, update `TestDispatchAirGapsTokenFromAgent` for per-git-step minting. The resolver-call order is now: base secrets (each once), **mint token (before prepare)**, **mint token (before complete)**, then the final `cat` — so the `Outputs` queue is `[<base secrets…>, token-1, token-2, cat-result]`. With one base secret `OTHER` and the token `AT_WORK_GIT_TOKEN`:
```go
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "other-value\n"},  // base secret OTHER
		{Stdout: tok1 + "\n"},      // mint for prepare
		{Stdout: tok2 + "\n"},      // mint for complete
		{Stdout: `{"status":{"ok":{}}}`}, // cat result
	}}
```
Assert: the three env-file writes are prepare(token1) / agent(no token, has `other-value`) / complete(token2) — i.e. `envWrites[0]` contains `tok1`, `envWrites[1]` contains neither `tok1` nor `tok2` but does contain `other-value`, `envWrites[2]` contains `tok2`; and neither token appears on any argv. Add a `TestDispatchPassesRunParamsToResolvers`: assert a resolver `Call.Env` contains `COVE_RUN_REPO=acme/myrepo`, `COVE_RUN_ISSUE=…`, `COVE_RUN_CLASS=implement` (derive from a task with an `issue.key` + the kit `origin`). Update the other `Dispatch` tests' `Outputs` for the new call order (each has the `AT_WORK_GIT_TOKEN`/base split — mostly they have no secrets, so only the final `cat` is queued; give the ones that DO declare secrets the right order). Run → FAIL.

- [ ] **Step 2: Implement the run params + per-git-step minting**

In `internal/dispatchrun/dispatchrun.go`, replace the single `env, err := secret.Resolve(o.R, nil, o.Secrets)` (~line 105) with:
```go
	// Run parameters exposed to the secret resolvers (e.g. a per-task minter).
	runEnv := map[string]string{
		"COVE_RUN_REPO":    o.Cfg.Origin.GitHub.Project,
		"COVE_RUN_ISSUE":   task.Issue.Key,
		"COVE_RUN_CLASS":   task.Worker.Class,
		"COVE_RUN_TIMEOUT": o.Timeout.String(),
	}
	// Split the code-host token (minted fresh per git step) from the base secrets
	// (resolved once). AT_WORK_GIT_TOKEN never enters the agent's env.
	var baseSpecs []secret.Spec
	var tokenSpec *secret.Spec
	for i := range o.Secrets {
		if o.Secrets[i].Name == gitTokenEnv {
			s := o.Secrets[i]
			tokenSpec = &s
		} else {
			baseSpecs = append(baseSpecs, o.Secrets[i])
		}
	}
	base, err := secret.Resolve(o.R, runEnv, baseSpecs) // fail closed before creating anything
	if err != nil {
		return err
	}
	// mint returns base + a freshly-minted code-host token (or just base if none is declared).
	mint := func() (map[string]string, error) {
		e := make(map[string]string, len(base)+1)
		for k, v := range base {
			e[k] = v
		}
		if tokenSpec != nil {
			tok, err := secret.Resolve(o.R, runEnv, []secret.Spec{*tokenSpec})
			if err != nil {
				return nil, err
			}
			for k, v := range tok {
				e[k] = v
			}
		}
		return e, nil
	}
```
Then rewrite the bracket (~lines 142–153):
```go
	// The bracket. Each git step gets a freshly-minted token; the agent gets base (no token).
	prepEnv, err := mint()
	if err != nil {
		return fmt.Errorf("mint token for prepare: %w", err)
	}
	if err := runStep(o.R, tgt, prepEnv, "at-work prepare", o.Timeout); err == nil {
		if err := writeVM(o.R, tgt, []byte(agentPrompt(w.Prompt)), promptVMPath); err != nil {
			return err
		}
		agentCmd := fmt.Sprintf("claude -p --dangerously-skip-permissions \"$(cat %s)\"", shellQuote(promptVMPath))
		_ = runStep(o.R, tgt, base, agentCmd, o.Timeout) // agent: no token; failure tolerated
	}
	compEnv, err := mint()
	if err != nil {
		return fmt.Errorf("mint token for complete: %w", err)
	}
	if err := runStep(o.R, tgt, compEnv, "at-work complete", o.Timeout); err != nil {
		return fmt.Errorf("at-work complete: %w", err)
	}
```
Delete the now-unused `withoutToken` helper (the agent uses `base`, which excludes the token by construction). Keep `gitTokenEnv`. Update the `Dispatch` doc comment to note per-git-step minting.

- [ ] **Step 3: Run + commit**

`go test ./internal/dispatchrun/ ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean; `go build -tags integration ./internal/dispatchrun/` compiles.
```bash
git commit -am "feat(dispatch): COVE_RUN_* passthrough + per-git-step token minting"
```

---

## Task 3: reference minter + kit wiring + docs

**Files:** create `kits/reference-worker/mint-github-token.sh`; modify `kits/reference-worker/config.yml`, `kits/reference-worker/RUNBOOK.md`, `internal/kit/refkit_test.go`, `docs/orchestration/at-cove-dispatch-interface.md`

- [ ] **Step 1: The reference minter (host-side template)**

Create `kits/reference-worker/mint-github-token.sh` — a **host** tool (at-cove runs the secret `command` on the host), NOT VM payload:
```sh
#!/bin/sh
# Reference per-task GitHub App minter. at-cove runs this on the HOST as the
# AT_WORK_GIT_TOKEN resolver, once before each git step, with COVE_RUN_* in the env.
# It mints a short-lived installation token scoped to COVE_RUN_REPO (contents + PRs).
#
# Provision (operator): a GitHub App with contents:write + pull_requests:write,
# installed on the org; then export on the at-cove host:
#   COVE_GH_APP_ID, COVE_GH_INSTALL_ID, COVE_GH_APP_KEY (path to the App .pem)
# Requires: openssl, curl, jq. Fail-closed: any error exits non-zero (aborts dispatch).
set -eu
: "${COVE_RUN_REPO:?COVE_RUN_REPO not set (run under at-cove dispatch)}"
: "${COVE_GH_APP_ID:?export COVE_GH_APP_ID}"
: "${COVE_GH_INSTALL_ID:?export COVE_GH_INSTALL_ID}"
: "${COVE_GH_APP_KEY:?export COVE_GH_APP_KEY (path to the App private key .pem)}"

repo_name=${COVE_RUN_REPO#*/}   # owner/name -> name (installation is org-scoped)

b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
now=$(date +%s)
header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64)
payload=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now - 60))" "$((now + 540))" "$COVE_GH_APP_ID" | b64)
sig=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "$COVE_GH_APP_KEY" | b64)
jwt="$header.$payload.$sig"

curl -fsS -X POST \
  -H "Authorization: Bearer $jwt" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$COVE_GH_INSTALL_ID/access_tokens" \
  -d "{\"repositories\":[\"$repo_name\"],\"permissions\":{\"contents\":\"write\",\"pull_requests\":\"write\"}}" \
  | jq -er '.token'
```
`chmod +x` it (and note in the commit it must land on the operator's `PATH`). `sh -n` must pass.

- [ ] **Step 2: Point the reference kit at the minter**

In `kits/reference-worker/config.yml`, change the `AT_WORK_GIT_TOKEN` secret:
```yaml
secrets:
  AT_WORK_GIT_TOKEN:
    description: per-task GitHub App installation token — push + PR on origin, minted per run
    command: ["mint-github-token.sh"]
```
(`refkit_test.go` already asserts the secret exists with a command — it stays green; if it pinned `["gh","auth","token"]`, update it to `["mint-github-token.sh"]`.)

- [ ] **Step 3: RUNBOOK — App provisioning**

In `kits/reference-worker/RUNBOOK.md`, add a section: create the GitHub App (contents:write + pull_requests:write), install it on the org, put `mint-github-token.sh` on the at-cove host's `PATH`, and export `COVE_GH_APP_ID`/`COVE_GH_INSTALL_ID`/`COVE_GH_APP_KEY`. Note the per-git-step minting (so the 1-hour App-token TTL never bounds run length) and that `at-cove` passes `COVE_RUN_REPO`/`ISSUE`/`CLASS`/`TIMEOUT`.

- [ ] **Step 4: Docs — minter shipped**

In `docs/orchestration/at-cove-dispatch-interface.md`, move the minter + `COVE_RUN_*` passthrough from **deferred** to **shipped** (the `## Status` list + the "Credential status" paragraph). Document: at-cove exposes `COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` to resolver commands during dispatch; a resolver becomes a per-run minter; scope is fixed in the minter (untrusted issue text can't widen it); the token is minted **fresh before each git step**, so the code host's fixed TTL never bounds run length; the three-authority separation (scheduler=tracker token; minter reads the App key on the host; worker VM gets only the scoped token). Reference [`at-cove-secrets.md`](../usage/at-cove-secrets.md) for resolver mechanics. Bump `updated`. If `at-cove-secrets.md` should note that dispatch resolvers see `COVE_RUN_*`, add one line there. Run docs-audit on the touched trees.

- [ ] **Step 5: Verify + commit**

`go test ./... && go build ./... && go vet ./...` clean; `sh -n kits/reference-worker/mint-github-token.sh`; `go build -tags integration ./internal/dispatchrun/` compiles; docs-audit clean.
```bash
git add kits/reference-worker/ internal/kit/refkit_test.go docs/orchestration/at-cove-dispatch-interface.md docs/usage/at-cove-secrets.md
git commit -m "feat(reference-worker): per-task GitHub App minter + docs (minter shipped)"
```

---

## Final verification (whole AET-24)

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged; `just build` → three binaries.
- [ ] `go build -tags integration ./internal/dispatchrun/` — e2e compiles.
- [ ] **Air-gap + per-step minting:** `TestDispatchAirGapsTokenFromAgent` shows the token in the prepare/complete env files but not the agent's, with a *distinct* token per git step; the token never on argv. `COVE_RUN_*` reaches the resolvers (`TestDispatchPassesRunParamsToResolvers`).
- [ ] The reference minter (`sh -n` clean) scopes to `COVE_RUN_REPO` with `contents`+`pull_requests` and fails closed on a missing var; RUNBOOK documents provisioning.
- [ ] Docs: the minter + `COVE_RUN_*` passthrough are **shipped** in `at-cove-dispatch-interface.md`; docs-audit clean.
- [ ] **Then** run the comprehensive whole-branch review (opus) over `feat/minter-passthrough` (Plans 1+2) and merge.

## Notes

- **Reconciliations** (re-grep): the exact `runner` test file + the `OS.Output` body to mirror; whether `refkit_test.go` pins the token command; the exact `## Status`/"Credential status" wording in the dispatch-interface doc.
- **Why base-once + token-per-step:** the minter is the only resolver that must be *fresh* per git op; re-resolving base secrets each step would be wasteful (and a static token resolver would just return the same value). The split keeps base fail-closed-before-build and mints the token exactly when a git step needs it.
- **The minter runs on the host** (at-cove's process), reading the App key from a host path — the scheduler's code never touches it, the worker VM never receives it. Logical three-authority separation (a separate broker is a future option).
