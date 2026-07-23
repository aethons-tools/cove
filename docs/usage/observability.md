---
summary: The operator-facing observability model for the cove binaries — how the attended/unattended output modes are chosen, where logs land, how a single run is correlated across processes and the VM, and the secrets-never-in-logs invariant.
read_when: You are operating `at-cove dispatch`/`work` (or `at-task`) and need to read, capture, or ship its logs — choosing a mode, finding the log file, grepping one run out of interleaved output, or confirming that secrets never reach a log.
owns: the operational observability model — output-mode selection + flags, log-file locations, the run/issue/class/step correlation contract, and the secrets-never-in-logs operational guarantee
prereqs: none — see ../OVERVIEW.md for what the project is
tier: leaf
updated: 2026-07-20
---

# Observability: logs, modes, and correlation

`at-cove` and `at-task` emit **structured `slog` logs** on **stderr** (stdout stays
clean for command data). Diagnostics and errors are records you can grep and ship;
one dispatch run is correlated end-to-end — host scheduler → `at-cove work` → the
VM's `at-task` — by a shared `run` id plus `issue`/`class`/`step` attributes. The
authoritative *design* lives in
[the structured-logging design](../superpowers/specs/2026-07-15-structured-logging-design.md);
this doc is the operator's *how it behaves* reference.

## Two output modes

The consumer, not a format flag, picks the mode — resolved by TTY auto-detect on
stderr and overridable:

| Mode | Selected when | stderr | file |
|---|---|---|---|
| **attended** | stderr is a terminal, or `--log-mode attended` | human-friendly **text** at `info+` | full **JSON** trace at `debug+` (unless `--no-log-file`) |
| **unattended** | stderr is not a terminal (headless/service), or `--log-mode unattended` | **JSON** at the configured level | — (none) |

- `--log-mode attended|unattended` (default: auto-detect) forces the mode.
- `--log-level debug|info|warn|error` (default `info`) sets the **stderr** level;
  the attended file is always `debug+`.
- `--no-log-file` suppresses the attended-mode file.
- Env fallbacks `AT_LOG_MODE` / `AT_LOG_LEVEL` apply when the flag is omitted.

These are **global** flags — they go *before* the subcommand. They are wired into a
per-run logger by `work` and `dispatch`; the other (interactive) commands print
plain CLI output (see [the boundary](#what-is-and-isnt-a-log) below).

The normal deployment is **unattended**: `at-cove dispatch` runs as a headless
service, writes JSON to stderr, and the platform capturing stderr is the log sink.
No log-shipping agent is built into cove — unattended mode just emits JSON for
whatever captures the process's stderr.

## Where logs land

Attended-mode JSON files live under the kit's **excluded runtime dir**
`<kitDir>/.state/logs/` (already gitignored and never image payload):

```
<kitDir>/.state/logs/at-cove-dispatch.jsonl        # the dispatch scheduler
<kitDir>/.state/logs/at-cove-work-<issue>.jsonl     # one file per work run, keyed by issue
```

The `<issue>` slug is sanitized (only `[A-Za-z0-9_-]`) before it names a file, since
it originates in task input. `--reap` (a maintenance scavenge, no task) writes no
per-run file. Retention/rotation is left to the operator.

## Correlating one run

Every dispatch mints a **`run` id** (`run_<issue>_<short-random>`) and seeds its
logger with `run`, `issue`, and `class`. Each layer sets a **`step`** as it proceeds
— `setup` (pre-dispatch: backend lookup, install-currency, key-ensure — it never
assembles; assembly is `install`'s job), `secrets`, `prepare`, `agent`, `complete`,
`broker`; the scheduler adds `claim`, `brief`, `dispatch`, `poll` (listing ready
work), and `reap` (the stale-claim reaper) — so a record names the layer that
produced it. To pull one dispatch out of interleaved concurrent output, grep on its
`run` id (or filter by `issue`).

The scheduler passes the run id into the `work` subprocess via the **`COVE_RUN_ID`**
env var, and `work` re-binds it, so a dispatched worker's own records — and the VM
records it merges — join the same trace. The VM-side capture/demux/merge and the
`auth_failed`/egress-wall agent-outcome classification are summarized in
[the overview](../OVERVIEW.md#command-surface); the full model is in the design spec.

## The dual-output error convention

User-facing errors flow through one helper (`logging.UserError`) so a person and a
machine each get the right thing without a double-print:

- **attended:** a human `at-cove: <message>` line on stderr **and** a structured
  `error` record in the JSON file (different streams → shown once).
- **unattended:** only the structured `error` record on stderr; the human line is
  dropped (nobody is watching a terminal).

## Secrets never reach a log

This is a **structural** invariant, not best-effort (it mirrors the project-wide
[*secrets never touch disk or the host process table*](../OVERVIEW.md#secret-injection-the-chat-data-flow)
rule):

1. **Injection paths are never logged, at any level** — resolved secret values and
   the VM env map. The `logging` package offers no affordance to dump them; this is
   review-enforced and covered by a test.
2. **Only self-constructed records reach the sink** — host and `at-task` records
   carry fields we own (`step`, `run`, outcome classifications), secret-free by
   design. This is what makes shipping VM records safe.
3. **Raw agent/VM output stays VM-local** — the agent step's output is teed to a
   VM-local file, never shipped to the structured sink; only a classified
   agent-outcome record leaves the VM.
4. **Redaction backstop** — any raw text logged locally at debug runs through a
   known-secret scrubber. Defense-in-depth, not the primary guarantee.

## What is (and isn't) a log

Only **diagnostics and errors** are structured. **Pure-UX output stays plain** and
on the right stream: help/usage text, `--dry-run` intent lines, `status` output, and
`install`/`uninstall` summaries are command *data* on stdout. The operational
commands (`work`, `dispatch`) route their diagnostics through the structured logger;
the interactive lifecycle commands (`create`/`chat`/`destroy`/…), `connect`, and
`at-mint` still print plain diagnostics — extending structured logging to them
beyond the high-value operational sites is deferred (design spec §12).
