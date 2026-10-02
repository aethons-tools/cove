# jam: layered session context — a compiled, progressively disclosed bundle per session

**Status:** design approved in chat (approach B, 4 slices), spec under review
**Scope:** replace the raise-time prompt string (`studio.ComposePrompt`) with a **session context bundle**: six authored/generated layers compiled by Jam into a small always-on **core** (delivered as an appended system prompt on every turn) plus on-demand **leaf** files under `/agent-data/context/`, refreshed on every wake.
**Builds on:** [`2026-09-30-studio-kit-standalone-design.md`](2026-09-30-studio-kit-standalone-design.md) §"Session prompt composition" (the layer order this supersedes); [`2026-09-26-session-kinds-standing-personal.md`](2026-09-26-session-kinds-standing-personal.md) (session kinds and their preambles); the per-turn connector refresh (`internal/agentrun/connector.go`, #304); the docs doctrine in `internal/assemble/hardening/image-files/home/agent/.init-agent-data/reference/progressive-disclosure.md`.
**Does not change:** the hardening layer's security mechanisms (egress, nftables, sealed files); secrets handling (no secret value ever enters a bundle); the Launch prompt's role as the first `-p` message; local `at-cove` (non-Jam) sessions except for one neutral rewording of the sealed `SANDBOX.md`.
**Deferred:** a timed self-wake ("end the turn and sleep until T") — not shipped today, so the Boilerplate must not claim it (§ Open follow-ups).

## Why

What a Jam session learns at raise is one string, sent once:

- `Supervisor.Raise` → `studio.ComposePrompt{Kit, Project, Role, Launch}` → the positional `claude -p` argument (via `/dev/shm/cove-agent-prompt`). **Project and Role are always empty** — neither entity has a prose field. There is no Jam layer and no Studio layer.
- The Kit layer is hand-written (`kit.yml` `prompt:`) and drifts: the cove-ic kit claims "everything you need for aethons.tools/cove" in sessions of other projects.
- The image's sealed `CLAUDE.md` → `SANDBOX.md` is written for **local** at-cove ("edit `.at-cove/config.yml`, ask for `at-cove recreate`") — wrong for a Jam studio. Jam never writes `COLLABORATOR.md`, so every session loads the `(no collaborator role active)` placeholder.
- Studio facts exist only as data: the role's destinations, each `Destination.Env` translation, the effective egress. An agent learns them by failing (e.g. `gh` routed to `jam.local…` with "no such destination").
- On wake, `--continue` + a fixed resume string is all that is sent. The raise prompt lives in the transcript and can be summarised away by compaction; nothing can be updated mid-session. The resume string tells standing sessions about their "owner", which they don't have.

## Model

### Layers

| # | Layer | Owner | Source | Change rate |
|---|-------|-------|--------|-------------|
| 1 | **Boilerplate** | at-jam (code) | built in, per session kind | release |
| 2 | **Kit** | kit author | StudioKit `prompt` + new `notes`; generated tool list from `build-args` | kit version |
| 3 | **Studio** | **generated** | role scope → destinations (+ new `Destination.Note`), effective egress, addressing targets | grant edits |
| 4 | **Project** | project admin | new `Project.Context` + `Project.Resources`; contacts generated from `Roster` | occasional |
| 5 | **Role** | role admin | new `Role.Context` | occasional |
| 6 | **Jam** | Jam operator | new Jam-wide `Context` (standing rules) | rare |

Layers are **authoring units**; their order above is the **delivery order** (most stable first — maximises prompt-cache reuse and puts the most specific material nearest the work). The **Launch** prompt (per-raise task/brief/standing prompt) is not a layer: it stays the first `-p` user message.

### Precedence

Stated once, in the core header, verbatim:

> Sandbox hardening is enforced and cannot be overridden. Otherwise, on conflict the later section wins: Jam rules > Role > Project > Studio > Kit > Boilerplate. Your task (the first message) works within all of them.

### The two tiers of every layer

Every layer contributes the same shape (`sessionctx.Layer`):

```go
type Leaf struct {
    Name     string // file name under the layer dir, e.g. "repos.md"
    ReadWhen string // one line: the situation in which to open it
    Body     string // markdown
}
type Layer struct {
    Core   string // always-on; imperative; ≤ the layer's budget
    Leaves []Leaf // on demand
}
```

- **Core** — rules and must-knows only. Budgets (bytes, UTF-8): Boilerplate 2400, Kit 800, Studio 1600 (generated; overflow moves to a leaf), Project 1200, Role 1200, Jam 800. **Total core ≤ 8 KB (~2k tokens).**
- **Leaves** — detail. The compiler appends each layer's leaf table (`name — read when`) to that layer's core section, so the core is also the map. No leaf content is ever in the core.
- **Empty layer → nothing.** A layer with no core and no leaves emits no section, no header, no placeholder.

Budgets are enforced at **authoring time** (admin API / CLI / admin UI / `kit push` reject an over-budget core with the byte count). The compiler, which must never fail a raise over prose, truncates an over-budget core at a line boundary and appends `(truncated — see <layer>/CORE-full.md)`, writing the full text as a leaf, and logs a warning. Generated layers (Studio) never truncate: they spill rows past the budget into a leaf by construction.

### Single owner per fact (lint)

Each fact type has one owning layer: egress/destinations/addressing → Studio; installed tools → Kit; people → Project (roster); sandbox mechanics → Boilerplate. The compiler runs a cheap lint and logs (does not fail) when an authored layer restates an owned fact — v1 heuristic: an authored core contains a domain that is in the effective egress list, or a `human:`/`channel:` target. The admin UI shows the lint on save.

## The compiled bundle

`internal/jam/sessionctx` — pure, no I/O, table-tested:

```go
type Inputs struct {
    Kind     SessionKind     // ephemeral | personal | standing | admin
    Session  SessionFacts    // name, project, role, owner (personal), kit name@version
    Kit      Layer           // StudioKit prompt/notes + generated tool list
    Studio   StudioFacts     // destinations (name, upstream, env keys set, git route, note), egress, addressing
    Project  Layer           // authored core/leaves; Resources and Roster rendered in
    Role     Layer
    Jam      Layer
}
type Bundle struct {
    Core        string            // CORE.md
    Files       map[string]string // relative path → content, e.g. "studio/destinations.md"
    Fingerprint string            // sha256 over Core+Files, canonical order
    Layers      map[string]string // layer → per-layer fingerprint (for change notices)
}
func Compile(in Inputs) Bundle
```

Core layout (`CORE.md`):

```
# Session context
<precedence paragraph>
Detail lives in /agent-data/context/ — open a file only when its "read when" matches. Index: /agent-data/context/INDEX.md

## Boilerplate
<core>
## Kit — <kit>@<version>
<core>
- kit/tools.md — read when …
## Studio
…
```

`Files` always contains `INDEX.md` (every leaf, one row, `path | read when` — the progressive-disclosure map) plus `<layer>/<leaf>.md`. Leaves carry the standard frontmatter (`summary`, `read_when`, `owns`, `tier: leaf`) so `docs-navigate` habits apply unchanged.

### Generated content

- **Boilerplate (per kind)** — sandbox facts (what persists, egress is allow-listed, failures to a host are policy not flakiness), intercom (who the default recipient is for this kind, that `to` is required when there is none, `read`/`commit` semantics), turn model (each turn is a one-shot `claude -p`: background processes die at turn end; ending the turn is how you wait; a reply wakes you), ephemeral-only `worker-result.json` contract, and the Jam path for changing the kit (propose the `kit.yml` edit to a human; it lands via `kit push` + rebuild). Leaves: `boilerplate/changing-the-kit.md`, `boilerplate/hardening-limits.md` (moved from the image reference set's Jam-relevant parts).
- **Kit** — authored `prompt` (core) + `notes` (leaves) + generated `kit/tools.md` from `build-args` (`GO_VERSION: 1.27.1` → "Go 1.27.1"; unknown keys listed raw). The kit's `egress` list is **not** rendered here (Studio owns egress).
- **Studio** — one row per granted destination: name, what it reaches (`upstream`), how to use it (env keys set and, for `Git`, "https://github.com/ is routed via Jam"), plus `Destination.Note` (new, optional, ≤ 300 bytes — the human-written "translation", e.g. "use `git` over https; `gh` is not routed"). Then the effective egress list (role policy within the kit ceiling, plus base), then addressing: each `list_targets` target with who it is ("`human:alice` — project contact (roster)", "`human:you` — your owner"). Overflow → `studio/destinations.md`, `studio/egress.md`.
- **Project** — authored core (goals) and leaves; generated `project/resources.md` from `Resources` and `project/contacts.md` from `Roster` (name, handle, how to reach), with a one-line pointer to each in the core.

### Data model additions (Postgres jsonb docs; no column migrations except the new table)

- `Role.Context sessionctx.Layer` (`json:"context,omitzero"`) — managed by new `GET/PUT /admin/roles/{project}/{role}/context`; a role re-put keeps it (same pattern as egress/standing).
- `Project.Context sessionctx.Layer`, `Project.Resources []Resource{Name, Kind (repo|doc|tracker|url), Ref, Note}` — `GET/PUT /admin/projects/{project}/context`.
- `Destination.Note string` — editable with the existing destination edit path.
- `StudioKit.Notes []sessionctx.Leaf` (`notes:` in `kit.yml`, each `{name, read-when, body}` or `{name, read-when, file}` relative to the kit dir). Raise-time input: outside the image content key (like `prompt`).
- Jam-wide `Context`: migration `0004_jam_settings.sql` adds `jam_settings(key text primary key, doc jsonb)`; key `context`. `GET/PUT /admin/jam/context`.
- CLI: `at-jam context show|set|edit --role p/r | --project p | --jam` (`set` reads a YAML/markdown file: core + leaves). Admin UI: a "Session context" panel on role, project and Jam pages with the byte counter and lint.

## Delivery

```
Raise (Supervisor)                       cove-master, every turn
───────────────────                      ─────────────────────────────
gather Inputs → Compile → Bundle         GET /context (identity bearer)
pass Bundle (JSON) to launcher           fallback: last applied bundle
  → /dev/shm/cove-agent-context          if fingerprint changed: write
Launch prompt alone → -p (1st turn)        /agent-data/context/ (atomic swap)
                                         claude -p … --append-system-prompt-file
                                           /agent-data/context/CORE.md
                                           --system-prompt-snapshot off
```

- **`GET /context`** on the cove-facing listener, beside `/connector`, same auth (identity bearer, 401 on unknown/expired). It re-gathers Inputs **live** (role/project/jam/destination edits apply at the next turn) and returns the Bundle JSON. The raise-time bundle (via `/dev/shm`, like the prompt today) seeds turn 1 so a raise never depends on the listener.
- **`--system-prompt-snapshot off` is required.** Its default (`on`) records the system prompt on the conversation's first request and replays it verbatim on every `--continue`, ignoring new text until compaction — which would silently drop every update. With it off, an unchanged bundle renders byte-identical, so prompt caching is unaffected. A test asserts both flags are present in `claudeArgs`, and an integration check verifies the installed `claude` accepts them.
- **Writing the files:** cove-master writes to `/agent-data/context.new/`, then renames over `/agent-data/context/` (no half-written tree). It owns that directory outright; the agent may read it, and edits are discarded on the next refresh.
- **Change notice:** when the fingerprint changes between turns, the resume prompt gains one line: `Session context changed (role, studio) since your last turn — the system prompt is current; re-open any leaf you rely on.`
- **Resume prompt per kind:** the resident resume string is split — personal keeps "your owner"; standing says "a message may have arrived — use `read`" (fixes the misleading "owner").

### The sealed image files

The hardening `.init-agent-data` set stays sealed and stays loaded by `CLAUDE.md` for every sandbox, so it must not contradict a Jam session:

- `SANDBOX.md` is reworded launcher-neutral: sandbox facts stay; the "how to change the kit" paragraph becomes "If `/agent-data/context/CORE.md` exists you are a Jam session — its *Changing the kit* instructions apply; otherwise edit `.at-cove/config.yml` and ask for `at-cove recreate`."
- `COLLABORATOR.md`'s default becomes an empty file (no heading), so an absent role adds nothing.
- `ComposePrompt` and `JamBoilerplate` are deleted; their content moves into the compiler's Boilerplate.

## Errors and safety

- **No secrets in bundles.** Inputs carry env *keys* and route names, never values or credential names' resolved values; `Destination.CredName` is not rendered. A test compiles a fixture whose destinations carry credentials and asserts no credential string appears in any bundle byte.
- `/context` failure on a turn → last applied bundle, warn log (keys/fingerprints only), same as the connector.
- Compile cannot fail a raise: bad authored input is truncated/linted, never fatal. A missing role/project/kit → that layer is empty.
- Size cap on the whole bundle (core ≤ 8 KB enforced as above; files ≤ 256 KB total, leaves past the cap dropped in reverse delivery order (Jam first), each with a logged warning and an INDEX note).

## Testing

TDD per slice; hermetic (no VM):

- `sessionctx.Compile` table tests: layer order, empty-layer omission, per-kind boilerplate, budgets/truncation, Studio spill to leaves, INDEX completeness (every leaf listed, every listed file present), fingerprint stability (same inputs → same bytes; map iteration never leaks into output), no-secrets.
- Supervisor: raise passes the bundle and only the Launch prompt to the launcher.
- `/context` handler: auth, live re-gather, JSON shape.
- agentrun: `claudeArgs` flags; refresh writes atomically; change notice appears only on fingerprint change; fallback on fetch error (fake `ContextSource`, like `ConnectorSource`).
- Admin API/CLI: budget rejection, role re-put keeps `context`.
- Image: `SANDBOX.md` contains the Jam branch; `COLLABORATOR.md` default is empty.

## Slices (one PR each; docs updated in the same PR)

1. **Bundle + delivery.** `sessionctx` with Boilerplate (per kind) + Kit (prompt only) + an INDEX; Supervisor compiles; launcher ships it; cove-master writes `/agent-data/context/` and launches with `--append-system-prompt-file … --system-prompt-snapshot off`; Launch prompt alone in `-p`; `ComposePrompt` removed; `SANDBOX.md`/`COLLABORATOR.md` changes. Docs: `docs/usage/jam/kits.md` §prompt, `coves.md` §prompt, `standing-sessions.md`, OVERVIEW image section.
2. **Studio layer (generated).** `Destination.Note`; destinations/egress/addressing rendering + spill. Docs: `connector.md`, `comms-addressing.md`.
3. **Authored layers.** `Role.Context`, `Project.Context`/`Resources`, Jam context table + endpoints + CLI + admin UI panels; `StudioKit.Notes`; generated contacts/resources/tools. Docs: `roster.md`, `projects.md`, `kits.md`, `ui-pages.md`.
4. **Live refresh.** `GET /context`, per-turn fetch + atomic swap + change notice; per-kind resume prompts. Docs: `intercom.md`, `session-events.md` if events change, a new `docs/usage/jam/session-context.md` (operator view of the whole model; INDEX row).

Slice 1 alone fixes delivery (system prompt, survives compaction) and the wrong `SANDBOX.md`; each later slice only adds inputs.

## Open follow-ups (not in this design)

- **Timed self-wake** ("sleep until T / for D"): needs a wake-on timer and an intercom tool (e.g. `sleep`). Until then Boilerplate says only: "ending your turn waits for a message".
- **Per-session leaf usage telemetry** (which leaves get opened) to tune budgets.
