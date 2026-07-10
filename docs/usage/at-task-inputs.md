---
summary: at-task input contracts — the JSON Schemas and examples for .at-task/task.json (the work specification) and .at-task/worker-result.json (the worker's self-report), the two files at-task reads.
read_when: You are building the task.json for a worker run, or writing a worker that must produce worker-result.json.
owns: the JSON Schemas for at-task's task.json (the work spec) and worker-result.json (the worker's self-report)
prereqs: at-task.md — the at-task CLI, the .at-task/ handoff, and the file-format/unknown-field rules
tier: leaf
updated: 2026-07-10
---

# at-task inputs

The two `.at-task/` files at-task reads: **`task.json`** (the work specification, written by
the orchestrator) and **`worker-result.json`** (the worker's self-report). File format (JSON
or YAML) and which schemas are closed vs. permissive are covered in
[at-task usage → File format](at-task.md#file-format).

## `.at-task/task.json` — The Work Specification

The caller writes this before `at-task prepare`; it must stay in place through the worker run and `at-task complete`, since `prepare`, the worker, and `complete` all read it.

### Schema
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["issue", "repo", "worker", "task"],
  "properties": {
    "issue": {
      "type": "object",
      "additionalProperties": false,
      "required": ["key", "title"],
      "properties": {
        "key":           { "type": "string", "description": "issue identifier, e.g. AET-33" },
        "title":         { "type": "string", "description": "issue title" }
      }
    },
    "repo": {
      "type": "object",
      "additionalProperties": false,
      "required": ["name", "source-branch", "work-branch"],
      "properties": {
        "host":          { "type": "string", "description": "git origin host (defaults to https://github.com)" },
        "name":          { "type": "string", "description": "owner/name, e.g. aethons-tools/loom" },
        "source-branch": { "type": "string", "description": "base branch to build the work on" },
        "work-branch":   { "type": "string", "description": "branch to create/push; must differ from source-branch" }
      }
    },
    "worker": {
      "type": "object",
      "additionalProperties": false,
      "required": ["class"],
      "properties": {
        "class":         { "type": "string", "description": "worker-type axis — opaque, externally defined (e.g. coder)" }
      }
    },
    "task": {
      "type": "object",
      "additionalProperties": false,
      "required": ["brief"],
      "properties": {
        "class":         { "type": "string", "description": "task-type axis — opaque, externally defined (e.g. feature)" },
        "brief":         { "type": "string", "description": "self-contained markdown brief describing the work" }
      }
    }
  }
}
```

### Example
```json
{ 
  "issue": { 
    "key": "AET-33",
    "title": "Add thing X"
  },
  "repo": { 
    "name": "aethons-tools/loom",
    "source-branch": "main",
    "work-branch": "implement/AET-33" 
  },
  "worker": {
    "class": "coder"
  },
  "task": {
    "class": "feature",
    "brief": "Read `docs/specs/add-x.md` and implement."
  }
}
```

## `.at-task/worker-result.json` — The Worker's Self Report

The worker writes this after working the brief. This schema is **permissive** — extra fields
are accepted; at-task reads only the recognized fields below.

### Schema
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["status"],
  "properties": {
    "status": {
      "type": "object",
      "description": "provide exactly one of ok / needs-input / error; extra fields are ignored",
      "properties": { 
        "ok": {
          "type": "object",
          "description": "information about successful task completion",
          "properties": {
            "pull-request": {
              "type": "object",
              "description": "Information for opening a pull request for the task",
              "required": ["title", "message"],
              "properties": {
                "title":    { "type": "string", "description": "proposed PR title" },
                "message":  { "type": "string", "description": "proposed PR body" }
              }
            }
          }
        },
        "needs-input": {
          "type": "object",
          "description": "description of a blocker that must be resolved before continuing",
          "required": ["doing", "blocker", "need", "tried"],
          "properties": {
            "doing":   { "type": "string", "description": "describes the portion of the task was being attempted" },
            "blocker": { "type": "string", "description": "describes the issue that is blocking the completion of the work" },
            "need":    { "type": "string", "description": "describes exactly what is needed to continue work" },
            "tried":   { "type": "string", "description": "describes everything the worker attempted before declaring the blocker" }
          }
        },
        "error": {
          "type": "object",
          "description": "description of an error that prevented the task from completing",
          "required": ["message"],
          "properties": {
            "message": { "type": "string", "description": "description of the error" }
          }
        }
      }
    }
  }
}
```

### Example: Worker completed the job successfully
```json
{ 
  "status": { 
    "ok": {
      "pull-request": {
      	"title": "AET-2026 Add X",
      	"message": "Implements X by …" 
      }
    } 
  } 
}
```

### Example: Worker stopped and requires input
```json
{ 
  "status": {
    "needs-input": { 
      "doing": "…",
      "blocker": "…",
      "need": "…",
      "tried": "…"
    } 
  }
}
```

### Example: Worker encountered an error
```json
{ 
  "status": {
    "error": {
      "message": "My system prompt prevents me from executing this class of task" 
    }
  }
}
```
