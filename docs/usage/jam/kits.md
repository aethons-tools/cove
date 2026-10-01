---
summary: The kit registry operator guide — authoring and storing named, versioned StudioKits in Jam with `kit push|list|show|versions|pin|rm`, the StudioKit schema, and binding one to a role with `role add --kit`.
read_when: You are registering a kit in Jam, pushing a new version, rolling a kit's current version back, or binding a kit to a role.
owns: the operator-facing kit-registry story — the StudioKit schema (`kind: studio`), the name/version/current model, the `kit` verbs, and the role→kit binding incl. the default kit
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to
tier: leaf
updated: 2026-09-30
---

# The kit registry

Jam stores **studio kits** — the small, directly-authored definition a brokered
studio is built and raised from — so a kit lives in Jam's data store and a role
references it by name. The registry is **studio-only**: it is not a place for a
full at-cove `config.yml` (those stay repo-committed `.at-cove/` kits; see
[migration note](studio-kit-migration.md)).

## The StudioKit

A StudioKit is YAML with a `kind: studio` discriminator (stored in the registry
as JSON with the same discriminator), a `name`, and five content fields. Unknown
fields are rejected, so a full-kit field on a studio kit fails at `push`.

```yaml
kind: studio
name: web                  # tag-safe: [A-Za-z0-9_.-]
base:                      # OPTIONAL — one of: image | context-files | context | context-dir | omitted
  image: ghcr.io/acme/base@sha256:…
egress: [github.com, pkg.go.dev]   # allow-list (capped by the ceiling, below)
build-args: {GO_VERSION: "1.23"}   # never secrets
secrets:                   # demands: name + description only, never values
  GH_TOKEN: {description: "clone access"}
prompt: "You work on the web service …"   # orients the session
```

| Field | Meaning |
|---|---|
| `base` | The image to build FROM — **exactly one of** (or omitted for the blessed default): `image:` a gated prebuilt ref; `context-files:` an inline build context; `context:` a base64-encoded zip build context (for real/binary contexts). Every form is provenance-gated — a context's Dockerfile must `FROM ${COVE_BASE_IMAGE}` so the built base descends from the blessed base. The build injects standard args: `COVE_BASE_IMAGE` (and its alias `AT_JAM_STUDIO_BASE_IMAGE`) = the blessed base ref to build FROM, and `AT_JAM_STUDIO_TARGET_ARCH` = `amd64`|`arm64`. **`context-files`** is a tree: the reserved, required key `dockerfile` is the Dockerfile content; every other key is a single path segment whose value is a file (string) or a subdirectory (nested map). **`context`** decodes host-side under hard caps (fail-closed): a root `Dockerfile` is required; entries that are absolute, contain `..`, or are symlinks are rejected; encoded ≤ 1 MiB, decompressed ≤ 64 MiB, ≤ 2000 entries. **`context-dir:`** is a client-only authoring convenience: `at-jam kit push` packs that host directory into `context` (same caps + symlink/`..` rejection, root `Dockerfile` required), resolving a relative path against the kit file's directory. A root `.dockerignore` is honored (exact `docker build` parity; the root `Dockerfile` and `.dockerignore` are always kept). It is never stored or sent to the server — the stored kit carries only the resulting `context`. |
| `egress` | The kit's allow-list, capped by the [ceiling](#the-egress-ceiling-cov-208). |
| `build-args` | Image build arguments. A key may not collide with a `secrets` name — secrets reach the session at raise, never the build. |
| `secrets` | Secret **demands** (name + description only); values are resolved at raise. In this slice demands are declarative only: per-demand env injection into the session is not wired yet (only the brokered identity token is injected today). |
| `prompt` | The kit layer of the session prompt. |

The session prompt is **composed at raise** from ordered layers — Jam
boilerplate → kit → project → role → launch — so it lives *outside* the image.
The image is tagged by a **build-digest** over only the build-affecting fields
(`base` + `egress` + `build-args`): a prompt- or secrets-only edit reuses the
cached image.

### The egress ceiling (COV-208)

A studio's egress **ceiling** structurally excludes `anthropic.com`,
`claude.com` and `claude.ai` (and their subdomains), so a brokered studio reaches
Anthropic only through the Jam broker (see [pool.md](pool.md)). An authored
`egress` naming them is not rejected — the ceiling simply caps it. `kit show`
prints the effective `egress ceiling:` and an `excluded (COV-208):` line, and
prepare logs the excluded roots.

## The model

A registered kit is a name holding **immutable, monotonically-numbered
versions**, behind a mutable **current** pointer:

- **push** a studio kit → it becomes the next version (`v1`, `v2`, …) and *current*
  advances to it.
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
