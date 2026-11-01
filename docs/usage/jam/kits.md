---
summary: The kit registry operator guide — authoring named, versioned StudioKits as `kits/<name>/` in the config repo, inspecting them with `kit list|show|versions|diff` (and `import|track|untrack|sync` from git), the StudioKit schema (incl. kit-declared MCP servers), and binding one to a role with the role file's `kit:`.
read_when: You are adding a kit to Jam, changing a kit's files or rolling one back, binding a kit to a role, giving a kit's sessions extra MCP servers, or making a kit build on another kit (the detail is in kits-inheritance.md).
owns: the operator-facing kit-registry story — the StudioKit schema (`kind: studio`) incl. `mcp-servers`, the name/version/current model, the `kit` verbs, and the role→kit binding incl. the default kit
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to
tier: leaf
updated: 2026-11-01
---

# The kit registry

Jam stores **studio kits** — the small, directly-authored definition a brokered
studio is built and raised from — and a role references one by name. A kit is
authored as a directory in the config repo, `kits/<name>/` (or referenced by
its own git source, `kits/<name>.source.yml`), and changes only by a commit:
by hand, or as a [draft](kits-drafts.md) on a branch in the admin console
([config-git.md](config-git.md#layout)). The registry is **studio-only**
([migration note](studio-kit-migration.md)).

## The StudioKit

A StudioKit is YAML, the `kit.yml` of a kit directory, with a `kind: studio`
discriminator and its content fields. Unknown fields are
rejected, so a full-kit field on a studio kit fails the plan. A kit **does not
name itself**: its name is its directory's (`kits/<name>/`), and must be tag-safe (`[A-Za-z0-9_.-]`).

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
    file: docs/release.md  # relative to the kit directory, read from it when the version is stored; or body: |
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
| `prompt` | The kit layer's always-on core (≤ 800 bytes; a version with more is refused) of the [session context](session-context.md). |
| `notes` | Leaves the kit ships into its sessions' context (`name`, `read-when`, `body` or a `file` in the kit directory, read when the version is stored); at most 20, same rules as [authored leaves](session-context-authoring.md). `tools.md` is reserved: every kit layer gains a generated `kit/tools.md` from `build-args`. Like `prompt`, a raise-time input — editing notes does not rebuild the image (a note *file* of a `Dockerfile` kit is part of its build context). In `kit.yml` the key is `read-when` (kit fields are kebab-case); the context admin API spells it `read_when`. |

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
  optionally after one scheme word (`Bearer ${VAR}`); a literal is rejected
  when the version is stored (and again by the harness). Claude Code expands `${VAR}` from the
  agent's environment at start; a server whose variable is unset fails to load
  without affecting the others. (Per-demand `secrets` injection is not wired yet
  — see the table — so today a variable must come from the image or connector.)
- **Egress is separate:** an `http` server's host must also be in `egress`.
- **Delivery:** `mcp-servers` is baked (non-secret JSON) into the image at
  `/etc/jam/mcp-servers.json`, the same build-time path as the egress lists,
  so it is **build-affecting** (part of the build-digest). jam-studio-agent refuses to
  start the agent if that file is missing or the generated config can't be written.

### Building on another kit (`base.kit`)

`base: { kit: web-dev }` (follow its current version; in the config repo also
the path form `kit: ../web-dev`) builds this kit on a parent kit's image and merges its definition over the
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
the admin UI print is the one under `claude-default`'s harness.) The tag also carries the Studio Manager's **assembly fingerprint** — the manager's embedded payload (hardening layer, jam-studio-agent), the blessed default base, the Jam host and the manager's identity key — so upgrading the manager (or moving Jam, or rotating the key) rebuilds each kit lazily on its next raise; running studios keep their image until re-raised. Superseded `jam-kit:*` images are not yet garbage-collected.

### The egress ceiling (COV-208)

A studio's egress **ceiling** structurally excludes `anthropic.com`,
`claude.com` and `claude.ai` (and their subdomains), so a brokered studio reaches
Anthropic only through the Jam broker (see [pool.md](pool.md)). An authored
`egress` naming them is not rejected — the ceiling simply caps it. `kit show`
prints the effective `egress ceiling:` and an `excluded (COV-208):` line, and
prepare logs the excluded roots.

**Legacy `name:`.** Kit files and registry rows from before names left the schema may still carry `name:`. It is accepted on input (and kept in the stored `kit.yml`, byte for byte) and must equal the kit's name — a `kits/api/kit.yml` that names itself `web` is refused (drop the field). Existing rows keep working as-is.

## The model

A registered kit is a name holding **immutable, monotonically-numbered
versions**, behind a **current** pointer:

- **A commit** that changes a kit's files, once applied, makes the next version
  (`v1`, `v2`, …) and *current* advances to it. Files equal to *current*'s make
  no version.
- A **role references a kit by name** (not `name@version`; the config repo
  refuses a pin, as versions are per-Jam); the name resolves to *current*.
  Upgrades don't churn role bindings.
- **Roll back** by committing the old files (`git revert`): that is a new
  version with the old content. Versions are never mutated or deleted.
- Each version records its **provenance** — when it was stored and where from:
  the config repo, dir, ref and commit, or for a kit from its own git source,
  that repo, dir, ref and commit (`kit versions`, `kit show`).

A version's directory is read, filtered by its `.dockerignore`, checked, and
its `kit.yml` validated as a studio kit ([kits-directory.md](kits-directory.md));
it stores the files, `kit.yml` byte for byte. A malformed kit is refused by the plan, not at a later raise.

## The `kit` verbs

```
jam kit list                                             # name  current=vN  versions=K  [parent=base-kit[@vN]]
jam kit show web                                         # current version's kit, its files (# files (tree <12 hex>):), ceiling/excluded
jam kit show web --version 1                             # a specific version's config
jam kit show web --own                                   # the kit's own YAML, not the merged one; `file:` notes shown with their contents
jam kit versions web                                     # VERSION CURRENT PUSHED SOURCE COMMIT
jam kit diff web [--from 2] [--to 3]                     # kit.yml's line diff, then the files added/removed/modified/mode (default: current vs the one before)
jam kit import --git <repo>[//dir]@<ref> [--name N]      # import a kit from git as a new version
jam kit track web --git <repo>[//dir]@<ref>              # import and keep the source for sync
jam kit sync web                                         # re-import from the tracked source
jam kit untrack web                                      # stop tracking (versions are kept)
```

- `import`/`track`/`sync`/`untrack` take the kit **from git** — Jam fetches
  `<dir>/kit.yml` from the repo itself; the source syntax, sync rules,
  credentials and errors are in [kits-git.md](kits-git.md). A kit the config
  repo references by `kits/<name>.source.yml` is fetched by the config apply
  instead ([config-git-sources.md](config-git-sources.md#kit-references-kitsnamesourceyml)).
- `kit diff` reads `GET /admin/kits/{name}/diff?from=N&to=M` (admin only). A
  binary file, or one over 64 KiB, gets a one-line marker, never its bytes. A
  broken version answers 400 with its own error.
- Removing `kits/<name>/` from the repo deletes the kit, **fail-closed**: a kit
  that any role references (or another kit [builds on](kits-inheritance.md#guards))
  can't be removed; the plan refuses it naming the referencing role or kits
  (`cannot delete kit web: role app/ic binds it`). Rebind the role first.

The admin UI shows the same on each kit's page — versions, diffs, files
([ui-pages.md](ui-pages.md#kit-pages)); on a branch it also edits the kit. A
diff shows the `kit.yml` line diff, then the files added, removed, modified
(sizes) or mode-changed; `&file=<path>` shows one file or its line diff
(binary or over 64 KiB: sizes only).

All verbs take the admin-client flags (`--app`/`--admin-url`/`--token`); see [operators.md](operators.md).

## Binding a kit to a role

A role's file in the config repo names its kit, `kit: web`
(`projects/acme/roles/builder.yml`). It names a kit in the repo or in Jam (a missing one fails the plan:
`kit web not found (not in the repo or Jam)`); the role resolves to its
*current* version. See [roster.md](roster.md) for the rest of the role.

## Role to kit, and the default kit

A brokered cove for the role raises from that kit. Resolution is
**fail-closed**: a name absent from the registry, or not tag-safe, fails the
raise. A role with no `kit:` raises the built-in **`default`** studio
kit — the blessed base, a minimal Anthropic-free egress list and a generic
prompt — which Jam **seeds into the registry at serve start** (idempotent,
and skipped once the config repo manages `default`), so
it is inspectable/versioned like any other (`kit show default`).

The supervisor carries only a light **reference** (`<id>@v<n>`) on each raise; the
launcher resolves it against its prepared-image inventory and, on a miss, Jam
resolves the full kit from *this* registry and calls `PrepareKit` to build it. That
handshake is documented on the raise side in
[coves.md](coves.md#the-studiokit-and-its-kit-prepare-protocol). A `ref` base is
**provenance-gated at build with no `--allow-unverified` escape hatch**.

Design rationale (the versioning model) lives in
[`../../superpowers/specs/2026-09-12-harbor-kit-registry.md`](../../superpowers/specs/2026-09-12-harbor-kit-registry.md).
