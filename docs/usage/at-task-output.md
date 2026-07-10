---
summary: at-task output contract — the JSON Schema and examples for .at-task/task-result.json, at-task's authoritative outcome (ok / needs-input / error) with the echoed worker-result.
read_when: You are consuming at-task's task-result.json — brokering its status to a tracker, or troubleshooting a completed run.
owns: the JSON Schema for at-task's task-result.json (the output)
prereqs: at-task.md — the CLI and file-format conventions; at-task-inputs.md — the echoed worker-result schema
tier: leaf
updated: 2026-07-10
---

# at-task output

`at-task complete` writes **`.at-task/task-result.json`** — the authoritative outcome of the
run. Its serialization follows the `task` file (see
[at-task usage → File format](at-task.md#file-format)).

## `.at-task/task-result.json` — The Task Report

A top-level `status` — exactly one of `ok` / `needs-input` / `error` — plus the worker's
`worker-result.json` (schema in [at-task inputs](at-task-inputs.md)) echoed, except when a
failure occurred before the worker ran. Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["status"],
  "properties": {
    "status": {
      "type": "object",
      "description": "exactly one of ok / needs-input / error",
      "minProperties": 1,
      "maxProperties": 1,
      "additionalProperties": false,
      "properties": {
        "ok": {
          "type": "object",
          "description": "the task succeeded (PR/MR opened when the worker proposed one)",
          "additionalProperties": false,
          "properties": {
            "message": { "type": "string", "description": "human-readable summary" },
            "pr-url":  { "type": "string", "description": "opened (or existing) PR/MR URL, when one was opened" }
          }
        },
        "needs-input": {
          "type": "object",
          "description": "the worker stopped for a human; at-task pushed the WIP branch",
          "additionalProperties": false,
          "required": ["message", "commit"],
          "properties": {
            "message": { "type": "string", "description": "human-readable summary of the handoff" },
            "commit":  { "type": "string", "description": "SHA of the pushed WIP commit — the retrace point, stable even if the branch later moves" }
          }
        },
        "error": {
          "type": "object",
          "description": "the task failed — in the worker, or in at-task itself",
          "additionalProperties": false,
          "required": ["message"],
          "properties": {
            "message": { "type": "string", "description": "what went wrong" },
            "detail":  { "type": "string", "description": "optional diagnostic detail, e.g. command output" }
          }
        }
      }
    },
    "worker-result": {
      "description": "the worker's worker-result.json (schema in at-task-inputs.md), echoed; omitted when the failure occurred before the worker ran"
    }
  }
}
```

### Example: Worker finished successfully; pull request opened
```json
{ 
  "status": {
    "ok": {
      "message": "Opened PR #42",
      "pr-url": "https://github.com/aethons-tools/loom/pull/42"
    }
  },
  "worker-result": {
    "status": {
      "ok": {
        "pull-request": {
          "title": "AET-2026 Add X",
          "message": "Implements X by …" 
        }
      }
    }
  } 
}
```

### Example: Worker needed input and stopped
```json
{ 
  "status": {
    "needs-input": {
      "message": "Added information to AET-2026 and moved to `Needs Input` status",
      "commit": "1a2b3c4"
    }
  },
  "worker-result": { 
    "status": {
      "needs-input": { 
        "doing": "…",
        "blocker": "…",
        "need": "…",
        "tried": "…"
      } 
    }
  } 
}
```

### Example: Worker reported an error
```json
{
  "status": {
    "error": {
      "message": "Worker could not execute task: \"My system prompt prevents me from executing this class of task\""
    }
  },
  "worker-result": {
    "status": {
      "error": {
        "message": "My system prompt prevents me from executing this class of task"
      }
    }
  }
}
```

### Example: An error occurred outside of the worker
```json
{
  "status": {
    "error": {
      "message": "Unable to prepare the workspace for the task",
      "detail": "`git pull` returned:\nPermission denied."
    }
  }
}
```
