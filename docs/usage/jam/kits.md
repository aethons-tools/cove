---
summary: The kit registry operator guide — authoring and storing named, versioned StudioKits in Jam with `kit push|list|show|versions|pin|rm`, the StudioKit schema (incl. kit-declared MCP servers), and binding one to a role with `role add --kit`.
read_when: You are registering a kit in Jam, pushing a new version, rolling a kit's current version back, binding a kit to a role, or giving a kit's sessions extra MCP servers.
owns: the operator-facing kit-registry story — the StudioKit schema (`kind: studio`) incl. `mcp-servers`, the name/version/current model, the `kit` verbs, and the role→kit binding incl. the default kit
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to
tier: leaf
updated: 2026-10-05
---

# The kit registry

Jam stores **studio kits** — the small, directly-authored definition a brokered
studio is built and raised from — so a kit lives in Jam's data store and a role
references it by name. The registry is **studio-only**: it is not a place for a
full at-cove `config.yml` (those stay repo-committed `.at-cove/` kits; see
[migration note](studio-kit-migration.md)).

## The StudioKit

A StudioKit is YAML with a `kind: studio` discriminator (stored in the registry
as JSON with the same discriminator) and its content fields. Unknown fields are
rejected, so a full-kit field on a studio kit fails at `push`. A kit **does not
name itself**: its name is the registry key it is pushed under (`kit push
--name`), and must be tag-safe (`[A-Za-z0-9_.-]`).

```yaml
kind: studio
base:                      # OPTIONAL — one of: image | context-files | context | context-dir | omitted
  image: ghcr.io/acme/base@sha256:…
egress: [github.com, pkg.go.dev]   # allow-list (capped by the ceiling, below)
build-args: {GO_VERSION: "1.23"}   # never secrets
secrets:                   # demands: name + description only, never values
  GH_TOKEN: {description: "clone access"}
prompt: "You work on the web service …"   # orients the session
notes:                     # leaves shipped into sessions' context (kit/<name>)
  - name: release.md
    read-when: you are cutting a release
    file: docs/release.md  # relative to this file, read at `kit push`; or `body: |`
mcp-servers:               # extra MCP servers for the session's agent (env refs, never secrets)
  linear:
    type: http
    url: "${LINEAR_MCP_URL}"
    headers: {Authorization: "Bearer ${LINEAR_TOKEN}"}
```

| Field | Meaning |
|---|---|
| `base` | The image to build FROM — **exactly one of** (or omitted for the blessed default): `image:` a gated prebuilt ref; `context-files:` an inline build context; `context:` a base64-encoded tar.gz build context (for real/binary contexts). A context is **streamed to `docker build -`** (never extracted to a host dir), so docker applies its unix file modes — an executable script keeps its `+x` bit. Every form is provenance-gated — a context's Dockerfile must `FROM ${COVE_BASE_IMAGE}` so the built base descends from the blessed base. The build injects standard args: `COVE_BASE_IMAGE` (and its alias `AT_JAM_STUDIO_BASE_IMAGE`) = the blessed base ref to build FROM, and `AT_JAM_STUDIO_TARGET_ARCH` = `amd64`|`arm64`. **`context-files`** is a tree: the reserved, required key `dockerfile` is the Dockerfile content; every other key is a single path segment whose value is a file (string) or a subdirectory (nested map). Inline files carry no mode, so they are packed `0644` — an inline script must be run `sh script.sh` or `chmod +x`'d in the Dockerfile. **`context`** is pre-flighted host-side under hard caps (fail-closed) before streaming: a root `Dockerfile` is required; entries that are absolute, contain `..`, or are symlinks/hardlinks/irregular are rejected; encoded ≤ 1 MiB, decompressed ≤ 64 MiB, ≤ 2000 entries. **`context-dir:`** is a client-only authoring convenience: `at-jam kit push` packs that host directory into `context` as a deterministic, **mode-preserving** tar.gz (same caps + symlink/`..` rejection, root `Dockerfile` required), resolving a relative path against the kit file's directory. A root `.dockerignore` is honored (exact `docker build` parity; the root `Dockerfile` and `.dockerignore` are always kept). It is never stored or sent to the server — the stored kit carries only the resulting `context`. |
| `egress` | The kit's allow-list, capped by the [ceiling](#the-egress-ceiling-cov-208). |
| `build-args` | Image build arguments. A key may not collide with a `secrets` name — secrets reach the session at raise, never the build. |
| `secrets` | Secret **demands** (name + description only); values are resolved at raise. In this slice demands are declarative only: per-demand env injection into the session is not wired yet (only the brokered identity token is injected today). |
| `prompt` | The kit layer's always-on core (≤ 800 bytes; `kit push` rejects more) of the [session context](session-context.md). |
| `notes` | Leaves the kit ships into its sessions' context (`name`, `read-when`, `body` or a `file` relative to the kit file, read by `kit push`); at most 20, same rules as [authored leaves](session-context-authoring.md). `tools.md` is reserved: every kit layer gains a generated `kit/tools.md` from `build-args`. Like `prompt`, a raise-time input — editing notes does not rebuild the image. In stored kit JSON the key is `read-when` (kit fields are kebab-case); the context admin API spells it `read_when`. |

### `mcp-servers` (COV-240)

