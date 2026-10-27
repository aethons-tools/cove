---
summary: The kit registry operator guide — authoring and storing named, versioned StudioKits in Jam with `kit push|list|show|versions|pin|rm|import|track|untrack|sync`, the StudioKit schema (incl. kit-declared MCP servers), and binding one to a role with `role add --kit`.
read_when: You are registering a kit in Jam, pushing a kit directory or a new version, rolling a kit's current version back, binding a kit to a role, giving a kit's sessions extra MCP servers, or making a kit build on another kit (the detail is in kits-inheritance.md).
owns: the operator-facing kit-registry story — the StudioKit schema (`kind: studio`) incl. `mcp-servers`, the name/version/current model, the `kit` verbs, and the role→kit binding incl. the default kit
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to
tier: leaf
updated: 2026-10-27
---

# The kit registry

Jam stores **studio kits** — the small, directly-authored definition a brokered
studio is built and raised from — so a kit lives in Jam's data store and a role
references it by name. The registry is **studio-only**
([migration note](studio-kit-migration.md)).

## The StudioKit

A StudioKit is YAML, the `kit.yml` of a kit directory, with a `kind: studio`
discriminator and its content fields. Unknown fields are
rejected, so a full-kit field on a studio kit fails at `push`. A kit **does not
name itself**: its name is the registry key it is pushed under (how `kit push` picks it:
[the verbs](#the-kit-verbs)), and must be tag-safe (`[A-Za-z0-9_.-]`).

```yaml
kind: studio
base:                      # OPTIONAL — one of: image | kit | omitted; a Dockerfile next to kit.yml is the build context
  image: ghcr.io/acme/base@sha256:…
egress: [github.com, pkg.go.dev]   # allow-list (capped by the ceiling, below)
build-args: {GO_VERSION: "1.23"}   # never secrets
secrets:                   # demands: name + description only, never values
  GH_TOKEN: {description: "clone access"}
prompt: "You work on the web service …"   # orients the session
notes:                     # leaves shipped into sessions' context (kit/<name>)
  - name: release.md
    read-when: you are cutting a release
    file: docs/release.md  # relative to the kit directory, read from it at push; or body: |
mcp-servers:               # extra MCP servers for the session's agent (env refs, never secrets)
  linear:
    type: http
    url: "${LINEAR_MCP_URL}"
    headers: {Authorization: "Bearer ${LINEAR_TOKEN}"}
```

| Field | Meaning |
|---|---|
| `base` | The image to build FROM — **one of** (or omitted for the blessed default): `kit:` a parent kit to build on ([below](#building-on-another-kit-basekit); then `JAM_STUDIO_BASE_IMAGE` is the parent's stage); `image:` a gated prebuilt ref (not with a `Dockerfile`). The build context is the **kit directory** ([kits-directory.md](kits-directory.md)). Every form is provenance-gated: a Dockerfile builds on the blessed base ([how](kits-directory.md#the-directory)). The build injects standard args: `JAM_STUDIO_BASE_IMAGE` = the blessed base ref to build FROM (the parent's stage for a `kit:` child), and `JAM_STUDIO_TARGET_ARCH` = `amd64`|`arm64`. The old packed-context fields are [refused](kits-directory.md#refusals). |
| `egress` | The kit's allow-list, capped by the [ceiling](#the-egress-ceiling-cov-208). |
| `build-args` | Image build arguments. A key may not collide with a `secrets` name — secrets reach the session at raise, never the build. |
| `secrets` | Secret **demands** (name + description only); values are resolved at raise. In this slice demands are declarative only: per-demand env injection into the session is not wired yet (only the brokered identity token is injected today). |
| `prompt` | The kit layer's always-on core (≤ 800 bytes; `kit push` rejects more) of the [session context](session-context.md). |
| `notes` | Leaves the kit ships into its sessions' context (`name`, `read-when`, `body` or a `file` in the kit directory, read at push); at most 20, same rules as [authored leaves](session-context-authoring.md). `tools.md` is reserved: every kit layer gains a generated `kit/tools.md` from `build-args`. Like `prompt`, a raise-time input — editing notes does not rebuild the image (a note *file* of a `Dockerfile` kit is part of its build context). In `kit.yml` the key is `read-when` (kit fields are kebab-case); the context admin API spells it `read_when`. |

A kit version is a **directory**: `kit.yml` plus its files; a `Dockerfile` beside `kit.yml` is the build context — the files, limits and `.dockerignore` are in [kits-directory.md](kits-directory.md).

### `mcp-servers` (COV-240)

A map of server name → `{type, url, headers}` (`type: http`) or
`{type, command, args}` (`type: stdio`) — Claude Code's `--mcp-config` entry
shape. Before the agent's first turn, jam-studio-agent's claude harness generates **one** config holding
the cove's own **`messaging`** server (`jam-studio-agent mcp`, the
[intercom](intercom.md#delivery-to-the-agent)) plus these, and runs claude with
`--strict-mcp-config`, so exactly messaging + the kit's servers load.

- **`messaging` is reserved** — a kit can neither declare nor override it.
- **No secret values.** Every header value must be an env reference `${VAR}`,
  optionally after one scheme word (`Bearer ${VAR}`); a literal is rejected at
  `kit push` (and again by the harness). Claude Code expands `${VAR}` from the
  agent's environment at start; a server whose variable is unset fails to load
  without affecting the others. (Per-demand `secrets` injection is not wired yet
  — see the table — so today a variable must come from the image or connector.)
- **Egress is separate:** an `http` server's host must also be in `egress`.
- **Delivery:** `mcp-servers` is baked (non-secret JSON) into the image at
  `/etc/jam/mcp-servers.json`, the same build-time path as the egress lists,
  so it is **build-affecting** (part of the build-digest). jam-studio-agent refuses to
  start the agent if that file is missing or the generated config can't be written.

### Building on another kit (`base.kit`)

`base: { kit: web-dev }` (follow its current version) or `kit: web-dev@v3` (pin)
builds this kit on a parent kit's image and merges its definition over the
parent's. The rules, errors, staleness and guards are in
[kits-inheritance.md](kits-inheritance.md).

The session context is compiled at raise ([session-context.md](session-context.md)),
so it lives *outside* the image.
The image is layered **kit base → harness → hardening**: between the kit's base
and the sealed hardening steps sits a **harness layer** that installs the agent
CLI at the raising role's [model-spec](model-spec-harness.md)
version and its plugins — a kit never installs Claude Code itself. The raise
resolves the role's model-spec *before* it looks for (or builds) the image.
The image is tagged by a **build-digest** over only the build-affecting fields
(`base` + the build context's tree hash + `egress` + `build-args` + `mcp-servers`
+ the **harness**: type, exact CLI version, plugins): a prompt-, notes- or
secrets-only edit reuses the cached image (the build context is the
[kit directory](kits-directory.md#the-directory)), while the same kit version raised by roles on different model-specs
builds one image per harness. Adding the harness (COV-242) changed every kit's
digest once, so each kit rebuilds on its next raise. (The digest `kit show` and
the admin UI print is the one under `claude-default`'s harness.) The tag also carries the Studio Manager's **assembly fingerprint** — the manager's embedded payload (hardening layer, jam-studio-agent), the blessed default base, the Jam host and the manager's identity key — so upgrading the manager (or moving Jam, or rotating the key) rebuilds each kit lazily on its next raise; running studios keep their image until re-raised. Removing at-task and at-switchboard from that payload (jam-only slice 2) changed every fingerprint once, so each kit rebuilds on its next raise after that upgrade. Superseded `jam-kit:*` images are not yet garbage-collected.

### The egress ceiling (COV-208)

A studio's egress **ceiling** structurally excludes `anthropic.com`,
`claude.com` and `claude.ai` (and their subdomains), so a brokered studio reaches
Anthropic only through the Jam broker (see [pool.md](pool.md)). An authored
`egress` naming them is not rejected — the ceiling simply caps it. `kit show`
prints the effective `egress ceiling:` and an `excluded (COV-208):` line, and
prepare logs the excluded roots.

**Legacy `name:`.** Kit files and registry rows from before names left the schema may still carry `name:`. It is accepted on input (and kept in the stored `kit.yml`, byte for byte), may name the kit at `kit push` ([the verbs](#the-kit-verbs)), and must equal the name the kit is pushed under — a file that names itself `web` can't be pushed as `api` (drop the field). Existing rows keep working as-is.

## The model

A registered kit is a name holding **immutable, monotonically-numbered
versions**, behind a mutable **current** pointer:

- **push** a studio kit → it becomes the next version (`v1`, `v2`, …) and *current*
  advances to it. Pushing a definition identical to *current* makes no new
  version (`kit push` prints `web unchanged (current v3)`).
- A **role references a kit by name** (not `name@version`); the name resolves to
  *current*. Upgrades don't churn role bindings.
- **pin** *current* to an older version to **roll back** (or forward); versions
  are never mutated or deleted.
- Each version records its **provenance** — when it was pushed and, for one
  imported from git, the repo, dir, ref and commit (`kit versions`, `kit show`).

On `push` the directory is read, filtered by its `.dockerignore`, checked, and its `kit.yml` parsed and validated as a studio kit. The version stores the files, `kit.yml` byte for byte. A push whose files equal *current*'s makes no version. A malformed or non-studio kit is rejected at `push`, not at a later raise.

## The `kit` verbs

```
jam kit push ./web                                       # → "pushed web v3" (name: --name, else kit.yml's name:, else the dir's)
jam kit push ./web --name web-dev                        # push the directory under another name
jam kit push --config ./kit.yml --name web               # push kit.yml alone (a one-file version)
jam kit push --config - --name web                       # read kit.yml from stdin
jam kit list                                             # name  current=vN  versions=K  [parent=base-kit[@vN]]
jam kit show web                                         # current version's kit, its files (# files (tree <12 hex>):), ceiling/excluded
jam kit show web --version 1                             # a specific version's config
jam kit show web --own                                   # the kit's own YAML, not the merged one; `file:` notes shown with their contents
jam kit versions web                                     # VERSION CURRENT PUSHED SOURCE COMMIT
jam kit diff web [--from 2] [--to 3]                     # kit.yml's line diff, then the files added/removed/modified/mode (default: current vs the one before)
jam kit pin web 1                                        # roll current back to v1 (409 while a kit follows web)
jam kit rm web                                           # remove the kit (all versions; 409 while a kit is built on it)
jam kit import --git <repo>[//dir]@<ref> [--name N]      # import a kit from git as a new version
jam kit track web --git <repo>[//dir]@<ref>              # import and keep the source for sync
jam kit sync web                                         # re-import from the tracked source
jam kit untrack web                                      # stop tracking (versions are kept)
```

- `kit push` **validates** the directory before sending it — a refused file or
  a malformed or non-studio kit is rejected client-side, not stored. Any pre-existing
  full-kit row now fails `kit show`/resolution (see the [migration
  note](studio-kit-migration.md)).
- `import`/`track`/`sync`/`untrack` take the kit **from git** — Jam fetches
  `<dir>/kit.yml` from the repo itself; the source syntax, sync rules,
  credentials and errors are in [kits-git.md](kits-git.md).
- `kit diff` reads `GET /admin/kits/{name}/diff?from=N&to=M` (admin only). A
  binary file, or one over 64 KiB, gets a one-line marker, never its bytes. A
  broken version answers 400 with its own error.
- `kit rm` is **fail-closed**: a kit that any role references (or another kit
  [builds on](kits-inheritance.md#guards)) can't be removed; the command reports
  the referencing role or kits. Rebind the role first.

The admin UI does the same from each kit's page — versions, diffs, pin, push ([ui-pages.md](ui-pages.md#kit-pages)); its "push a new version" box edits `kit.yml` and keeps the version's other files. A version's page shows its `kit.yml` as stored and a **Files** table (path, mode, size, `kit.yml` included). A diff shows the `kit.yml` line diff, then the files added, removed, modified (sizes) or mode-changed, with the unchanged counted. `&file=<path>` shows one file or its line diff (binary or over 64 KiB: sizes only).

All verbs take the admin-client flags (`--app`/`--admin-url`/`--token`); see [operators.md](operators.md).
To change a kit's files before they become a version, open a **draft** (`kit draft open|status|get|put|rm|sync|publish|discard`): see [kits-drafts.md](kits-drafts.md).

## Binding a kit to a role

```
jam role add --project acme --name builder --destinations anthropic,git --kit web
```

`--kit` names a registered kit (it must already exist — `role add` fails closed
otherwise); the role resolves to its *current* version. See
[roster.md](roster.md) for the rest of the role surface.

## Role to kit, and the default kit

A brokered cove for the role raises from that kit. Resolution is
**fail-closed**: a name absent from the registry, or not tag-safe, fails the
raise. A role with `--kit` unset (`""`) raises the built-in **`default`** studio
kit — the blessed base, a minimal Anthropic-free egress list and a generic
prompt — which Jam **seeds into the registry at serve start** (idempotent), so
it is inspectable/versioned like any other (`kit show default`).

The supervisor carries only a light **reference** (`<id>@v<n>`) on each raise; the
launcher resolves it against its prepared-image inventory and, on a miss, Jam
resolves the full kit from *this* registry and calls `PrepareKit` to build it. That
handshake is documented on the raise side in
[coves.md](coves.md#the-studiokit-and-its-kit-prepare-protocol). A `ref` base is
**provenance-gated at build with no `--allow-unverified` escape hatch**.

Design rationale (the versioning model) lives in
[`../../superpowers/specs/2026-09-12-harbor-kit-registry.md`](../../superpowers/specs/2026-09-12-harbor-kit-registry.md).
