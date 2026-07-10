# Rename `at-work` → `at-task` (full depth) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the git/PR worker binary `at-work` → `at-task` (so `at-task prepare` / `at-task complete`), full depth: the `.at-work/` handoff dir → `.at-task/`, and the `AT_WORK_*` env vars → `AT_TASK_*`.

**Architecture:** A **safe targeted find-replace** — `at-work`, `AT_WORK_`, and `.at-work` are distinct strings that never appear inside anything we must keep — plus a couple of dir/file renames. Two green tasks: (1) all code + kit + build scripts, atomically (keeps the dispatch flow consistent); (2) the docs.

**Tech Stack:** Go 1.22 — **no logic changes, no new dependencies**. Pure rename.

**Scope note:** Plan A of 3 for [AET-30](https://linear.app/aethons-tools/issue/AET-30). Plans B (`at-cove dispatch`→`work`) and C (fold `at-dispatch`→`at-cove dispatch`) follow.

## Global Constraints — the exact rename

**Replace these EXACT tokens everywhere they appear** (all three are distinct — no collisions):
- `at-work` → `at-task`
- `AT_WORK_` → `AT_TASK_`  (covers `AT_WORK_GIT_TOKEN`→`AT_TASK_GIT_TOKEN`, `AT_WORK_ASKPASS_TOKEN`→`AT_TASK_ASKPASS_TOKEN`)
- `.at-work` → `.at-task`  (the handoff dir, in paths + comments)

**Also:** the Go identifier `workSubdir` (in `internal/dispatch/worker/`, value `".at-work"`) → `taskSubdir` (value `".at-task"`). And the file/dir renames in each task.

**PRESERVE — do NOT rename any of these** (they are the *worker*/*work* concepts, not the `at-work` binary):
- `worker`, `workers`, `worker-result` / `worker-result.json`, the Go types `WorkerResult`/`WorkerStatus`/`WorkerOK`/`WorkerNeedsInput`/`WorkerError`, and the package `internal/dispatch/worker`
- `work-branch` (the git branch field), `workDir` / `/home/agent/work` (the VM working dir), `workers` (kit config), `reference-worker` (the kit dir name)

**Every commit builds + `go test ./...` green.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged. **No logic changes** — only names.

---

## Task 1: rename in code + kit + build scripts (atomic)

Do the whole code/kit rename in one commit so the flow stays consistent (the kit declares `AT_TASK_GIT_TOKEN`, at-cove's bracket sets it and calls `at-task prepare`, and the `at-task` binary reads it — all together).

**Files (all that match under code/kit/scripts):** `cmd/at-work/` (→ `cmd/at-task/`), `internal/dispatch/worker/*.go`, `internal/dispatchrun/*.go`, `cmd/at-cove/main.go`, `internal/kit/config.go`, `internal/kit/refkit_test.go`, `internal/dispatch/github/github.go`, `internal/dispatch/github/github_integration_test.go`, `kits/reference-worker/config.yml`, `kits/reference-worker/image-files/.install-files/install.sh`, `kits/reference-worker/mint-github-token.sh`, `kits/reference-worker/RUNBOOK.md`, `scripts/build.sh`, `scripts/build-images.sh`.

- [ ] **Step 1: Rename the binary directory**

```bash
git mv cmd/at-work cmd/at-task
```

- [ ] **Step 2: Apply the three token replacements across code + kit + scripts**

Re-grep the live set first: `grep -rIl "at-work\|AT_WORK_\|\.at-work" cmd/ internal/ kits/ scripts/`. In each matching file, replace `at-work`→`at-task`, `AT_WORK_`→`AT_TASK_`, `.at-work`→`.at-task`. Key spots to confirm by hand:
- `cmd/at-task/main.go` (moved): the package doc + the `prepare`/`complete` help strings ("at-work"→"at-task"); `os.Getenv("AT_WORK_GIT_TOKEN")` → `AT_TASK_GIT_TOKEN` (two call sites — `NewShellGit` + `github.New`).
- `internal/dispatch/worker/git.go`: `AT_WORK_ASKPASS_TOKEN` → `AT_TASK_ASKPASS_TOKEN` (the askpass env, set + read); the `.at-work` exclude uses `taskSubdir` after Step 3.
- `internal/dispatchrun/dispatchrun.go`: `gitTokenEnv = "AT_WORK_GIT_TOKEN"` → `"AT_TASK_GIT_TOKEN"`; the bracket command strings `"at-work prepare"`/`"at-work complete"` → `"at-task prepare"`/`"at-task complete"`; the VM path consts `taskVMPath`/`resultVMPath` (`workDir + "/.at-work/…"` → `"/.at-task/…"` — note `workDir`/`/home/agent/work` STAYS).
- `cmd/at-cove/main.go`: any `at-work` in the dispatch help/dry-run text → `at-task`.
- `internal/kit/refkit_test.go`: `cfg.Secrets["AT_WORK_GIT_TOKEN"]` → `["AT_TASK_GIT_TOKEN"]`.
- `kits/reference-worker/config.yml`: the secret key `AT_WORK_GIT_TOKEN` → `AT_TASK_GIT_TOKEN`.
- `kits/reference-worker/image-files/.install-files/install.sh`: `go install github.com/aethons-tools/cove/cmd/at-work@…` → `cmd/at-task@…`; any comment.
- `kits/reference-worker/mint-github-token.sh` + `RUNBOOK.md`: comments naming the `AT_WORK_GIT_TOKEN` secret / `at-work`.
- `scripts/build.sh` + `scripts/build-images.sh`: the `at-work` binary in the `BINARIES` list / build loop → `at-task`.

- [ ] **Step 3: `workSubdir` → `taskSubdir`**

In `internal/dispatch/worker/` (defined in `types.go`; used in `format.go`, `resultv2.go`, `git.go`, and tests): rename the identifier `workSubdir` → `taskSubdir`. Its value is already `.at-task` from Step 2.

- [ ] **Step 4: Verify + commit**

Run:
```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
grep -rn "at-work\|AT_WORK_\|\.at-work\|workSubdir" cmd/ internal/ kits/ scripts/    # expect: nothing
grep -rn "worker\|workers\|worker-result\|work-branch\|workDir\|/home/agent/work\|reference-worker" internal/dispatch/worker/ internal/dispatchrun/ | head   # confirm these PRESERVED (still present)
go build -tags integration ./internal/dispatchrun/    # e2e compiles
```
Expected: build/vet/test green; the first grep empty; the preserved tokens still present; `go.mod` unchanged. `just build` should now produce `at-task` (not `at-work`).
```bash
git add -A
git commit -m "rename(at-work->at-task): binary, .at-task/ handoff dir, AT_TASK_* env vars (code+kit)"
```

---

## Task 2: rename in docs

**Files:** rename `docs/usage/at-work.md` → `docs/usage/at-task.md`, `docs/usage/at-work-inputs.md` → `at-task-inputs.md`, `docs/usage/at-work-output.md` → `at-task-output.md`; modify `docs/usage/INDEX.md`, `docs/OVERVIEW.md`, `docs/orchestration/INDEX.md`, `docs/orchestration/at-cove-dispatch-interface.md`, `docs/orchestration/linear-agent-workflow.md`, `docs/orchestration/scheduler-config.md`, `AGENTS.md`.

- [ ] **Step 1: Rename the usage doc files**

```bash
git mv docs/usage/at-work.md docs/usage/at-task.md
git mv docs/usage/at-work-inputs.md docs/usage/at-task-inputs.md
git mv docs/usage/at-work-output.md docs/usage/at-task-output.md
```

- [ ] **Step 2: Apply the token replacements in all docs**

Re-grep: `grep -rIl "at-work\|AT_WORK_\|\.at-work" docs/ AGENTS.md | grep -v docs/superpowers/`. In each (the renamed usage docs + `docs/usage/INDEX.md` + `docs/OVERVIEW.md` + the four `docs/orchestration/*` + `AGENTS.md`): replace `at-work`→`at-task`, `AT_WORK_`→`AT_TASK_`, `.at-work`→`.at-task`. This includes:
- the renamed usage docs' frontmatter (`summary`/`read_when`/`owns`), headings, the `.at-work/` file-handoff prose, the `AT_WORK_GIT_TOKEN` mentions; bump `updated: 2026-07-10`.
- **inbound links** to the renamed files: any `at-work.md`/`at-work-inputs.md`/`at-work-output.md` link target (in `docs/usage/INDEX.md`, the orchestration docs, OVERVIEW) → `at-task*.md`.
- `AGENTS.md`: the binaries line (`at-cove`, `at-work` → `at-task`) and the `cmd/at-work` entry-point reference (`cmd/at-task`).
- Do NOT touch `docs/superpowers/` (frozen design history) — its `at-work`/`.at-work` references are historical.

- [ ] **Step 3: Verify + commit**

Run:
```
grep -rn "at-work\|AT_WORK_\|\.at-work" docs/ AGENTS.md | grep -v docs/superpowers/    # expect: nothing
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage/ ; python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/orchestration/
```
Expected: the grep empty (live docs clean); docs-audit 0 errors on both trees (a pre-existing line-count warning is fine); every renamed-file link resolves (no dangling `at-work*.md`).
```bash
git add -A
git commit -m "docs(rename): at-work -> at-task (usage docs renamed, .at-task/, AT_TASK_*)"
```

---

## Final verification (Plan A)

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged; `just build` → binaries include `at-task` (not `at-work`).
- [ ] `go build -tags integration ./internal/dispatchrun/` compiles.
- [ ] `grep -rn "at-work\|AT_WORK_\|\.at-work\|workSubdir\|cmd/at-work" cmd/ internal/ kits/ scripts/ docs/ AGENTS.md | grep -v docs/superpowers/` — **nothing** (frozen design history under `docs/superpowers/` may retain them).
- [ ] Preserved: `internal/dispatch/worker` package, `worker-result.json`, `Worker*` types, `work-branch`, `workDir`/`/home/agent/work`, `workers`, `reference-worker` all intact.
- [ ] docs-audit clean; no dangling links to the renamed usage docs.

## Notes

- **This is a pure rename** — zero behavior/logic change. A reviewer's job is: (a) every `at-work`/`AT_WORK_`/`.at-work` occurrence renamed; (b) NOTHING in the preserve list touched (no `worker`→`tasker`, no `work-branch`→`task-branch`); (c) file/dir renames done via `git mv` (history preserved); (d) build+tests green; (e) links resolve.
- **Why atomic (Task 1):** the env-var name spans the kit config (declares `AT_TASK_GIT_TOKEN`), `dispatchrun` (`gitTokenEnv` sets it + `refkit_test` asserts it), and the `at-task` binary (reads it) — renaming them together keeps the real dispatch flow consistent and `refkit_test` green in one step.
- **Plan B next** renames the `at-cove dispatch` command → `at-cove work`; **Plan C** folds the `at-dispatch` scheduler into `at-cove dispatch`.