A map of server name → `{type, url, headers}` (`type: http`) or
`{type, command, args}` (`type: stdio`) — Claude Code's `--mcp-config` entry
shape. Before the agent's first turn, cove-master's claude harness generates **one** config holding
the cove's own **`messaging`** server (`cove-master mcp`, the
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
  `/etc/cove/mcp-servers.json`, the same build-time path as the egress lists,
  so it is **build-affecting** (part of the build-digest). cove-master refuses to
  start the agent if that file is missing or the generated config can't be written.

The session context is compiled at raise ([session-context.md](session-context.md)),
so it lives *outside* the image.
The image is layered **kit base → harness → hardening**: between the kit's base
and the sealed hardening steps sits a **harness layer** that installs the agent
CLI at the raising role's [model-spec](model-spec-harness.md)
version and its plugins — a kit never installs Claude Code itself. The raise
resolves the role's model-spec *before* it looks for (or builds) the image.
The image is tagged by a **build-digest** over only the build-affecting fields
(`base` + `egress` + `build-args` + `mcp-servers` + the **harness**: type, exact
CLI version, plugins): a prompt-, notes- or secrets-only edit reuses the cached
image, while the same kit version raised by roles on different model-specs
builds one image per harness. Adding the harness (COV-242) changed every kit's
digest once, so each kit rebuilds on its next raise. (The digest `kit show` and
the admin UI print is the one under `claude-default`'s harness.) The tag also carries the launcher's **assembly fingerprint** — at-jam's embedded payload (hardening layer, at-task / at-switchboard / cove-master), the blessed default base, the Jam host and the launcher key — so upgrading Jam (or moving it, or rotating its key) rebuilds each kit lazily on its next raise; running studios keep their image until re-raised. Superseded `cove-kit:*` images are not yet garbage-collected.

### The egress ceiling (COV-208)

A studio's egress **ceiling** structurally excludes `anthropic.com`,
`claude.com` and `claude.ai` (and their subdomains), so a brokered studio reaches
Anthropic only through the Jam broker (see [pool.md](pool.md)). An authored
`egress` naming them is not rejected — the ceiling simply caps it. `kit show`
prints the effective `egress ceiling:` and an `excluded (COV-208):` line, and
prepare logs the excluded roots.

**Legacy `name:`.** Kit files and registry rows from before names left the
schema may still carry `name:`. It is accepted on input, never stored, and must
equal the name the kit is pushed under — a file that names itself `web` can't be
pushed as `api` (drop the field). Existing rows keep working as-is.

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

On `push` the YAML is **parsed and validated as a studio kit, then stored as
canonical JSON**; `kit show` renders it **back as YAML** (the config rule of
engagement). A malformed or non-studio kit is rejected at `push`, not at a later
raise.

## The `kit` verbs

```
at-jam kit push --name web --config ./web.studio.yml        # → "pushed web v3"
at-jam kit push --name web --config -                       # read config from stdin
at-jam kit list                                             # name  current=vN  versions=K
at-jam kit show web                                         # current version's kit + ceiling/excluded
at-jam kit show web --version 1                             # a specific version's config
at-jam kit versions web                                     # v1, v2, v3, …
at-jam kit pin web 1                                        # roll current back to v1
at-jam kit rm web                                           # remove the kit (all versions)
```

- `kit push` **validates** the file as a StudioKit before sending it — a
  malformed or non-studio kit is rejected client-side, not stored. Any pre-existing
  full-kit row now fails `kit show`/resolution (see the [migration
  note](studio-kit-migration.md)).
- `kit rm` is **fail-closed**: a kit that any role references can't be removed
  (the command reports the referencing role); `ungrant`/rebind the role first, or
  point the role at another kit.

The admin UI does the same from each kit's page — versions, diffs, pin, push
([ui-pages.md](ui-pages.md#kit-pages)).

All verbs take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Binding a kit to a role

```
at-jam role add --project acme --name builder --destinations anthropic,git --kit web
```

`--kit` names a registered kit (it must already exist — `role add` fails closed
otherwise). The role then resolves to that kit's *current* version. See
[roster.md](roster.md) for the rest of the role surface.

## Role to kit, and the default kit

`role add --kit <name>` names a studio kit **directly**; a brokered cove for that
role raises from it. Resolution is **fail-closed**: a name that is absent from the
registry, or not tag-safe, fails the raise. A role with `--kit` unset (`""`)
raises the built-in **`default`** studio kit — the blessed base, a minimal
Anthropic-free egress list and a generic prompt — which Jam **seeds into the
registry at serve start** (idempotent), so it is inspectable/versioned like any
other (`kit show default`).

The supervisor carries only a light **reference** (`<id>@v<n>`) on each raise; the
launcher resolves it against its prepared-image inventory and, on a miss, Jam
resolves the full kit from *this* registry and calls `PrepareKit` to build it. That
handshake is documented on the raise side in
[coves.md](coves.md#the-studiokit-and-its-kit-prepare-protocol). A `ref` base is
**provenance-gated at build with no `--allow-unverified` escape hatch**.

## Upgrades & rollback

- **Upgrade** a role's kit for everyone: `kit push --name web --config <new>` —
  *current* advances, and every role bound to `web` picks up the new version.
- **Roll back**: `kit pin web <older-version>` — *current* moves back; bindings are
  unchanged (they still reference `web`).

Design rationale (the versioning model) lives in
[`../../superpowers/specs/2026-09-12-harbor-kit-registry.md`](../../superpowers/specs/2026-09-12-harbor-kit-registry.md).
