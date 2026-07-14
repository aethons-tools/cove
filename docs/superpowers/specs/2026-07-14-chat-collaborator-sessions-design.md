# `chat` collaborator sessions — Design

**Date:** 2026-07-14
**Status:** Proposed (pre-implementation)
**Repo:** `github.com/aethons-tools/cove` (binaries `at-cove`, `at-task`, `at-mint`)
**Builds on:** the unified kit config (`2026-07-10-unified-kit-config-design.md`, which introduced the schema-only `collaborators:` tree), the demand/supply secret model (`2026-07-14-at-mint-minter-design.md`), and the Linear dispatch flow (`docs/orchestration/linear-agent-workflow.md`).

## 1. Purpose

Make an **interactive collaborator session** a first-class, kit-defined thing. Today `at-cove connect` launches a bare `claude` with no role, no board awareness, and no notion of "plan-and-dispatch vs. implement." The `collaborators:` config tree is parsed but wired to nothing.

The target workflow: a human opens a session to **groom and plan the board** — turn an idea into well-formed Linear issues and decompose them into dispatchable sub-issues — and lets **dispatched workers do the implementation**. The one exception is **review/troubleshooting**, where the same session may make direct fixes. The session already has live access to Linear and GitHub through the human's **claude.ai account connectors** (subscription OAuth, which the interactive path uses); what it lacks is (a) a *role* (which board activity, and the plan-vs-implement boundary), (b) the *procedure* for board work, and (c) the *parameters* of this repo's board.

This spec covers **(A) the `chat`/collaborator mechanism** in `at-cove`. It also sketches **(B) the `board-*` skills** (the procedure) as an appendix; those are skill *content*, best refined by dogfooding once (A) exists, and are not part of (A)'s implementation plan.

## 2. Governing decisions

- **`connect` → `chat` (hard rename).** The interactive-session command is renamed `chat`; `connect` is **removed** — no alias (single-user tool). Everything about the session (OAuth login via `--claudeai`, host-shared `credentials.json`, resume/`--raw`/`--fresh`/`--no-auth`) is unchanged.
- **`kit-dir` becomes a `--kit-dir` flag, standardized across all commands.** The kit directory stops being a positional and becomes an optional `--kit-dir DIR` flag on every command that takes one (default: the current cwd/single-kit resolution, unchanged). This frees the leading positional for `chat`'s collaborator key *and* makes the whole CLI uniform. The shared kit-dir resolution is the single change point; each command swaps its positional for the flag.
- **A collaborator is a selectable, defaulted class.** `at-cove chat [collaborator]` takes an optional leading positional naming a `collaborators:` class. Omitted → the sole defined collaborator; if several, the one marked `default: true`; if several and none marked, a usage error listing them. `<common>` is never selectable.
- **A collaborator carries a role prompt, not (necessarily) secrets.** Because GitHub and Linear ride the human's connectors, most collaborators need no secrets. The new load-bearing field is `prompt:`. The `secrets:` bucket (with `<common>` merge) stays for the occasional kit that wants an extra scoped token.
- **The role is delivered as injected context, not `-p`.** Interactive `claude` has no headless prompt. `chat` writes the selected collaborator's `prompt:` into a persistent context file that the session's `CLAUDE.md` includes — the same in-memory-over-ssh delivery path secrets already use. No per-collaborator image rebuild (that stays a deferred "eventually").
- **"Knows our Linear structure" needs no new plumbing.** The knowledge is three parts, two of which already exist: **procedure** = the `board-*` skills (appendix B); **parameters** = the kit's own `.at-cove/config.yml` `tracker.linear` block (team, `states`, `class-label-prefix`) that the session is sitting inside; **live data** = the Linear/GitHub connectors. The collaborator `prompt:` binds them and states the boundary: *planning emits Linear issues; only review/troubleshooting may implement in place.*
- **Collaborators use connectors; workers use minted tokens.** This is the clean split: an interactive collaborator operates through the human's account connectors (Linear + GitHub) — no `AT_TASK_GIT_TOKEN`, no `at-mint` on the collaborator path. The minted-token air-gap is a worker concern only.
- **No new dependencies.** Standard library + `gopkg.in/yaml.v3`. Hermetic tests via `runner.Fake`.

## 3. The `chat` command

```
at-cove chat [collaborator] [--kit-dir DIR] [flags]   # collaborator: optional class name
```

The kit/sandbox is resolved via the standardized `--kit-dir` flag (default: cwd/single-kit resolution as today). The **only** positional is the optional collaborator class, validated against the kit's `collaborators:` — no positional ambiguity, since the kit dir is now a flag:

