# jam: StudioKit `base` sources — image / inline tree / zip context

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** widen a StudioKit's `base` from the shipped `ref`/`dockerfile`/`context(map)` to **four mutually-exclusive forms** — a prebuilt `image` ref, an inline `context-files` tree (text), a base64 `context` zip (real/binary contexts), or omitted (blessed default). The two context forms funnel into the same provenance-gated Dockerfile build added in COV-223 (#272).
**Builds on:** the StudioKit slice (COV-222 #271) and the Dockerfile-context build (COV-223 #272) — `internal/studio` (`Base`, `BuildDigest`), the launcher's `materializeDockerfileBase` + `backend.ResolveKitBaseDockerfile`/`BaseSpec.DockerfileDir`, and `internal/baseimage`'s gate.
**Does not change:** the provenance gate (ON, no escape hatch, for every base form); the two-identity model (`BuildDigest` over build-affecting fields); COV-208; `at-cove`.
**Migration:** replaces the just-shipped `Base{Ref,Dockerfile,Context}` (brand-new, this session) with **no shim**; the `dfkit` dev example is rewritten to `context-files`, and the default kit (empty base) is unaffected.

## Why

The shipped `Base.Dockerfile` + `Base.Context map[path]string` only carries **text** files — "not very real" for a base that needs binaries or a large tree. The inline form is still wanted for simple cases, but it's one of several ways to name a base. This generalizes `base` to cover: a prebuilt image, a simple inline tree, and a full (possibly binary) build context — each still provenance-gated.

## The four forms (exactly one; omitted ⇒ blessed default)

```yaml
# 1. a prebuilt, gated image ref (renames today's `ref`)
base:
  image: ghcr.io/acme/base:tag

# 2. a simple context authored inline (text only)
base:
  context-files:
    dockerfile: |            # RESERVED key, REQUIRED — the Dockerfile content
      ARG COVE_BASE_IMAGE
      FROM ${COVE_BASE_IMAGE}
      COPY dir1/file.txt /etc/thing
    dir1:                    # a map ⇒ a subdirectory (recursive)
      file.txt: |            # a string ⇒ a file
        some text

# 3. a real build context (may include binaries), base64 of a zip
base:
  context: |
    <base64-encoded zip; its root must contain a Dockerfile>
```

### `image`
Renames the shipped `Base.Ref`. Resolved + gated via `backend.ResolveKitBase` (unchanged path); must descend from a blessed `cove-base-image`.

### `context-files` (inline tree)
A recursive tree materialized into the build dir:
- the reserved key **`dockerfile`** holds the Dockerfile content (**required** — a context needs one);
- every other key is a **single path segment** (no `/`, no `..`, non-empty): a **string** value is a file's content, a **map** value is a subdirectory (recurse).
An invalid key (containing `/`, `..`, empty, or the reserved name misused) fails validation. This replaces the flat `dockerfile` + `context map[path]string`.

### `context` (base64 zip) — untrusted, hard caps, fail-closed
A base64 string decoding to a **zip** whose root contains a `Dockerfile`. Decoded + unzipped host-side into the build dir. Because it is untrusted input we decompress, it is guarded (all violations are **loud errors**, never silent):

- **Cheap checks at register** (`ParseStudioKit`/`Validate`): the string is valid base64; the decoded bytes are a readable zip; encoded size ≤ **1 MiB**.
- **Full checks at prepare** (materialize): reject any entry whose name is absolute, contains a `..` component, or is a symlink/irregular file (zip-slip / escape defense); enforce total **decompressed size ≤ 64 MiB** (via a limited reader, not the header's claimed size) and **entry count ≤ 2000** (bomb defense); require a top-level `Dockerfile` after extraction.
- Uses Go stdlib `archive/zip` only (no new dependency).

The caps are constants (tunable); the numbers above are the initial policy.

## Unification (downstream unchanged)

`context-files` and `context` both **materialize into the per-ref base build dir**, then reuse the COV-223 path: `backend.ResolveKitBaseDockerfile(dir)` builds + gates it (`FROM ${COVE_BASE_IMAGE}`), and the kit image is built FROM the resolved base. `image` uses `ResolveKitBase`. So the launcher gains only a *normalize-to-a-dir* step in front of the existing build+gate; `PrepareKit`'s structure is otherwise unchanged.

## BuildDigest

All three set forms are build-affecting. `BuildDigest` hashes whichever is present — the `image` string, the `context` base64 blob verbatim, or the canonicalized `context-files` tree — plus egress + build-args, as today. Prompt/secrets remain excluded. A base change (any form) yields a new image tag; a prompt-only edit still reuses the image.

## Go modeling

`studio.Base` becomes:
```go
type Base struct {
    Image        string         `yaml:"image,omitempty" json:"image,omitempty"`
    ContextFiles ContextTree    `yaml:"context-files,omitempty" json:"contextFiles,omitempty"`
    Context      string         `yaml:"context,omitempty" json:"context,omitempty"` // base64 zip
}
```
`ContextTree` is a recursive `map[string]TreeNode` where a node is either a file (string) or a subtree — modeled with a small custom `UnmarshalYAML`/`MarshalJSON` (a node is `map[string]any` internally, normalized on parse), so a string leaf and a map node are unambiguous and the JSON form is deterministic for the digest. `Validate` enforces exactly-one-of {Image, ContextFiles, Context} (or none ⇒ default), the reserved/required `dockerfile` in `context-files`, single-segment keys, and the cheap `context` checks.

## Testing (hermetic)

- **Validation:** exactly-one-of; `context-files` requires `dockerfile`; keys with `/`/`..`/empty rejected; `context` base64/zip-readable/≤1 MiB cheap checks; `image` unchanged.
- **Materialize (`context-files`):** nested tree written correctly; path-escape rejected (carried over from COV-223).
- **Materialize + unzip (`context`):** a valid zip extracts + builds; **zip-slip** (`../x`, absolute), **symlink**, **entry-count >2000**, and **decompressed >64 MiB** each fail loud with no partial/escaped write; missing root Dockerfile fails.
- **BuildDigest:** differs across the three forms and across content changes; stable for a prompt-only edit.
- **Launcher:** both context forms reach `ResolveKitBaseDockerfile` with the materialized dir; `image` reaches `ResolveKitBase`; gate failures fail closed (from COV-223).

Real-substrate stays behind `integration`; the live loop reuses `dev/verify-dockerfile-kit.sh` (now exercising a `context-files` kit) + a new zip-context example.

## Additive & reversible

Only `internal/studio` (`Base` shape + zip/tree materialization helpers), the launcher's normalize-to-a-dir step, and docs/examples change. The gate, digest model, and `at-cove` are untouched. One intentional break: the shipped `Base{Ref,Dockerfile,Context(map)}` shape, replaced no-shim (this session's own, unreleased).
