---
summary: at-work usage — the prepare/complete/version CLI, the AT_WORK_GIT_TOKEN credential, the cwd file handoff (.at-work/brief.md out, .at-work/outcome.json in), and the JSON Schemas for input.json, the agent's outcome.json, and output.json.
read_when: You are invoking at-work directly, writing an agent that must produce .at-work/outcome.json, or building the input.json / consuming the output.json for a worker run.
owns: the at-work CLI usage and the JSON Schemas for its input.json, agent outcome.json, and output.json
prereqs: none — for how at-work is dispatched inside a sandbox see ../orchestration/at-cove-dispatch-interface.md
tier: leaf
updated: 2026-07-08
---

# at-work usage

`at-work` turns one unit of work into a pull request. It owns all git and code-host
interaction (clone, branch, commit, push, open PR) **around** an agent run that
something else performs — it never runs the agent. The handoff is a file convention in
the **current directory**: `prepare` writes the brief, the agent writes its outcome,
`complete` reads it. For the design rationale and how it is dispatched in a hardened
sandbox, see the [dispatch interface](../orchestration/at-cove-dispatch-interface.md).

## Commands

All commands operate on the **current working directory** (the shared work dir for the
three steps). One credential env var, used by `prepare`/`complete` only:

```
AT_WORK_GIT_TOKEN   # code-host token for clone/push/PR (never on argv/logs)
```

| Command | Reads | Does | Writes |
|---------|-------|------|--------|
| `at-work prepare <input.json>` | `input.json` | clone (if absent) → sync `source-branch` → create/resume `work-branch` | `.at-work/brief.md` |
| `at-work complete <input.json> <output.json>` | `input.json`, `.at-work/outcome.json` | on `OK`: commit → push → open PR; on `NEEDS_INPUT`: commit WIP → push | `<output.json>` |
| `at-work version` | — | — | prints `at-work <version>` |

Between the two, whatever runs the agent reads `.at-work/brief.md`, edits and tests the
repo, and writes `.at-work/outcome.json`. `complete` **always** writes a valid
`output.json`: a missing or invalid `outcome.json` becomes a structured `ERROR`.

**Exit codes:** `2` bad arguments; `1` a setup/IO failure in `prepare` (or a failed
`output.json` write in `complete`); `0` otherwise. `complete` returns `0` even when the
`output.json` status is `NEEDS_INPUT`/`ERROR` — the status lives in the file.

**Status (top-level `output.json.status`):** `OK` iff `outcome.json` is `OK` **and** a PR
was opened (`work.pr-url` set); `NEEDS_INPUT` iff the outcome says so; otherwise `ERROR`.

## Inputs

### `input.json` — the task (both commands read it)

Unknown fields are ignored. Schema (JSON Schema 2020-12):

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["issue", "repo"],
  "properties": {
    "issue": {
      "type": "object",
      "required": ["key", "title", "work-class", "brief"],
      "properties": {
        "key":        { "type": "string", "description": "issue identifier, e.g. AET-33" },
        "title":      { "type": "string", "description": "used as the PR title" },
        "work-class": { "type": "string", "description": "handler class, e.g. implement" },
        "brief":      { "type": "string", "description": "self-contained markdown brief for the agent" }
      }
    },
    "repo": {
      "type": "object",
      "required": ["name", "source-branch", "work-branch"],
      "properties": {
        "name":          { "type": "string", "description": "owner/name, e.g. aethons-tools/loom" },
        "source-branch": { "type": "string", "description": "base branch to build the work on" },
        "work-branch":   { "type": "string", "description": "branch to create/push; must differ from source-branch" }
      }
    }
  }
}
```

```json
{ "issue": { "key": "AET-33", "title": "Add thing X", "work-class": "implement",
             "brief": "…full markdown brief…" },
  "repo":  { "name": "aethons-tools/loom", "source-branch": "main", "work-branch": "implement/AET-33" } }
```

### `.at-work/outcome.json` — the agent's self-report (`complete` reads it)

The agent writes this after working the brief. Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["status"],
  "properties": {
    "status":     { "enum": ["OK", "NEEDS_INPUT", "ERROR"] },
    "pr-message": { "type": "string", "description": "proposed PR body; used when status = OK" },
    "needs-input": {
      "type": "object",
      "description": "present when status = NEEDS_INPUT",
      "required": ["doing", "blocker", "need", "tried"],
      "properties": {
        "doing":   { "type": "string" },
        "blocker": { "type": "string" },
        "need":    { "type": "string" },
        "tried":   { "type": "string" }
      }
    },
    "message":    { "type": "string", "description": "present when status = ERROR" }
  }
}
```

```json
{ "status": "OK", "pr-message": "Implements X by …" }
{ "status": "NEEDS_INPUT", "needs-input": { "doing": "…", "blocker": "…", "need": "…", "tried": "…" } }
{ "status": "ERROR", "message": "…" }
```

## Output

### `output.json` — written by `complete`

A top-level authoritative `status`, an optional `message`, the `agent` block (the
outcome above, echoed), and a `work` block describing what at-work did. Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["status", "work"],
  "properties": {
    "status":  { "enum": ["OK", "NEEDS_INPUT", "ERROR"], "description": "authoritative" },
    "message": { "type": "string" },
    "agent":   { "description": "the agent's outcome.json (schema above); omitted if none" },
    "work": {
      "type": "object",
      "properties": {
        "branch":     { "type": "string", "description": "the work-branch" },
        "pr-url":     { "type": "string", "description": "opened/existing PR (OK only)" },
        "safe-state": { "type": "string", "description": "<work-branch> @ <sha> (NEEDS_INPUT)" },
        "error":      { "type": "string", "description": "diagnostic (ERROR)" }
      }
    }
  }
}
```

```json
// OK — agent finished; at-work pushed + opened the PR
{ "status": "OK", "message": "opened PR #42",
  "agent": { "status": "OK", "pr-message": "…" },
  "work":  { "branch": "implement/AET-33", "pr-url": "https://github.com/aethons-tools/loom/pull/42" } }

// NEEDS_INPUT — agent stopped; at-work pushed the WIP branch, no PR
{ "status": "NEEDS_INPUT",
  "agent": { "status": "NEEDS_INPUT", "needs-input": { "doing": "…", "blocker": "…", "need": "…", "tried": "…" } },
  "work":  { "branch": "implement/AET-33", "safe-state": "implement/AET-33 @ 1a2b3c4" } }

// ERROR — no/invalid outcome, or a mechanical failure
{ "status": "ERROR", "message": "no agent outcome",
  "work":  { "branch": "implement/AET-33" } }
```
