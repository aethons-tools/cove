# Kit-selectable base image + provenance-gated hardening — Design

**Date:** 2026-07-16 · **Open decisions resolved:** 2026-07-17
**Status:** Decisions resolved — ready to decompose (§9)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Supersedes:** the original scope of [COV-34](https://linear.app/aethons-tools/issue/COV-34) ("at-cove consumes cove-image"), which this subsumes and expands.
**Builds on:** the published image tree from [COV-33](https://linear.app/aethons-tools/issue/COV-33) (`cove-base-image`/`cove-image` on GHCR, digest-pinned).
**Builds on (landed):** [COV-36](https://linear.app/aethons-tools/issue/COV-36) (at-cove release + at-task embed) — **done**. The embed path this design consumes now exists: `internal/attask/bin/at-task-linux-{amd64,arm64}` (the sealed layer injects these) and `internal/basedigest` (`Blessed() []string`, embedded from `blessed-digests.txt`, auto-appended on each `cove-base-image` publish per [COV-37](https://linear.app/aethons-tools/issue/COV-37)). Nothing consumes them yet — COV-34 is the consumer.
**Leaves untouched:** [COV-35](https://linear.app/aethons-tools/issue/COV-35) (CI gate runs in `cove-image`) — a CI concern, unrelated.

## 1. Purpose

Today a kit cannot choose or extend the image at-cove hardens: the base is hardcoded in the embedded hardening `Dockerfile` (`FROM cove-base:latest`), and a kit customizes only through `config.yml image:` (`setup-scripts`, `env`, `paths`, `allowed-domains`). That blocks the multi-repo story — other repos need their *own* base — and forces kit customization through a bespoke `setup-scripts` abstraction instead of the thing everyone already knows: a Dockerfile.

This design makes the base **kit-selectable** — name an image to harden, or drop a `Dockerfile` in the kit's `image/` directory and at-cove builds it — while keeping the sealed hardening layer's security guarantees intact via a **provenance gate**: the resolved base must descend from a blessed `cove-base-image`, so hardening can trust the egress stack / `agent` user / layout it depends on. `setup-scripts` is removed; the kit's own Dockerfile expresses that customization. In short: unhide Docker, keep the boundary sealed.

> **Rename (folded in, sub-issue 1):** the kit's author-facing directory is renamed `image-files/` → **`image/`**. Under this design it stops being an overlay of files and becomes purely a Docker build context, so `image-files` is a misnomer. This is the *kit's* directory only; the repo-internal sealed template tree (`internal/assemble/hardening/image-files/`) is untouched. All references below use the new name.

## 2. Governing decisions

- **The kit picks the base; at-cove still hardens it, last.** The sealed hardening layer (nftables, squid, sshd, entrypoint, git credential helper) is applied `FROM <resolved base>` as the final step — the security boundary is unchanged. The kit adds *under* it, never over it.
- **Two ways to specify a base, mutually exclusive:**
  - `image/Dockerfile` present → at-cove builds it (context = `image/`) → uses its digest.
  - `config.yml image.base` tag set → use that image.
  - **Both present → hard error.**
  - **Neither → default `cove-base-image@<digest>`** (the lean floor — not `cove-image`, whose go/java/chrome toolchain a generic worker sandbox doesn't need).
- **Provenance gate.** The resolved base must **descend from any blessed `cove-base-image`** (§4). A non-descendant is rejected — *unless* `--allow-unverified-base` is passed, which downgrades the rejection to a loud warning (deliberate friction relief during rapid change).
- **`setup-scripts` is removed.** Kit build-time customization moves into the kit's own Dockerfile.
- **`image/` is *only* the kit Dockerfile's build context.** It is not overlaid, not COPYed by hardening, and irrelevant when a base tag is used. If there is no kit `Dockerfile`, `image/` does nothing.
- **Session env moves to a well-known in-image drop-in directory, not `ENV`.** A Dockerfile `ENV` never reaches SSH sessions (they read `/etc/environment` via `pam_env`). So the base image carries a well-known `/etc/cove/env.d/` directory of `*.env` fragments — the base ships one, the kit's Dockerfile drops its own, or a tagged base already contains them — and the sealed layer concatenates and folds them into `/etc/environment`, exactly as `apply-image-env.sh` does today (§5). `PATH` is just a key in a fragment.
- **`allowed-domains` stays in `config.yml`.** It feeds the sealed squid allow-list, a hardening concern, not a kit-Dockerfile one.

## 3. The kit config change

`internal/kit/config.go` — `ImageConfig`:

```
 type ImageConfig struct {
-    SetupScripts   []string          `yaml:"setup-scripts"`
-    Paths          []string          `yaml:"paths"`
-    Env            map[string]string `yaml:"env"`
+    Base           string            `yaml:"base"`            // image ref to harden; mutually exclusive with image/Dockerfile
     AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list (unchanged)
 }
```

`env`/`paths` leave the schema; they are expressed through the well-known `/etc/cove/env.d/` fragments (§5). `setup-scripts` leaves entirely.

## 4. Base resolution and the provenance gate

**Resolution (in `at-cove build`, before the hardening build):**

1. If `image/Dockerfile` **and** `image.base` are both set → error.
2. If `image/Dockerfile` exists → `docker build` it with context `image/`; capture the resulting image digest. at-cove passes `--build-arg COVE_BASE_IMAGE=<blessed cove-base-image>` (from `basedigest.DefaultRef()`) so the kit Dockerfile's `FROM ${COVE_BASE_IMAGE}` builds on the blessed base (the Dockerfile's `ARG COVE_BASE_IMAGE` default is only for a bare manual `docker build`). Since this path and `image.base` are mutually exclusive, the blessed default is the only base to inject (COV-126).
3. Else if `image.base` is set → resolve that ref to a digest.
4. Else → `cove-base-image@<blessed-digest>`.

**Provenance check** — the resolved base must descend from a blessed `cove-base-image`:

- An OCI image config carries `rootfs.diff_ids`: the ordered digests of its uncompressed layers. An image built `FROM X` has **X's `diff_ids` as the exact prefix** of its own. So `descends_from(B, A)` ⟺ `A.diff_ids` is a prefix of `B.diff_ids`.
- at-cove reads the resolved base's `diff_ids` (`docker inspect … .RootFS.Layers`, **per arch**) and asserts *some* blessed `cove-base-image`'s layers form the prefix.
- This can't be forged: matching the prefix means those bottom layers *are* cove-base-image, byte-for-byte. `cove-image` passes naturally (it is `FROM cove-base-image`). A `FROM ubuntu`/`FROM scratch` base fails.
- **Why it matters:** not to contain a malicious kit (the sealed layer re-asserts nftables/squid/sshd on top, and the egress lock contains the rest) — but so the hardening steps can **trust** their prerequisites (the egress stack, the `agent` user, the expected paths) are present and unmodified at the floor, instead of probing for them.
- **Failure mode:** reject, unless `--allow-unverified-base` → warn-and-continue.

**Where at-cove learns the blessed digests — landed (COV-36).** at-cove **embeds** the blessed `cove-base-image` digests via `internal/basedigest`: `Blessed() []string` returns them newest-first from the committed, embedded `blessed-digests.txt`, which the `cove-base-image` publish appends to on each republish ([COV-37](https://linear.app/aethons-tools/issue/COV-37)). The gate fetches the resolved base's `diff_ids` and passes if **any** blessed digest's layers form the prefix.

- **Cardinality — resolved: a rolling set.** COV-36 shipped the list form (`Blessed() []string`), which subsumes the single-digest fallback the draft weighed. Accepting a set is what makes republishes merciful: a kit whose base was built on an *earlier* `cove-base-image` publish still descends from *that* still-blessed digest. The gate checks descent from any entry; no single "current" digest is privileged.

## 5. Session env via a well-known in-image `/etc/cove/env.d/` drop-in directory

> **Superseded (2026-07-18).** The `/etc/cove/env.d/` drop-in mechanism below was
> replaced by **`COVE_SSHENV`**: an image sets session env with plain `ENV` and
> names the vars in a colon-separated `COVE_SSHENV` (`PATH` intrinsic); the sealed
> layer's `apply-sshenv.sh` copies those live values into `/etc/environment`. This
> removes the footgun where an image had to maintain an env.d fragment *and* an
> `ENV` (forgetting the fragment silently dropped the toolchain from SSH sessions).
> The egress proxy vars + `CLAUDE_CONFIG_DIR` are now sealed-layer-written, not
> image-provided. The rest of this section is retained for history.

- Session env lives in a fixed, well-known *in-image* **drop-in directory `/etc/cove/env.d/`** holding `*.env` fragments, each a block of `KEY=VALUE` lines. A directory (not a single file) so the base and a kit's Dockerfile can each *contribute a fragment* without rewriting one shared file.
- The `cove-base-image` ships **`/etc/cove/env.d/00-base.env`** carrying the base-owned keys (`PATH`, `CLAUDE_CONFIG_DIR`, the proxy vars). A kit's Dockerfile adds its own fragment (e.g. `/etc/cove/env.d/50-kit.env`) for kit session env; a tagged base may already contain fragments; absent any, there is simply no kit-supplied session env.
- Because `image/` is inert during hardening (§2), fragments are **not** overlaid — they live inside the resolved base and the sealed layer reads them from that in-image path.
- The sealed layer **concatenates the fragments in lexical order and folds them into `/etc/environment`**, so `pam_env` exposes them to every SSH session — the same job `apply-image-env.sh` does today for `config.yml`'s `env`/`paths`, retargeted to the directory.
- `PATH` is a well-known key in `00-base.env`; last-writer-wins means a later fragment could shadow a base key — that is the kit's responsibility and the reason base keys are namespaced into the low-numbered `00-base.env`. (The old `baseEnvKeys` config-time guard that forbade a kit `env` from setting `PATH`/proxy goes away with the `env` field; the numbering convention replaces it.)
- The kit's Dockerfile must **not** rely on `ENV` for session env (it never reaches PAM); a fragment in the directory is the mechanism.

## 6. What changes in the assemble/hardening layer

- **`internal/kit/config.go`** — the `ImageConfig` schema change (§3): add `base`; drop `setup-scripts`/`env`/`paths` (and the now-moot `baseEnvKeys` guard); validate `base` vs a present `image/Dockerfile` are mutually exclusive (a kit-dir-aware check, since Dockerfile presence is filesystem state, not config).
- **`internal/assemble/`** — a new pre-hardening stage: detect `image/Dockerfile`, build it (context `image/`) via the runner seam, resolve the base digest (or `image.base`, or default `cove-base-image@Blessed()[0]`), run the provenance gate (`--allow-unverified-base` downgrades reject→warn); thread the resolved base into the hardening `Dockerfile` as a build ARG (`FROM ${BASE}`). Hardening **no longer overlays or COPYs the kit's `image/`** — that tree is only the kit Dockerfile's context now. (The kit directory `image-files/` → `image/` rename rides here / sub-issue 1.)
- **Overridable defaults → `cove-base-image` (decision §7 #1 = a).** `internal/assemble/overridable/` (`settings.json`, `.claude.json`, `CLAUDE.md`) is **deleted**; those files move into the `cove-base-image` Dockerfile (in `images/`). Hardening then COPYs **only** the sealed, non-overridable files. A kit overrides a default the normal way — its Dockerfile (which is `FROM` a cove-base descendant) writes its own. The build-time `.claude.json` deep-merge with the `claude install`–generated data **stays in hardening**, now sourcing the default from the in-image path (`/home/agent/.init-agent-data/.claude.json`) instead of the overlay.
- **Hardening `Dockerfile`** — `FROM cove-base:latest` → `FROM ${BASE}` (ARG, default the blessed `cove-base-image@digest`); **injects the embedded at-task** (COPY the `internal/attask/bin/at-task-linux-<arch>` binary into the sealed layer, COV-36); COPYs only the sealed files.
- **`run-setup.sh`** — deleted (setup-scripts gone).
- **`apply-image-env.sh`** — retargeted from `config.yml` env/paths to concatenating `/etc/cove/env.d/*.env` and folding into `/etc/environment` (§5).
- **Migration** — `.at-cove/config.yml` and `kits/reference-worker/config.yml` both use `setup-scripts` today; convert each to an `image/Dockerfile` (+ an `/etc/cove/env.d/` fragment written by that Dockerfile if they set env/paths).

## 7. Resolved decisions (2026-07-17)

The four open decisions the draft deferred are now settled:

1. **Overridable-defaults placement → (a) ship in `cove-base-image`.** The defaults leave `at-cove` (`internal/assemble/overridable/` deleted) and move into the `cove-base-image` Dockerfile; hardening COPYs only sealed files; a kit overrides by writing its own in its Dockerfile. Keeps hardening purely sealed and makes overrides a normal Dockerfile concern. This carries a companion edit to the `cove-base-image` tree — folded into sub-issue 3 (§9), not a separate COV-33 ticket.
2. **Blessed-digest cardinality → a rolling set.** Already landed by COV-36 (`basedigest.Blessed() []string`); the gate accepts descent from any entry (§4).
3. **In-image session-env path → `/etc/cove/env.d/` drop-in directory** of `KEY=VALUE` `*.env` fragments, concatenated in lexical order and folded into `/etc/environment`; base ships `00-base.env` (§5).
4. **Escape hatch → `--allow-unverified-base`**, which downgrades the provenance rejection to a loud, multi-line stderr warning naming the unverified digest, then proceeds. **Interim placement:** because image-build is inline today, the flag rides the current build-triggering commands (`create`/`recreate`/`work`/`dispatch`); the discrete-`install` effort ([COV-38](https://linear.app/aethons-tools/issue/COV-38)) later consolidates build + gate + flag onto `at-cove install`, the single place a base is resolved and built. The gate logic lives in `internal/assemble`, so that relocation is cheap.

## 8. Testing

- **Hermetic (unit):** config validation (base/Dockerfile mutual exclusion, defaulting); base-resolution selection logic; the provenance-prefix comparison against fixture `diff_ids` lists (descendant passes, non-descendant fails, override downgrades to warn). All drive `internal/runner.Fake` — no real Docker.
- **The provenance check's Docker-inspect and the kit-Dockerfile build** are execution behind the runner seam, tested via the `Fake` with canned inspect output; a real build is an `integration`-tagged test.
- **Migration:** the two migrated kits assemble and build to a working, hardened image (verified interactively / integration).

## 9. Scope and sequencing

Both prerequisites are in place: COV-33 published the base (`blessed-digests.txt` carries a real digest) and COV-36's embed landed the at-task binaries + `basedigest`. COV-34 is the consumer. It decomposes into **four sub-issues, all interactive (`class:planner`)** — the work touches the security boundary and spans multiple PRs, so each runs in chat with CI/build in the loop, not a one-shot worker. The ordering keeps `main` green at every PR (no kit breaks before it is migrated):

1. **Config schema: add `image.base` + mutual-exclusion validation + rename `image-files/` → `image/`.** *(Go, hermetic.)* Add `Base`; validate `base` vs a present `image/Dockerfile` are mutually exclusive (kit-dir-aware); rename the kit directory `image-files/` → `image/` throughout `internal/assemble` + docs (the rename is cheap now and lets all downstream base-detection code use the new name from the start). Keep `setup-scripts`/`env`/`paths` for now so existing kits still parse. Docs: OVERVIEW kit-format.
2. **Base resolution + provenance gate.** *(Go + runner seam; hermetic + integration.)* The resolver (build kit Dockerfile by digest → `image.base` → default `cove-base-image@Blessed()[0]`); the `diff_ids` prefix check against `basedigest.Blessed()` (any entry, per-arch); `--allow-unverified-base` → warn (on the current build-triggering commands — interim per [COV-38](https://linear.app/aethons-tools/issue/COV-38)). Introduces the `FROM ${BASE}` ARG defaulting to the blessed digest, so the produced image is unchanged. Purely additive — nothing removed yet.
3. **Hardening rewire + overridable→base + at-task inject + `env.d`.** *(Security boundary; interactive/integration.)* `FROM ${BASE}`; stop overlaying kit `image/`; move the overridable defaults into `cove-base-image` and delete `internal/assemble/overridable` (hardening COPYs sealed-only; preserve the `.claude.json` install-merge from the in-image path); inject the embedded at-task; delete `run-setup.sh`; retarget `apply-image-env.sh` to fold `/etc/cove/env.d/*.env` (base ships `00-base.env`). The `cove-base-image` content edit is folded in here (one coherent behavior change), not tracked separately under COV-33. Real build validates.
4. **Migrate the two kits + retire the old fields.** *(Interactive/integration.)* Convert `.at-cove` + `kits/reference-worker` from `setup-scripts`/`env`/`paths` to an `image/Dockerfile` (+ an `env.d` fragment); *then* remove those three fields from the schema. Verify each builds to a working hardened image.

**Deferred follow-ons (surfaced while planning, out of scope here):**

- **[COV-38](https://linear.app/aethons-tools/issue/COV-38) — discrete `at-cove install`.** Image-build becomes a separate, cached step that `create`/`work`/`dispatch` consume, and it becomes the sole home of the provenance gate + `--allow-unverified-base` (§7 #4). COV-34 ships the interim placement; COV-38 consolidates.
- **[COV-39](https://linear.app/aethons-tools/issue/COV-39) — per-class `allowed-domains`.** `allowed-domains` restructured like secrets (root + `<common>` + per worker/collaborator, merged), which forces resolving build-time-baked vs session-time egress. Needs its own brainstorm; entangled with COV-38. COV-34 keeps the single `image.allowed-domains`.

Non-goals: changing the egress/sshd threat model; a kit's ability to weaken the sealed layer (still impossible — it's applied last); public distribution of at-cove (COV-36); the two deferred follow-ons above.
