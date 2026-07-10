# at-cove config v2 — Plan 3: reference kit + --loop cleanup + docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish AET-29: delete the now-unused reference-kit worker scripts, remove the leftover `--loop` plumbing, and rewrite the docs so the shipped product (at-cove-owned `workers` bracket) matches the docs — closing the config-v2 migration.

**Architecture:** Three independent tasks. (1) The reference kit no longer ships `run-worker.sh`/`run-agent.sh`/`AT_WORK_AGENT_COMMAND` — at-cove owns the bracket now, so those are deleted. (2) The `--loop` flag on `destroy`/`status` and the `internal/state` loop-instance helpers (dead since Plan 1 removed the loop feature) are removed. (3) `OVERVIEW.md`'s command surface and `orchestration/at-cove-dispatch-interface.md` are updated from the old kit-`dispatch.command`/`run-worker.sh` model to the host-orchestrated `workers` bracket.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3`. **No new dependencies.** POSIX `sh` for kit scripts; Markdown for docs.

**Scope note:** Plan 3 of 3 for [AET-29](https://linear.app/aethons-tools/issue/AET-29), on branch `feat/at-cove-config-v2` (builds on Plans 1–2). Canonical config: [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md); design: [`2026-07-09-at-cove-config-v2-design.md`](../specs/2026-07-09-at-cove-config-v2-design.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies**.
- **Every commit builds + `go test ./...` green.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
- The e2e (`internal/dispatchrun/e2e_integration_test.go`, `//go:build integration`) is maintainer-run; keep it **compiling** under `-tags integration`. It already shells `at-cove dispatch kits/reference-worker --in <task.json> --out <task-result.json>` with `worker.class:"implement"` and asserts `status.ok.pr-url` — that works with the new bracket (the reference kit declares `workers.implement`), so it needs no change beyond docs it points at.
- **Template-vs-repo line:** `kits/reference-worker/` is payload; edit it as the worker template. `docs/` is this repo's docs.

---

## Task 1: reference kit — delete the worker scripts

The bracket is now at-cove's; the kit only declares `workers` (Plan 2). Remove the dead scripts + their wiring.

**Files:** delete `kits/reference-worker/image-files/usr/local/bin/run-worker.sh` + `run-agent.sh`; modify `kits/reference-worker/config.yml`, `kits/reference-worker/image-files/.install-files/install.sh`, `kits/reference-worker/RUNBOOK.md`, `internal/kit/refkit_test.go`.

- [ ] **Step 1: Update `refkit_test.go` to fail-first**

`internal/kit/refkit_test.go` (`TestReferenceWorkerKitConfig`) asserts `cfg.Image.Env["AT_WORK_AGENT_COMMAND"] == "run-agent.sh"`. Delete that assertion block (the kit no longer sets it). Keep the `AT_WORK_GIT_TOKEN` secret + `workers["implement"].Prompt` assertions. Run `go test ./internal/kit/ -run ReferenceWorker` → still green (it's a deletion) — this step just removes the stale assertion so the config edit in Step 3 doesn't fail it.

- [ ] **Step 2: Delete the scripts**

```bash
git rm kits/reference-worker/image-files/usr/local/bin/run-worker.sh kits/reference-worker/image-files/usr/local/bin/run-agent.sh
```

- [ ] **Step 3: Remove `AT_WORK_AGENT_COMMAND` from `config.yml`**

In `kits/reference-worker/config.yml`, delete the `env:` block under `image:` (its only key was `AT_WORK_AGENT_COMMAND: run-agent.sh`). Leave `setup-scripts`/`allowed-domains`, `secrets`, and `workers` intact. (Add a brief comment on `workers` noting at-cove wraps each class in the prepare→agent→complete bracket.)

- [ ] **Step 4: Update `install.sh`**

In `kits/reference-worker/image-files/.install-files/install.sh`, delete the final `chmod 0755 /usr/local/bin/run-worker.sh /usr/local/bin/run-agent.sh` line and the comment above it ("The worker/agent scripts arrive via image-files…"). Add a one-line note that at-cove drives the `at-work prepare → claude → at-work complete` bracket itself (the image only needs `at-work`, `claude`, and the project toolchain on PATH).

- [ ] **Step 5: Update `RUNBOOK.md`**

Replace every `run-worker.sh`/`run-agent.sh`/`dispatch.command`/`AT_WORK_AGENT_COMMAND` reference with the new model: the kit declares `workers.<class>.prompt`; at-cove injects `.at-work/task.json` under `/home/agent/work`, runs `at-work prepare` → `claude -p "<class prompt + result protocol>"` (token-stripped) → `at-work complete`, and extracts `.at-work/task-result.json`. The prerequisites (colima, `gh auth`, seeded claude login, a scratch repo) and the `just e2e` invocation are unchanged.

- [ ] **Step 6: Verify + commit**

```
go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/
go build -tags integration ./internal/dispatchrun/    # e2e still compiles
grep -rn "run-worker\|run-agent\|AT_WORK_AGENT_COMMAND\|dispatch.command\|dispatch.input" kits/reference-worker/   # only prose-free; nothing functional
```
Expected: pass; `go.mod` unchanged. (The `install.sh` still installs `at-work` + `claude` — unchanged.)
```bash
git add kits/reference-worker/ internal/kit/refkit_test.go
git commit -m "refactor(reference-worker): drop run-worker.sh/run-agent.sh — at-cove owns the bracket"
```

---

## Task 2: remove the leftover `--loop` plumbing

Plan 1 removed the loop feature but left the `--loop` flag on `destroy`/`status` and the `internal/state` loop-instance helpers (dead — no loop instance can be created). Remove them.

**Files:** `cmd/at-cove/main.go`, `internal/state/state.go`, `internal/state/state_test.go`, `cmd/at-cove/main_test.go`

- [ ] **Step 1: Confirm the footprint**

Run: `grep -rn "\-\-loop\|loopName\|LoopInstance\|ValidLoopName\|loopNamePattern\|HasLoopInstances\|OtherLoopInstancesExist" cmd/ internal/ --include=*.go`
This is the deletion checklist. Confirm the only users are `instanceCmd` + `doDestroyInstance` (cmd) and the `state` helpers themselves (+ their tests).

- [ ] **Step 2: Simplify `instanceCmd`**

In `cmd/at-cove/main.go` `instanceCmd` (~L143-166): remove the `loopName := fs.String("loop", …)` flag and the `if *loopName != "" { …ValidLoopName…LoopInstance… }` block. `inst` is always `state.Interactive`:
```go
func instanceCmd(cmd string, args []string, r runner.Runner, g cli.Globals, out, errw io.Writer, do func(kitDir string, inst state.Instance) error) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := kitDirArg(pos, cmd, errw)
	if code != 0 {
		return code
	}
	return exitCode("at-cove", do(kitDir, state.Interactive), errw)
}
```
Update its doc comment (no more `--loop`). (Consider whether `instanceCmd` still earns its own function now that it doesn't branch — leaving it is fine; it still de-dups destroy/status flag parsing.)

- [ ] **Step 3: Simplify `doDestroyInstance`**

In `doDestroyInstance` (~L383-431): with only the interactive instance, remove the loop branching.
- Dry-run: always the interactive message (`would destroy … remove image … delete …`); drop the `HasLoopInstances`/else branch.
- Image reclaim: remove the whole `if inst != state.Interactive { … } else if state.HasLoopInstances(kitDir) { bi.Image = "" }` block — always reclaim the image:
```go
	bi := instanceFromState(st)
	if err := b.Destroy(bi, keepVolumes); err != nil {
		return err
	}
	return state.DeleteFor(kitDir, inst)
```
The `inst` parameter is now always `state.Interactive`; leave the `*For(kitDir, inst)` calls as-is (collapsing the `state` For-variants is out of scope). Update the function's doc comment (drop "but NOT for a loop instance").

- [ ] **Step 4: Delete the `state` loop helpers**

In `internal/state/state.go`: delete `LoopInstance`, `loopNamePattern`, `ValidLoopName`, `HasLoopInstances`, `OtherLoopInstancesExist`. Remove the `regexp` import if it becomes unused. Keep `Instance`, `Interactive`, and the `*For` functions.

- [ ] **Step 5: Remove the loop tests**

`internal/state/state_test.go`: delete tests for the removed helpers (grep `Loop`/`ValidLoopName`/`HasLoopInstances`). `cmd/at-cove/main_test.go`: delete/adjust any test that passed `--loop` to destroy/status or asserted the loop-image-keep behavior (grep `--loop`/`loop`).

- [ ] **Step 6: Verify + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
Confirm: `grep -rn "\-\-loop\|LoopInstance\|ValidLoopName\|HasLoopInstances\|OtherLoopInstancesExist\|loop-" cmd/ internal/ --include=*.go` returns nothing (a `loop-*.json` state file left by a pre-v2 build is now simply ignored — acceptable, per the AET-29 decision).
```bash
git commit -am "refactor: remove leftover --loop flag + state loop-instance helpers"
```

---

## Task 3: docs — command surface + dispatch interface

Bring the docs to the shipped model: at-cove owns the `workers` bracket; no `dispatch.command`/`--loop`.

**Files:** `docs/OVERVIEW.md`, `docs/orchestration/at-cove-dispatch-interface.md`

- [ ] **Step 1: `OVERVIEW.md` command surface**

- The `at-cove dispatch` row: replace "inject `--in` at the kit's `dispatch.input` VM path, run the kit's `dispatch.command`, extract `dispatch.output` to `--out`" with: "inject `--in` as the task, run the **at-work worker bracket** (`prepare` → agent → `complete`) for the task's `worker.class` (declared in the kit's `workers`), extract the result to `--out`, destroy." Keep the flags.
- The "Flags specific to a command (e.g. `--raw`, `--ws`, `--loop`)" line: drop `--loop` (e.g. leave `--raw`, `--ws`).
- Grep `OVERVIEW.md` for any remaining `dispatch.command`/`dispatch.input`/`loop`/`setup:` references and fix or remove them. Bump nothing else.

- [ ] **Step 2: Rewrite `at-cove-dispatch-interface.md`**

This doc currently describes the OLD model (kit `dispatch.command`/`dispatch.input`/`dispatch.output`, `run-worker.sh`, the air-gap living in the kit script). Rewrite to the shipped model, preserving the doc's good structure (grounding, governing principle, three-authority table, isolation-by-class, status). Key corrections (use docs-author; reference the usage docs rather than restating schemas):

- **Frontmatter** `summary`/`read_when`/`owns`: the seam is now at-cove's `workers` map + the **host-orchestrated bracket**; drop `dispatch.input`/`dispatch.output`/`dispatch.command`. Bump `updated`.
- **§Command surface** steps 3–5 → : (3) inject the task at the at-cove-owned VM path `/home/agent/work/.at-work/task.json`; (4) **at-cove drives the bracket itself over ssh** — `at-work prepare` (env with the token) → `claude -p "<class prompt + result protocol>"` (env **without** `AT_WORK_GIT_TOKEN`) → `at-work complete` (env with the token); (5) extract `/home/agent/work/.at-work/task-result.json` to `--out`, destroy. at-cove reads `worker.class` from the task to pick the kit's `workers[class].prompt` (it is no longer fully opaque to the input); the command synopsis stays `at-cove dispatch <kit-dir> --in <task.json> --out <task-result.json> …`.
- **§The kit-declared entrypoint and the credential air-gap** → retitle to reflect that **at-cove owns the bracket**. Replace the `run-worker.sh` sh snippet with a description of at-cove's per-step env: the token is included for `prepare`/`complete` and withheld from the agent step (each ssh step writes/removes its own tmpfs env file, so the token is **only ever transmitted to the VM for the two git steps** — a stronger air-gap than the old `env -u` in a kit script). The kit declares `workers[class].prompt` (role/behavior); at-cove appends the standard result protocol.
- **§Worker contract**: the scheduler builds the task; at-cove injects it and extracts the result at the fixed `.at-work/` paths; file shapes owned by the [at-work usage docs](../usage/at-work.md). (No `dispatch.input`/`dispatch.output`.)
- **§Three separated authorities** table: unchanged in substance (the "Agent — token stripped for its step" row is now enforced by at-cove, not a kit script) — tweak that note.
- **§Status — shipped vs deferred**: move "the reference worker kit + the bracket" to **shipped** (the air-gap is now at-cove-owned); keep the minter/`COVE_RUN_*` passthrough and multi-code-host deferred. Update the "Deferred: run-worker.sh" line (that script is gone).

- [ ] **Step 3: docs-audit + commit**

Run the docs-audit skill on the two touched docs (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/orchestration/` and confirm the usage tree still clean). Fix any dangling link. Bump `updated`.
```bash
git add docs/OVERVIEW.md docs/orchestration/at-cove-dispatch-interface.md
git commit -m "docs(at-cove): dispatch interface + command surface -> host-orchestrated workers bracket"
```

---

## Final verification (whole AET-29)

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged; `just build` → three binaries.
- [ ] `go build -tags integration ./internal/dispatchrun/` — e2e compiles.
- [ ] Config surface is `{name, secrets, image, workers}` and matches [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md) field-for-field.
- [ ] `grep -rn "dispatch.command\|dispatch.input\|DispatchConfig\|run-worker\|run-agent\|--loop\|LoopInstance\|SetupScript\b\|cfg.Setup\|cfg.Backend\|cfg.Loops" cmd/ internal/ kits/ docs/OVERVIEW.md docs/orchestration/ --include=*.go --include=*.yml --include=*.md` — no live references (design-history under `docs/superpowers/` may still mention them; that's frozen).
- [ ] Air-gap intact end-to-end: `at-cove` withholds `AT_WORK_GIT_TOKEN` from the agent step (`TestDispatchAirGapsTokenFromAgent`); no secret on argv.
- [ ] docs-audit clean on `docs/usage/` and `docs/orchestration/`.
- [ ] **Then** run the comprehensive whole-branch review (opus) over `feat/at-cove-config-v2` and merge.

## Notes

- **Reconciliations** (re-grep; lines drift): the exact `instanceCmd`/`doDestroyInstance` bodies; which `state_test.go`/`main_test.go` tests reference `--loop`/loop helpers; whether `regexp` is still used in `state.go` after the deletions.
- **The reference kit still needs `at-work` + `claude` + the toolchain on PATH** (install.sh) — only the `run-worker.sh`/`run-agent.sh` orchestration scripts go; at-cove now issues `at-work prepare`/`claude -p`/`at-work complete` directly.
- **After this plan, AET-29 is complete:** the whole `at-cove` config + dispatch path matches the usage docs, and the old `dispatch`/`setup`/`loops`/`backend`/`run-worker.sh` surface is gone from the live code and docs.
