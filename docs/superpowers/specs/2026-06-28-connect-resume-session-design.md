# `connect` resumes the last Claude Code session

## Goal

`at-cove connect` should return the user to the Claude Code session they were last in,
both on an ordinary reconnect and on the first connect after a `recreate`.
Resuming is the default;
`--fresh` opts out and starts a new session.

## Key facts that shape the design

- Claude Code resumes per working directory:
  `claude --continue` reopens the most-recent session for the current directory,
  non-interactively, with no session id required.
  `connect` always launches from a fixed `cd /home/agent/workspace`,
  so "most recent for this directory" is exactly "the session the user was last in".
- Session transcripts live at `/agent-data/projects/<dir>/<session>.jsonl`
  (the sandbox sets `CLAUDE_CONFIG_DIR=/agent-data`).
  `/agent-data` is a named Docker volume that `recreate` never deletes,
  so transcripts survive a destroy+recreate.
- `recreate` does not launch `claude` —
  it is a container-lifecycle command (destroy + create).
  The user runs `connect` afterward.

Together these mean the feature needs **no exit hook, no session capture, and no state file**:
`--continue` plus the already-persistent volume deliver resume-after-recreate for free.
The only work is in the `connect` launch command.

## Scope

In scope:
- A `--fresh` flag on `at-cove connect`.
- Resuming by default when a prior session exists.

Out of scope (confirmed):
- `recreate` is unchanged.
  It still does not open a session itself;
  resume happens on the next `connect`.
- `--raw` (debug bash) never resumes.
- No session-id capture, no `SessionEnd` hook, no `--resume <id>`.

## Design

### Resume decision

`main` computes a single boolean: `resume := !fresh && !raw`.
`--raw` launches bash (resume is meaningless);
`--fresh` forces a brand-new `claude` session even if one exists.

### Launch snippet (the only behavioral change)

The remote command that `connect`'s transport exec's is extended so the
`claude` launch becomes resume-aware.
The three cases:

- bash (`--raw`): `exec bash`
- claude, not resuming (`--fresh`): `exec claude`
- claude, resuming (default): a guard that resumes only when a transcript exists,
  otherwise starts fresh:

```sh
if ls "${CLAUDE_CONFIG_DIR:-/agent-data}"/projects/*/*.jsonl >/dev/null 2>&1; then exec claude --continue; else exec claude; fi
```

All three are wrapped as today with `cd /home/agent/workspace && …`.

Two deliberate choices:

- **The `if … fi` guard, not an unconditional `claude --continue`.**
  Claude Code's behavior when no session exists yet (first-ever connect) is undocumented
  and may error.
  The guard makes first-run deterministic:
  resume when a transcript exists, otherwise start fresh.
- **Detection tests for any session under `projects/*/*.jsonl`,
  not a replica of Claude Code's per-directory folder-name hash.**
  That hash is an internal format the docs warn against depending on.
  In this sandbox the only project directory that ever exists is the workspace
  (cwd is always `/home/agent/workspace`),
  so "any session exists" is equivalent and robust to Claude Code changing its naming.
  The `${CLAUDE_CONFIG_DIR:-/agent-data}` fallback keeps detection working
  even if the variable is absent from the shell, since the path is fixed by the image.

### Components and files

- `internal/connect/transport.go`:
  the launch-command builder takes the resume flag.
  `remoteExec(cmd string, resume bool)` wraps a new `launchProgram(cmd, resume)` helper
  that returns one of the three tails above.
  `StdinScript` and `SendEnv` each gain a `Resume bool` field,
  read in their `Launch` methods.
  The zero value is `false` (fresh), so existing call sites and tests are unaffected.
- `main.go`:
  parse `--fresh` for the `connect` subcommand;
  thread a `fresh bool` into `doConnect`;
  set `Resume: !raw && !fresh` on the constructed transport;
  extend the help text and the `--dry-run` line to state whether the session will resume.
- `internal/connect/connect.go`: unchanged.
  The launch program is the transport's responsibility, not `Connect`'s.
- `recreate`, backend, volumes: unchanged.

### Data flow

```
connect (--fresh?) ─► main: resume = !raw && !fresh
  └─ resolve secrets ─► dial ─► ensureAuthenticated   (all unchanged)
       transport.Launch builds the remote string:
         cd /home/agent/workspace && <launchProgram(cmd, resume)>
       interactive PTY ssh ─► claude --continue  (or fresh claude, or bash)
```

## Error handling

Resume detection is best-effort.
If the projects directory is missing, empty, or unreadable,
the `ls` test fails and the snippet runs `exec claude` (a fresh session) —
it never blocks or fails the connect.
A `--fresh` connect bypasses detection entirely.

## Testing

`internal/connect/transport_test.go`:
- `Resume: true` + claude (both transports):
  the remote command contains the guard,
  `exec claude --continue`, and the `else exec claude` fallback.
- `Resume: false` + claude:
  the remote command ends in `exec claude` with no `--continue`
  (the existing tests already cover this as the zero-value case).
- `--raw` (`Cmd: "bash"`):
  the remote command ends in `exec bash` regardless of `Resume`.

`main_test.go`:
- default `connect` (dry-run) reports a resuming session and would set `Resume: true`.
- `connect --fresh` (dry-run) reports a fresh session and would set `Resume: false`.
- `connect --raw` does not resume.
