# Rename `at-cove dispatch` → `at-cove work` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the one-shot "execute one task in a fresh VM" command from `at-cove dispatch` to `at-cove work` — freeing the name `dispatch` for the scheduler (Plan C).

**Architecture:** A targeted rename of the user-facing command name + the scheduler's invocation of it + a couple of internal magic strings. Two green tasks: code, then docs. **Internal package/func names stay** (`internal/dispatchrun`, `dispatchrun.Dispatch`/`Reap`) — not user-facing; renaming them balloons the diff. **No logic changes.**

**Tech Stack:** Go 1.22 — pure rename, no new dependencies.

**Scope note:** Plan B of 3 for [AET-30](https://linear.app/aethons-tools/issue/AET-30), on branch `feat/cli-rename` (after Plan A). Plan C folds the `at-dispatch` scheduler into `at-cove dispatch` and reorganizes the docs (incl. any doc-file rename) — so Plan B keeps doc *filenames* as-is and only updates command references in content.

## Global Constraints

- **Rename the command `dispatch` → `work`** wherever it is user-facing: the subcommand name + help/usage text, the scheduler's argv (`at-cove dispatch …` → `at-cove work …`), the e2e invocation, and doc prose. Also the two internal magic strings tied to it (the container label + the tmpfs env path).
- **Keep internal Go names:** the package `internal/dispatchrun`, the funcs `Dispatch`/`Reap` — they stay. (A `doWork` calling `dispatchrun.Dispatch` is fine; the split is user-facing-name vs internal-name.)
- **Do NOT touch** the `at-dispatch` scheduler binary itself (Plan C) beyond the argv string it emits, and do NOT rename any doc *file* (Plan C).
- **Pure rename, no logic change.** Every commit builds + `go test ./...` green; `gofmt` clean; `go.mod` unchanged.

---

## Task 1: rename the command in code

**Files:** `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, `internal/dispatchrun/dispatchrun.go`, `internal/dispatch/scheduler/engine.go`, `internal/dispatch/scheduler/engine_test.go`, `internal/dispatchrun/e2e_integration_test.go`. Re-grep first: `grep -rn "dispatch" cmd/at-cove/ internal/dispatchrun/ internal/dispatch/scheduler/ --include=*.go`.

- [ ] **Step 1: `cmd/at-cove/main.go` — the subcommand**

- The cli command: `{Name: "dispatch", Brief: "run one unit of work in a fresh ephemeral sandbox", Run: … doDispatch(…)}` → `{Name: "work", Brief: "…", Run: … doWork(…)}`.
- Rename `doDispatch` → `doWork` (its `flag.NewFlagSet("dispatch", …)` → `"work"`; the `kitDirArg(pos, "dispatch", …)` → `"work"`; all user-facing strings `"at-cove dispatch: …"` → `"at-cove work: …"`; the doc comment `at-cove dispatch <kit-dir> …` → `at-cove work …`).
- Rename `dispatchName` → `workName`; its container-name format `"at-cove-dispatch-%s-%d-%d"` → `"at-cove-work-%s-%d-%d"`.
- Leave the `dispatchrun.Dispatch`/`dispatchrun.Reap` calls as-is (internal names).

- [ ] **Step 2: `internal/dispatchrun/dispatchrun.go` — the magic strings**

- Package doc comment (line 1): `at-cove dispatch` → `at-cove work`.
- `const Label = "at-cove.dispatch"` → `"at-cove.work"` (the container label used symmetrically by `RunEphemeral` + `ScavengeLabeled` — internal, stays consistent).
- `envVMPath = "/dev/shm/at-cove-dispatch-env"` → `"/dev/shm/at-cove-work-env"`.
- Keep the funcs `Dispatch`/`Reap` and the package name.

- [ ] **Step 3: the scheduler's invocation**

- `internal/dispatch/scheduler/engine.go`: the comment `at-cove dispatch` → `at-cove work`; the argv `[]string{"at-cove", "dispatch", cl.Kit, …}` → `{"at-cove", "work", cl.Kit, …}`.
- `internal/dispatch/scheduler/engine_test.go`: the `GotArgv` assertions checking `"at-cove dispatch"` / `--timeout` → `"at-cove work"` (re-grep `dispatch` in the file).

- [ ] **Step 4: the e2e + cmd tests**

- `internal/dispatchrun/e2e_integration_test.go`: the comment + `exec.Command("at-cove", "dispatch", …)` → `("at-cove", "work", …)`.
- `cmd/at-cove/main_test.go`: the dispatch-command tests — `run([]string{"dispatch", …})` → `run([]string{"work", …})`; assertions on `"dispatch"` / `"at-cove dispatch"` text → `"work"` / `"at-cove work"` (re-grep `dispatch`/`Dispatch` in the file; rename test funcs like `TestDryRunDispatch…`/`TestDispatch…` → `…Work…` for clarity).

- [ ] **Step 5: Verify + commit**

Run:
```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
go build -tags integration ./internal/dispatchrun/    # e2e compiles
grep -rn "at-cove dispatch\|at-cove.dispatch\|\"dispatch\"\|doDispatch\|dispatchName\|at-cove-dispatch-env" cmd/at-cove/ internal/dispatchrun/ internal/dispatch/scheduler/ --include=*.go   # expect: nothing
```
Expected: green; the grep empty. The internal package `dispatchrun` + funcs `Dispatch`/`Reap` legitimately remain (not matched by the grep above). `just build` still builds all binaries.
```bash
git commit -am "rename(cmd): at-cove dispatch -> at-cove work (command, label, scheduler argv)"
```

---

## Task 2: rename the command in docs

**Files:** `docs/OVERVIEW.md`, `docs/orchestration/at-cove-dispatch-interface.md`, `docs/orchestration/scheduler-config.md`, `docs/orchestration/linear-agent-workflow.md`, `docs/orchestration/INDEX.md`. Re-grep: `grep -rn "at-cove dispatch" docs/ | grep -v docs/superpowers/`.

- [ ] **Step 1: Update command references (content only — no file renames)**

Replace `at-cove dispatch` → `at-cove work` in the prose/examples of each doc:
- `docs/OVERVIEW.md`: the command-surface row `at-cove dispatch <kit> --in <f> --out <f> …` → `at-cove work <kit> --in <f> --out <f> …`; any other `at-cove dispatch` mention.
- `docs/orchestration/at-cove-dispatch-interface.md`: every `at-cove dispatch` → `at-cove work` (the command synopsis, the step list, the status/credential prose). **Keep the filename** `at-cove-dispatch-interface.md` and its `## Command surface — at-cove dispatch` heading may become `## Command surface — at-cove work`. Bump `updated: 2026-07-10`.
- `docs/orchestration/scheduler-config.md`: the `at-cove dispatch <kit> --in … --out …` example → `at-cove work …`.
- `docs/orchestration/linear-agent-workflow.md`: any `at-cove dispatch` → `at-cove work`.
- `docs/orchestration/INDEX.md`: the row describing the dispatch-interface doc ("the `at-cove dispatch` command …") → "the `at-cove work` command …".

> Note: the *file* `at-cove-dispatch-interface.md` and the term "dispatch interface" stay for now — Plan C (which makes `at-cove dispatch` the scheduler) reorganizes the orchestration docs, and that's where any doc-file rename belongs. Do NOT rename doc files in this task.

- [ ] **Step 2: Verify + commit**

Run:
```
grep -rn "at-cove dispatch" docs/ | grep -v docs/superpowers/    # expect: nothing
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/orchestration/    # 0 errors
```
Expected: the grep empty (the scheduler command `at-cove dispatch` doesn't exist yet — Plan C adds it); docs-audit 0 errors (pre-existing warning ok); links resolve.
```bash
git commit -am "docs(rename): at-cove dispatch -> at-cove work (command references)"
```

---

## Final verification (Plan B)

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged; `just build` OK; `go build -tags integration ./internal/dispatchrun/` compiles.
- [ ] `grep -rn "at-cove dispatch\|at-cove.dispatch\|at-cove-dispatch-env\|doDispatch\|dispatchName" cmd/ internal/ docs/ | grep -v docs/superpowers/` — nothing (except the doc *filename* `at-cove-dispatch-interface.md`, which is intentionally kept for Plan C).
- [ ] The scheduler now invokes `at-cove work` (`engine.go` argv); the e2e shells `at-cove work`.
- [ ] Internal `dispatchrun` package + `Dispatch`/`Reap` funcs intact (kept by design).

## Notes

- **Why keep internal names:** `dispatchrun.Dispatch` is called by `doWork` — the user-facing verb is `work`, the internal orchestrator keeps its name to bound churn. A future cleanup could rename the package if desired.
- **Doc-file naming is Plan C's job:** Plan C folds `at-dispatch` → `at-cove dispatch` (the scheduler), at which point the orchestration docs get organized coherently (and `at-cove-dispatch-interface.md` may be renamed to reflect that it documents `at-cove work`). Keeping filenames stable here avoids double-churn.
