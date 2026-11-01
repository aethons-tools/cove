---
summary: "`just local` — build the studio images, `jam`/`jam-studio-manager` and the `jam:dev` container image so they point at each other: the local jam-studio-base becomes the binaries' default, trusted studio base."
read_when: You want a local Jam whose studios run on images you just built from `images/` (a changed base or jam-studio), or you see "local studio base" in `jam version`, or a release build refused a `local.txt`.
owns: "`just local`/`local-clean` (scripts/local.sh), the local studio base (internal/basedigest/blessed/local.txt) and its release guard"
prereqs: DEVELOPMENT.md for the image tree and the release pipeline; usage/jam/kits.md for the provenance gate
tier: leaf
updated: 2026-10-30
---

# A local build that points at itself: `just local`

`just local` builds everything a local Jam needs from this checkout, wired together:
the studio images, then `jam` and `jam-studio-manager` with that base as their default studio base, then `jam:dev`.
Without it, a local `jam` still builds studios FROM the published `ghcr.io/aethons-tools/jam-studio-base`,
and the provenance gate rejects anything built FROM a local image.

## What it does

[`scripts/local.sh`](../scripts/local.sh), in one Docker (below):

1. Builds `jam-studio-base:dev-<id>`, then `jam-studio:dev-<id>` FROM it. `<id>` is the first 12 hex digits of the image ID, so a tag never moves to other content.
2. Writes `internal/basedigest/blessed/local.txt` (git-ignored) naming that base.
3. Packages the VS Code extension (`scripts/stage-vscode.sh`, without its checks) and runs `scripts/build.sh` for the host and for the Docker's `linux/<arch>`. The binaries embed `local.txt`.
4. Builds `jam:dev` from the linux `jam` (`scripts/jam-image.sh`), for `just dev-jam-up`.

It ends by printing the refs and the `base:` for a kit on the local `jam-studio`.

| Setting | Meaning |
|---|---|
| `DOCKER_CONTEXT` | The Docker to build in: the one your Studio Manager builds studios in. Default `colima` when that context exists, else the current one. |
| `LOCAL_SKIP_IMAGES=1` | Reuse the images `local.txt` names; rebuild only the binaries and `jam:dev`. |

## What a local base changes

With `local.txt` embedded ([`internal/basedigest`](../internal/basedigest)):

- **The default base** (a kit that selects none) is the local base, for `jam` and `jam-studio-manager` alike. Run both from this build: the launcher in Jam and the manager's backend each resolve it.
- **Kit Dockerfiles** building `FROM ${JAM_STUDIO_BASE_IMAGE}` get the local base too: that build arg is the default base.
- **The gate** trusts the local base first, then the published blessed bases, so a kit with `base: {image: jam-studio:dev-<id>}` (or a Dockerfile FROM it) passes, and kits on the ghcr images keep working.
- **`jam version`** notes `jam: local studio base <ref>` on stderr (its stdout stays the bare version); **`jam-studio-manager version`** adds `(local studio base <ref>)`.

The images must be in the manager's Docker: the gate inspects the local tag and can't pull it.

## Staying local, and leaving

`local.txt` stays until you remove it, so every `just build` after `just local` embeds the local base too, and says so.
`just local-clean` removes it; the next build is a normal one.

A release can't carry one: the goreleaser before-hook runs [`scripts/release/check-no-local-base.sh`](../scripts/release/check-no-local-base.sh), which fails when `local.txt` exists.
CI's checkout never has it.

## Tests

`scripts/local.test.sh` (`just test-local`) runs `local.sh` against a fake `docker` and stub build scripts: no daemon.
`internal/basedigest`'s tests cover the default and blessed refs with and without a local base.
