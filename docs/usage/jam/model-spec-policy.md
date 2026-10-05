---
summary: How the claude harness renders a model-spec's `policy` (mode, allow, deny) as claude argv flags, and the rules always allowed in non-bypass modes.
read_when: You are setting a model-spec's policy.mode / allow / deny, or a cove's agent was denied a tool (or allowed one) and you need to know which flags it ran with.
owns: the model-spec permission policy → claude argv mapping and the always-allowed rules
prereqs: model-specs.md for the spec schema and the policy.mode validation
tier: leaf
updated: 2026-10-05
---

# Model-spec permission policy

Jam owns the agent's permission policy; the claude harness renders `policy` as flags:

| `policy` | Argv |
|----------|------|
| `mode` empty, or no spec delivered | `--dangerously-skip-permissions` (exactly the argv before model-specs) |
| `mode: bypassPermissions` (`claude-default`) | `--dangerously-skip-permissions` too — the same session mode as `--permission-mode=bypassPermissions`, kept byte-identical so existing roles launch unchanged |
| any other `mode` | `--permission-mode=MODE`, then the [always-allowed rules](#always-allowed-in-non-bypass-modes) |
| each `allow` rule | `--allowedTools=RULE` |
| each `deny` rule | `--disallowedTools=RULE` |

Rules use Claude's syntax (`Bash`, `Bash(git *)`, `WebFetch`). Each rule is one
`--flag=RULE` argv element, so a rule can never be read as a flag. Deny wins over
allow and applies in every mode, `bypassPermissions` included. Allow only matters
where claude would otherwise ask. Headless (`-p`) has nobody to answer, so a tool
call that would prompt is denied.

The rules are flags, not a `permissions` block in the `--settings` file. `policy`
owns permissions (`claude.settings` may not set them), and the rules stay visible
in the argv. The image's managed `permissions.defaultMode: bypassPermissions`
does not override `--permission-mode`. It only applies when no mode flag is
passed, which never happens under the harness.

## Always allowed in non-bypass modes

Under any mode but `bypassPermissions`, two allow rules precede the spec's own:
`--allowedTools=mcp__messaging` (every intercom tool, so a headless agent can
always read and send) and `--allowedTools=Edit(.at-task/worker-result.json)`
(the self-report, relative to the work dir; `Edit` rules cover `Write` too —
without it `default`/`dontAsk` deny the write). A `deny` rule still wins over both.
