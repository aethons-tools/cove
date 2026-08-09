# `adopt-base` — a recipe to adopt a new base image — design

**Status:** design approved (brainstorm 2026-08-09), pending implementation.
**Date:** 2026-08-09
**Related:** the base-image pin (`.at-cove/config.yml` → `image.base`), the blessed
watermark (`internal/basedigest/blessed/watermark.txt`), the blessed-list generator
(`cmd/gen-blessed`, `internal/blessgen`), the provenance gate
(`internal/baseimage/`, COV-34), the release pipeline
(`.github/workflows/release.yml`, `docs/DEVELOPMENT.md`).

## Goal

Automate the manual dance of **adopting a newly-published base image**: resolve the
`@sha256` index digest for a published tag and rewrite the two files that pin it —
`.at-cove/config.yml`'s `image.base` (always) and
`internal/basedigest/blessed/watermark.txt` (on a **breaking** base change only).

Today this is entirely by hand: `docker buildx imagetools inspect …` for each digest,
then two edits, then a PR. The digests can't be resolved from inside an egress-locked
sandbox unless `ghcr.io` is allow-listed, and the two edits have different triggers that
are easy to conflate. A single tested recipe removes the toil and the footguns.

**Non-goals:** publishing images (CI owns that); regenerating the gitignored
`generated.txt` snapshot (`gen-blessed` owns that); bumping the watermark on *routine*
(non-breaking) bumps; becoming a shipped binary (dev-only maintenance tool).

## Surface

A justfile recipe mirroring `gen-blessed` (thin wrapper over a `cmd/` Go tool):

```
just adopt-base <tag> [--breaking] [--pr] [--reason "..."]
# routine bump:            just adopt-base 531-0812
# breaking cutover + PR:   just adopt-base 527-0808 --breaking --pr --reason "Docker+systemd base, drop podman — COV-116"
```

- `<tag>` — a published release tag (e.g. `527-0808`, or `latest`). Resolved to the
  multi-arch **index** digest.
- `--breaking` — also raise `watermark.txt` to the `cove-base-image` index digest for
  `<tag>`, evicting all older bases from the blessed set. Omitted ⇒ `image.base` only.
- `--pr` — after editing, branch + commit + push + `gh pr create`. Omitted ⇒ edit the
  files, print a summary and suggested next steps, leave git untouched.
- `--reason "…"` — fills the parenthetical in the `watermark.txt`
  `# Current watermark:` comment (and seeds the PR body). Only meaningful with
  `--breaking`. Default: `(breaking base change)`.

The recipe body is a one-liner: `go run ./cmd/adopt-base {{ARGS}}`.

## Architecture

Preserves the repo's pure-plan / execution split (AGENTS.md).

### `internal/adoptbase` — pure transforms (hermetically tested)

No I/O. Two functions operating on file *contents*:

- `RewriteImageBase(configYAML, digest string) (string, error)` — replace the
  `image.base` value with `ghcr.io/aethons-tools/cove-image@<digest>`, anchored on the
  existing `base:` line under the `image:` block. Preserves surrounding comments and
  formatting (line-oriented replacement, **not** a YAML round-trip, which would drop
  comments). Errors if the anchor line is absent or ambiguous.
- `RewriteWatermark(watermarkTxt, tag, digest, reason string) (string, error)` — replace
  the single non-comment digest line with `<digest>` and rewrite the
  `# Current watermark: cove-base-image:<tag> (<reason>)` comment line. Preserves the
  rest of the explanatory header. Errors if the file shape is unexpected.

Both are idempotent (re-running with the same digest is a no-op diff).

### `internal/blessgen` — tag → digest resolver (reuses the CI-proven client)

Extend the existing GHCR client with:

- `func (g GHCR) DigestForTag(ctx context.Context, tag string) (string, error)` — reuse
  the same package-versions fetch the blessed-list walk already uses; return the digest
  of the release-tagged **index** manifest carrying `tag`. Error if `tag` names no
  version, or names only per-arch child manifests (a kit must never pin those).

The client is already `Owner`/`Package`-generic, so the same code resolves both
`cove-image` and `cove-base-image`; only the `basedigest.Image` constant is base-specific.
This keeps a single, trusted, tested registry-access path — the one CI already relies on.

### `cmd/adopt-base` — execution shell (thin, untested like `cmd/gen-blessed`)

Reads `GITHUB_TOKEN`, constructs the two `blessgen.GHCR` clients, calls the resolver and
the pure transforms, writes the files, and — under `--pr` — runs git/`gh`. Flag parsing,
file read/write, and git/gh are the only impure parts and live here.

## Data flow

```
just adopt-base 527-0808 --breaking --pr
  → cmd/adopt-base: parse flags; require GITHUB_TOKEN (read:packages)
  → blessgen.GHCR{Package: cove-image}.DigestForTag("527-0808")     → sha256:9292…
  → adoptbase.RewriteImageBase(config.yml, sha256:9292…)            → write .at-cove/config.yml
  → blessgen.GHCR{Package: cove-base-image}.DigestForTag("527-0808")→ sha256:b675…
  → adoptbase.RewriteWatermark(watermark.txt, "527-0808", sha256:b675…, reason)
                                                                     → write watermark.txt
  → print resulting blessed head (cutover confirmation)
  → --pr: branch chore/adopt-base-527-0808 → commit → push → gh pr create
```

## Errors & verification

- **Resolution is the existence check.** A bad/absent tag fails loudly *before* any file
  is touched — no partial writes.
- **Token required.** Missing/unauthorized `GITHUB_TOKEN` (needs `read:packages`, same as
  `gen-blessed`) is a hard error with a clear message. Unlike `gen-blessed`, the tool
  **cannot** no-op offline — it has nothing to resolve without the registry.
- **Breaking-cutover confirmation.** With `--breaking`, after rewriting the watermark the
  tool prints the resulting blessed head so the operator sees the eviction took effect.
- **Egress note.** Digest resolution needs registry access (the `blessgen` client hits
  `api.github.com`). Runs host-side, or from this repo's sandbox once `ghcr.io` /
  `api.github.com` egress is in the kit and `at-cove recreate` has applied it.

## Testing

- `internal/adoptbase`: table tests for both transforms — happy path, comment
  preservation, idempotency (re-run is a no-op), and malformed-input errors.
- `internal/blessgen.DigestForTag`: exercised through the existing fake `Lister` —
  tag-present (→ index digest), tag-absent (→ error), per-arch-only (→ error).
- `cmd/adopt-base`: stays thin; the git/`gh` path is not unit-tested (matches
  `cmd/gen-blessed`).

## Docs

Add an **"Adopting a new base"** subsection to `docs/DEVELOPMENT.md` (the doc that owns
the image tree + release pipeline): the routine vs `--breaking` distinction, the
`read:packages` token requirement, and the `--pr` shortcut. The justfile recipe comment
is self-documenting. Ships as its own PR.

## Alternatives considered

- **Resolve via the ghcr.io registry v2 API** (pull-scope token, `ghcr.io` egress)
  instead of reusing `blessgen`. More broadly accessible auth, but introduces a second,
  untested registry code path. Rejected in favor of the CI-proven `blessgen` client.
- **Two recipes** (`adopt-base` + `bless-base`) instead of one with `--breaking`.
  Rejected: one recipe with an explicit `--breaking` flag keeps the surface small; the
  flag (not a defaulted behavior) keeps the dangerous edit deliberate.
- **Always rewrite both files.** Rejected: raising the watermark on a non-breaking bump
  wrongly evicts still-valid older bases from the blessed set.