- **explicit** — must match a defined non-`<common>` class, else a usage error.
- **omitted, one collaborator** — use it.
- **omitted, several** — use the `default: true` one; if none is marked, a usage error: `chat: multiple collaborators; specify one of: triager, reviewer`.
- **omitted, none defined** — launch a plain session (today's behavior, no role injected).

Flags (`--kit-dir`, `--raw`, `--fresh`, `--no-auth`, `--dry-run`) are as described. `dry-run` reports the resolved collaborator + whether a role prompt would be injected, and returns before touching the backend.

## 4. Schema — `Collaborator` gains `prompt` + `default`

```go
type Collaborator struct {
	Prompt  string                  `yaml:"prompt,omitempty"`  // role injected as session context
	Default bool                    `yaml:"default,omitempty"` // the default when the key is omitted
	Secrets map[string]SecretConfig `yaml:"secrets,omitempty"`
}
```

```yaml
# .at-cove/config.yml
collaborators:
  <common>:
    secrets: { }                 # merged into every collaborator (rarely needed now)
  triager:
    default: true
    prompt: |
      You are the board steward for this repo. Turn ideas into well-formed Linear
      issues and decompose them into dispatchable sub-issues (use board-intake /
      board-plan). Read .at-cove/config.yml `tracker.linear` for the team, states
      and class label prefix; use the Linear connector for live data. PLAN — do not
      implement: emit issues, let dispatched workers build them. The one exception:
      during review or troubleshooting you MAY make direct fixes (via the GitHub
      connector).
```

Validation (in `ParseConfig`, alongside the existing `validateClassTree` + `rejectReservedSecretNames`): **at most one collaborator may set `default: true`**; `<common>` may not set `prompt`/`default` (it is a base, not a role). `ResolvedCollaborator` continues to merge `<common>` secrets (own wins) and now also returns the resolved `prompt`.

## 5. `chat` wiring

`chat` (a thin evolution of `doConnect`):

1. Load the recorded **state** (as today) *and* the **kit config** (`kit.Load(kitDir)`) — the latter is the source of the `collaborators:` tree. A **malformed or absent kit config is a hard error** (abort before connecting) — `chat` is a kit-aware command, unlike the old state-only `connect`.
2. **Select** the collaborator per §3; resolve it via `ResolvedCollaborator` (`<common>`-merged secrets + prompt).
3. **Resolve secrets**: the existing state/root secrets (unchanged) *plus* the collaborator's `secrets:` (demand/supply via `Store.Plan` + `mint.Expander`, exactly as `work`/`connect` already resolve). Injected into the session env in memory as today.
4. **Inject the role**: write the resolved `prompt:` to the collaborator context file in the VM (§6). A plain session (no collaborator) writes the empty default.
5. Launch the interactive session exactly as the old `connect` did.

A kit that defines **no** collaborators yields a plain session (today's exact behavior), so the rename is behavior-preserving for kits that never adopt a collaborator.

## 6. Role injection mechanism

The session's global `CLAUDE.md` (in `CLAUDE_CONFIG_DIR=/agent-data`, seeded from `.init-agent-data/CLAUDE.md`) gains an include of an optional collaborator role file, and a **default empty file ships in the image**, so the include always resolves (workers, plain sessions). Both the `@COLLABORATOR.md` include line and the default `COLLABORATOR.md` live in the **hardening (sealed) layer** — the role-injection scaffold is part of the security baseline, so a kit can neither remove the include nor hijack the seeded default; `chat` only overwrites the runtime copy in the agent-writable `/agent-data`:

```
# .init-agent-data/CLAUDE.md
@PROGRESSIVE_DISCLOSURE.md
@SANDBOX.md
@COLLABORATOR.md            # role, injected by `chat`; empty by default
```

`chat` overwrites `/agent-data/COLLABORATOR.md` with the selected collaborator's `prompt:` (or a benign `# (no collaborator role active)` placeholder) before launching — written over ssh under `umask 077`, the same path secrets take, though the prompt is **not** secret (it is source-controlled kit content). On resume, `chat` rewrites it, so the active role always matches the invocation.

The default `COLLABORATOR.md` MUST exist in the built image so a non-`chat` session's `CLAUDE.md` include never dangles.

## 7. What this does *not* build (scope fence)

- **The `board-*` skills' content** — appendix B; drafted and refined separately.
- **Per-collaborator image tweaks** — deferred; roles are prompts for now.
- **Wiring the scheduler to "assign" interactive classes** — the `linear-agent-workflow.md` vision of the scheduler routing `spec`/`review` issues to a human is out of scope; `chat` is human-initiated. (A collaborator does not need a `class:` label to exist.)
- **Any collaborator credential minting** — collaborators use connectors, full stop.

## 8. Component changes (summary)

- **Shared kit-dir resolution** (`cmd/at-cove` + wherever the current positional `[kit-dir]` is parsed) — replace the positional with a `--kit-dir DIR` flag registered uniformly on every command that takes a kit dir (default: today's cwd/single resolution). One helper change + per-command flag registration.
- `internal/kit/config.go` — `Collaborator` gains `Prompt`/`Default`; validate at-most-one-default and `<common>` role-free; `ResolvedCollaborator` returns the prompt.
- `cmd/at-cove/main.go` — rename `connect` → `chat` (no alias); `doChat` = the old `doConnect` + kit-config load (hard-error on failure) + collaborator selection + prompt injection; usage/help.
- `internal/connect/connect.go` — `Options` gains `CollaboratorPrompt string` (+ the VM path to write it); `Connect` writes the role file before launch (writes the empty placeholder when unset).
- `internal/assemble/hardening/image-files` — add the `@COLLABORATOR.md` include to the seeded `CLAUDE.md` and ship a default empty `COLLABORATOR.md` (both in the sealed hardening layer).
- Docs — `docs/usage/at-cove-config.md` (`collaborators.prompt`/`default`), a `chat` usage doc (rename from any `connect` usage), `docs/OVERVIEW.md` (the command), and `docs/orchestration/` (the collaborator session's plan-vs-implement boundary).

## 9. Testing

Hermetic (`runner.Fake`), no VM:
- **kit**: `Collaborator` parse (`prompt`/`default`); at-most-one-default rejected; `<common>` with a role rejected; `ResolvedCollaborator` merges `<common>` secrets and returns the prompt.
- **chat selection**: explicit match; explicit miss → error; omitted+sole → that one; omitted+several+default → the default; omitted+several+no-default → error listing them; omitted+none → plain session (no role file content).
- **chat wiring**: the resolved collaborator's `<common>`-merged secrets are resolved and injected; the role file is written with the resolved prompt (and the empty placeholder for a plain/no-collaborator session); a **malformed/absent kit config hard-errors** before connecting.
- **kit-dir flag**: `--kit-dir DIR` resolves the kit on each command; omitted → the same default as today's positional; the old positional is gone (a stray positional on, e.g., `work` is now a usage error / the collaborator on `chat`).
- **assemble**: the built (hardening-layer) image contains a `COLLABORATOR.md` (so the `CLAUDE.md` include never dangles) and the seeded `CLAUDE.md` includes it.

## 10. Appendix B — the `board-*` skills (sketch, not in this plan)

Three skills already scaffolded (empty) in the image at `.init-agent-data/skills/`. They encode the *procedure*; each reads the kit's `tracker.linear` config for parameters and drives Linear via the connector.

- **`board-intake`** — idea/request → **one well-formed Linear issue**: right team, `ready` state, the `class:<name>` label for the intended handler, and the house task shape (a crisp "what to do", explicit constraints, a definition-of-done that ends in a PR against `main`).
- **`board-plan`** — an issue → a **tree of dispatchable sub-issues**, each sized for a single worker run (one PR's worth), correctly labeled/stated so the scheduler will pick them up. This is the "→ Linear, don't implement here" exit ramp of the superpowers flow.
- **`board-execute`** — the in-bracket **implement** path a dispatched worker runs; largely documents what `at-cove work` already does (prepare → agent → complete), plus the review/troubleshoot-fix variant a collaborator may invoke directly.

These will be drafted and then sharpened live in a real `chat` session, and are tracked as follow-on work — not part of the (A) implementation plan.

## 11. Risks / non-goals

- **`chat` reads the kit config (the old `connect` did not) and hard-errors on a bad one.** Slightly stricter than `connect`, deliberately — a collaborator session is kit-defined, so a broken kit should stop rather than silently drop the role.
- **`--kit-dir` is a breaking CLI change** (positional → flag) across all commands. Accepted: single-user tool; it makes the surface uniform and is the enabler for `chat`'s collaborator positional. Muscle memory is the only cost.
- **Global `CLAUDE.md` gains an include for all sessions** (workers too). Guarded by shipping the default empty `COLLABORATOR.md` in the hardening layer so the include never dangles; the empty file is inert context.
- **Non-goals:** scheduler routing of interactive classes; per-collaborator images; collaborator token minting; changing the worker/air-gap paths.

## 12. Decomposition (plan)

One hermetic plan, green throughout:
1. **`--kit-dir` flag standard** — replace the positional kit-dir with a `--kit-dir DIR` flag across all commands (shared resolver + per-command registration); update existing command tests. (Lands first: it's the enabler and is otherwise independent.)
2. `Collaborator` schema (`prompt`/`default`) + validation (at-most-one-default; `<common>` role-free) + `ResolvedCollaborator` prompt.
3. Collaborator **selection** logic (explicit/sole/default/ambiguous/none) as a pure, tested function.
4. `chat` command (hard rename from `connect`, no alias); wire kit-config load (hard-error), selection, secret resolution, and role-file injection into the interactive path.
5. Image payload (hardening layer): `@COLLABORATOR.md` include + default file; assemble test.
6. Docs (`at-cove-config.md`, `chat` usage, OVERVIEW, orchestration boundary, and the `--kit-dir` change across usage docs).
