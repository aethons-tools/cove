# Structured logging across `at-cove` / `at-task` — Design

**Date:** 2026-07-15
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Tracks:** enables cloud deployment (a prerequisite; see the "To the Cloud" project and [COV-7](https://linear.app/aethons-tools/issue/COV-7)); delivers immediate operator diagnosability. Closes the diagnosability gap surfaced while debugging a dispatch-time agent `401` (see §3, §8).
**Relates to:** [COV-8](https://linear.app/aethons-tools/issue/COV-8) hardening (the fail-closed-with-attribution behavior in §8 overlaps the guardrails theme); [COV-21](https://linear.app/aethons-tools/issue/COV-21) (egress-wall → NEEDS INPUT: the agent-outcome classifier in §6 is where that detection plugs in).

## 1. Purpose

The product has almost no logging. The only real logger is a single `log.New(stderr, "at-cove dispatch ", …)` in the dispatch scheduler (~11 `Printf` sites in `internal/dispatch/scheduler/engine.go`); everything else is ad-hoc `fmt.Fprintf(stderr, …)` (26 sites in `at-cove`, 6 in `at-task`, plus `at-mint`/`connect`/`cli`). Nothing is structured, leveled, or correlated.

Because the system spans **processes and hosts** — host scheduler (`at-cove dispatch`) → shells `at-cove work` (host) → SSH → `at-task prepare` / agent / `at-task complete` (in the VM) — a cross-layer failure is opaque: it surfaces on the host console with no attribution to the layer, run, or step that produced it. A dispatched worker's agent returning `401 Invalid authentication credentials` appears as a bare line next to the scheduler's own output, with no indication of which of the ~8 layers failed.

This design establishes **structured logging built on `log/slog`**, with **end-to-end correlation** from the scheduler through the VM, an **attended/unattended output model**, and a **secret-safety boundary** that holds even as logs ship to a cloud aggregator. Structured logging is a hard prerequisite for cloud deployment (machine-ingestable, correlated logs) and is immediately valuable for local diagnosis.

## 2. Scope

**In scope.** A shared `internal/logging` package; adoption across the diagnostic/operational surface (scheduler, `at-cove work` orchestration, secret resolution, VM lifecycle, the `at-task` worker bracket); cross-layer correlation (a per-dispatch run ID + `step` attribution) threaded into the VM; capture and structured merge of VM-side (`at-task`) records into the host stream; a **dual-output convention** so user-facing errors emit both a human line and a structured record; the secret-safety boundary and redaction backstop.

**Out of scope (unchanged).** Pure-UX CLI output — help text, `--dry-run` intent lines, `--reap` summaries, machine-readable command data — stays plain output on **stdout**. The agent's own (uncontrolled) log format is not restructured; it is captured, bounded, and labeled at the edges (§6). No log-shipping agent / no cloud sink integration is built here — unattended mode writes JSON to **stderr** and lets the deployment platform capture it.

## 3. Governing decisions

- **Foundation: Go stdlib `log/slog`.** No new dependency (matches the repo's zero-new-deps ethos and the egress-locked build). `TextHandler` and `JSONHandler` ship in stdlib; we select between them by output mode (§4).
- **Two output modes, chosen by consumer, not by a format flag:**
  - **unattended** (cloud / headless): **structured JSON to stderr**, nothing else. The platform captures stderr.
  - **attended** (human at a terminal): **human-friendly text to stderr** + **full structured JSON to a file** (§5).
  - Mode is resolved by **TTY auto-detect** (stderr is a terminal → attended; else → unattended), overridable by `--log-mode attended|unattended` / `AT_LOG_MODE`.
- **Logs go to stderr; stdout stays clean.** Diagnostic logs never intermix with CLI data output, so JSON logs can be piped/captured without corrupting command output.
- **The logger flows via `context.Context`**, not package globals — `logging.Into(ctx, logger)` / `logging.From(ctx)`. Every long-lived path already threads `ctx`; this keeps correlation automatic and tests hermetic.
- **Correlation is a first-class through-line.** The scheduler mints a **run ID** per dispatch and attaches `run`, `issue`, `class`, and `step` as `slog` attributes; the run ID is passed **into the VM** so `at-task` records join the same trace (§7).
- **"Diagnostic + errors" dual output.** User-facing errors emit a human line *and* a structured record. In attended mode these land on **different streams** (text→stderr, JSON→file), so a human never sees an error twice; in unattended mode there is only the JSON record (nobody is watching a terminal), and the human line is dropped (§8).
- **Secret-safety is structural, not best-effort.** Injection paths (resolved secret values, the VM env map) are **never logged at any level**. Only records we construct — secret-free by design — reach the sink. Raw agent/VM output stays **VM-local** and is never shipped to the structured sink; a known-secret scrubber is a defense-in-depth backstop for any raw text logged locally at debug (§6, §9).
- **Logs are a runtime artifact of the kit** — they live under the kit's excluded runtime dir `<kitDir>/.state/logs/` (§5), which is already gitignored and never image payload.

## 4. Output-mode model

| Mode | selection | stderr | file |
| --- | --- | --- | --- |
| **unattended** | no TTY on stderr, or `--log-mode unattended` | structured JSON (all levels ≥ configured) | — |
| **attended** | TTY on stderr, or `--log-mode attended` | human-friendly text (info+) | structured JSON, full trace (debug+) |

Attended mode runs **two handlers at independent levels** behind a small hand-rolled **tee `slog.Handler`** (fan-out to both; no dependency): stderr stays calm at `info+` for the human, while the file captures everything at `debug+` — including every `step` transition and every merged VM record — for post-hoc debugging. `--log-level` / `AT_LOG_LEVEL` sets the stderr level (default `info`); the file is always `debug+`. `--no-log-file` disables the file in attended mode.

`at-task` inside the VM is inherently unattended → JSON to its stderr → captured and merged by the host (§7). No special case.

## 5. Log file location

Attended-mode JSON goes to the kit's **excluded runtime dir**:

```
<kitDir>/.state/logs/at-cove-<run-id>.jsonl      # one file per dispatch run
<kitDir>/.state/logs/at-cove-<timestamp>.jsonl   # non-dispatch attended commands
```

`.state/` is already a managed exclusion (`internal/kit/gitignore.go` → `managedIgnores = {".build/", ".local/", ".state/"}`, written into `<kitDir>/.gitignore` by `EnsureGitignore`) and is already outside image payload (the image only ever copies `<kitDir>/image-files/`). So logs are **gitignored and never baked into the VM image** with no new plumbing. Merged VM-side records land in the same file, giving one correlated trace per run. (Retention/rotation is left to the operator for now; noted as future work.)

## 6. Secret-safety boundary

The invariant (AGENTS.md: *secrets never hit logs or disk*) is preserved by **construction**, layered:

1. **Injection paths are never logged, at any level** — the env map built in `dispatchrun` (`envScript`/`writeVM`) and resolved `secret.Spec` values. The `logging` package deliberately offers no affordance to dump an env map; this remains a review-enforced invariant, reinforced by a test (§10).
2. **Only self-constructed records reach the sink.** `at-task` and host records carry fields we own (`step`, `run`, outcome classifications) — secret-free by design. This is what makes "full structured into the VM" safe: the VM ships structured records, not raw text.
3. **Raw agent/VM output stays VM-local.** The agent step's stdout/stderr is captured to a VM-local file (workdir/tmpfs), retrievable at debug or on failure, and is **never shipped to the structured/cloud sink**. Instead, `at-task` emits a structured **agent-outcome record** — `ok` / `needs-input` / `error`, with detectable classifications (`auth_failed` for a `401`, egress-wall for a squid denial; the latter is where [COV-21](https://linear.app/aethons-tools/issue/COV-21) plugs in).
4. **Redaction backstop.** Any raw text that *is* logged locally at debug runs through a **known-secret scrubber** that masks resolved token values — best-effort defense-in-depth, not the primary guarantee.

## 7. Cross-layer correlation & VM→host transport

**Run ID + steps.** The scheduler mints a run ID per dispatch (e.g. `run_<issue>_<short-random>`, unique per dispatch) and seeds the context logger with `run`, `issue`, `class`. Each layer sets `step` as it proceeds:

```
claim → brief → secrets → assemble → vm.create → prepare → agent → complete → broker
```

Every record self-identifies its layer, so a failure names the step that produced it.

**Runner writer injection.** Today `runner.OS` hardwires `cmd.Stdout = os.Stdout` / `cmd.Stderr = os.Stderr` (`internal/runner/runner.go`), so VM output spills raw and undemuxed to the host console. The `Runner` interface is extended so a caller can supply `stdout`/`stderr` `io.Writer`s; `runner.Fake` mirrors the change. `dispatchrun` uses this to **capture** the SSH channel instead of letting it spill.

**Demux by stream.** Inside the VM, `at-task` writes its structured `slog` JSONL to **stderr**; the **agent's** raw output is captured by the step wrapper to a **VM-local file** (§6). The two never intermix on the wire.

**Host merge.** `dispatchrun` reads the captured `at-task` stderr, parses each JSONL record, and **re-emits it through the host logger**, grafting on the run-id/issue/class context — so VM records land in the one unified stream/file with full attribution. Unparseable lines are not dropped; they are logged at debug as raw (scrubbed), tagged with `run`/`step`.

## 8. The dual-output error convention

A helper — `logging.UserError(ctx, w, err, attrs…)` — is the single path for user-facing errors:

- **attended:** prints the human `at-cove: <message>` line to `w` (stderr) for the person, **and** emits a structured `error`/`warn` record (with `run`/`step`/attrs) to the JSON file. Different streams → no double-print.
- **unattended:** emits only the structured record to stderr (JSON). The human line is dropped.

This closes the loop on the motivating bug:

- The `ANTHROPIC_AUTH_TOKEN`-unresolved case (today a warn-and-continue that scrolls past) becomes a structured `warn` `{step:secrets, secret:ANTHROPIC_AUTH_TOKEN, kit:…}` — **and** the work path is made to **fail closed with attribution** when the agent bearer is unresolved (aborting pre-VM, naming the secret and kit, as the git/tracker well-known secrets already do), rather than launching a doomed credential-less run.
- The agent `401` becomes `error {step:agent, class:auth_failed, run:…}` — self-attributing.

## 9. Architecture summary

```
internal/logging/            # new, self-contained
  New(opts) *slog.Logger     # handler + mode selection (attended/unattended, TTY auto-detect + override)
  tee handler                # fan-out: text→stderr @info+, json→file @debug+ (attended)
  Into/From(ctx)             # context propagation
  UserError(ctx,w,err,attrs) # dual-output convention
  scrub(text) string         # known-secret redaction backstop

internal/runner/             # extended: stdout/stderr writer injection (+ Fake)
internal/dispatch/scheduler/ # engine: run-id mint, step attrs, context logger (replaces log.Logger)
internal/dispatchrun/        # VM capture + JSONL demux + host merge; step attrs
cmd/at-cove/                 # cli.Globals gains LogMode + --no-log-file; wire logging.New at entry
cmd/at-task/                 # adopts slog → structured JSONL to stderr, step attrs, secret-free
```

Each unit has one purpose and a well-defined interface: `logging` owns handler/mode wiring (no other package touches it); the runner owns process stdio; `dispatchrun` owns the VM capture/merge; the scheduler owns run-id/step semantics.

## 10. Testing

Hermetic by default (repo ethos — drive fakes, no Docker/network/live VM):

- **`logging`:** buffer handlers assert structured fields, per-destination levels, mode resolution (attended vs unattended), and that **stdout stays clean**; the scrubber masks a known token.
- **Runner:** `Fake` captures injected writers; assert host callers can redirect VM output.
- **Scheduler / dispatchrun:** `fakeTracker`/`fakeExecutor` assert `run`/`step` attribution on emitted records; a **JSONL fixture** fed through the fake exercises the VM-record merge (correct context grafted, unparseable lines preserved at debug).
- **Secret-safety:** a dedicated test feeds a known resolved token through the resolution + dispatch path and asserts the value **never appears** in any emitted record or the log file.
- **Fail-closed:** the work path aborts pre-VM with an attributed error when `ANTHROPIC_AUTH_TOKEN` is unresolved.
- Real-ssh transport tests stay behind the `integration` build tag.

## 11. Rollout (independently-mergeable increments)

Each increment is ~one PR that keeps the tree green. Dependency shape: **#1** and **#2** are independent roots → **#3** → **#4/#5** → **#6**. #1–#3 already deliver the immediate diagnosability win.

1. **`internal/logging` core** — package, mode resolution (TTY + override), tee handler, `Into`/`From`, `UserError`, scrubber; `cli.Globals` gains `LogMode` + `--no-log-file`. No caller behavior change yet. Tests per §10.
2. **Runner writer injection** — extend `runner.Runner` + `Fake`. Pure plumbing. (Parallel with #1.)
3. **Host adoption + the bug fix** — migrate the scheduler logger and `engine.go` sites to the context logger; thread run-id + `step`; convert high-value `stderr` sites to structured + `UserError`; **fail closed with attribution on unresolved `ANTHROPIC_AUTH_TOKEN`**. Resolves the live 401 pain.
4. **VM capture + demux + merge** — `dispatchrun` captures the SSH channel (via #2), parses `at-task` JSONL, re-emits with context; agent raw output → scrubbed VM-local file; structured agent-outcome record with `auth_failed` / egress-wall classification hooks.
5. **`at-task` adopts `slog`** — structured JSONL to stderr with `step` attrs, secret-free. Lands with/after #4.
6. **Migrate remaining sites + docs** — convert remaining diagnostic `Fprintf` sites; leave pure-UX prints as plain output. New `docs/` observability doc + INDEX row; AGENTS.md note on the secrets-never-in-logs invariant and the attended/unattended model.

## 12. Future work (not in this design)

- Log retention / rotation for `<kitDir>/.state/logs/`.
- A cloud log-shipping integration (unattended mode already emits JSON to stderr for a platform to capture; a dedicated sink/exporter is separate).
- Extending structured logging to `at-mint` and `connect` beyond the high-value diagnostic sites migrated in #6.
