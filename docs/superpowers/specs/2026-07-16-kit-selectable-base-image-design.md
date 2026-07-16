# Kit-selectable base image + provenance-gated hardening — Design

**Date:** 2026-07-16
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Supersedes:** the original scope of [COV-34](https://linear.app/aethons-tools/issue/COV-34) ("at-cove consumes cove-image"), which this subsumes and expands.
**Builds on:** the published image tree from [COV-33](https://linear.app/aethons-tools/issue/COV-33) (`cove-base-image`/`cove-image` on GHCR, digest-pinned).
**Entangled with:** [COV-36](https://linear.app/aethons-tools/issue/COV-36) (at-cove release + at-task embed) — the embedded at-task is injected by the sealed layer, and at-cove learns the blessed base digest through the same embed mechanism.
**Leaves untouched:** [COV-35](https://linear.app/aethons-tools/issue/COV-35) (CI gate runs in `cove-image`) — a CI concern, unrelated.

## 1. Purpose

Today a kit cannot choose or extend the image at-cove hardens: the base is hardcoded in the embedded hardening `Dockerfile` (`FROM cove-base:latest`), and a kit customizes only through `config.yml image:` (`setup-scripts`, `env`, `paths`, `allowed-domains`). That blocks the multi-repo story — other repos need their *own* base — and forces kit customization through a bespoke `setup-scripts` abstraction instead of the thing everyone already knows: a Dockerfile.

This design makes the base **kit-selectable** — name an image to harden, or drop a `Dockerfile` in `image-files/` and at-cove builds it — while keeping the sealed hardening layer's security guarantees intact via a **provenance gate**: the resolved base must descend from a blessed `cove-base-image`, so hardening can trust the egress stack / `agent` user / layout it depends on. `setup-scripts` is removed; the kit's own Dockerfile expresses that customization. In short: unhide Docker, keep the boundary sealed.

## 2. Governing decisions

- **The kit picks the base; at-cove still hardens it, last.** The sealed hardening layer (nftables, squid, sshd, entrypoint, git credential helper) is applied `FROM <resolved base>` as the final step — the security boundary is unchanged. The kit adds *under* it, never over it.
- **Two ways to specify a base, mutually exclusive:**
  - `image-files/Dockerfile` present → at-cove builds it (context = `image-files/`) → uses its digest.
  - `config.yml image.base` tag set → use that image.
  - **Both present → hard error.**
  - **Neither → default `cove-base-image@<digest>`** (the lean floor — not `cove-image`, whose go/java/chrome toolchain a generic worker sandbox doesn't need).
- **Provenance gate.** The resolved base must **descend from a blessed `cove-base-image`** (§4). A non-descendant is rejected — *unless* `--i-know-what-im-doing` is passed, which downgrades the rejection to a loud warning (deliberate friction relief during rapid change).
- **`setup-scripts` is removed.** Kit build-time customization moves into the kit's own Dockerfile.
- **Session env moves to a well-known file, not `ENV`.** A Dockerfile `ENV` never reaches SSH sessions (they read `/etc/environment` via `pam_env`). So the kit writes a well-known `.env` file that the sealed layer folds into `/etc/environment`, exactly as `apply-image-env.sh` does today — `PATH` is just an entry in it.
- **`allowed-domains` stays in `config.yml`.** It feeds the sealed squid allow-list, a hardening concern, not a kit-Dockerfile one.

## 3. The kit config change

`internal/kit/config.go` — `ImageConfig`:

```
 type ImageConfig struct {
-    SetupScripts   []string          `yaml:"setup-scripts"`
-    Paths          []string          `yaml:"paths"`
-    Env            map[string]string `yaml:"env"`
+    Base           string            `yaml:"base"`            // image ref to harden; mutually exclusive with image-files/Dockerfile
     AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list (unchanged)
 }
```

`env`/`paths` leave the schema; they are expressed through the well-known `.env` file (§5). `setup-scripts` leaves entirely.

## 4. Base resolution and the provenance gate

**Resolution (in `at-cove build`, before the hardening build):**

1. If `image-files/Dockerfile` **and** `image.base` are both set → error.
2. If `image-files/Dockerfile` exists → `docker build` it with context `image-files/`; capture the resulting image digest.
3. Else if `image.base` is set → resolve that ref to a digest.
4. Else → `cove-base-image@<blessed-digest>`.

**Provenance check** — the resolved base must descend from a blessed `cove-base-image`:

- An OCI image config carries `rootfs.diff_ids`: the ordered digests of its uncompressed layers. An image built `FROM X` has **X's `diff_ids` as the exact prefix** of its own. So `descends_from(B, A)` ⟺ `A.diff_ids` is a prefix of `B.diff_ids`.
- at-cove reads the resolved base's `diff_ids` (`docker inspect … .RootFS.Layers`, **per arch**) and asserts a blessed `cove-base-image`'s layers form the prefix.
- This can't be forged: matching the prefix means those bottom layers *are* cove-base-image, byte-for-byte. `cove-image` passes naturally (it is `FROM cove-base-image`). A `FROM ubuntu`/`FROM scratch` base fails.
- **Why it matters:** not to contain a malicious kit (the sealed layer re-asserts nftables/squid/sshd on top, and the egress lock contains the rest) — but so the hardening steps can **trust** their prerequisites (the egress stack, the `agent` user, the expected paths) are present and unmodified at the floor, instead of probing for them.
- **Failure mode:** reject, unless `--i-know-what-im-doing` → warn-and-continue.

**Where at-cove learns the blessed digest(s).** at-cove **embeds** the `cove-base-image` digest it was built against (via the COV-36 embed path) and fetches that image's `diff_ids` to compare against.

- *Open decision (explore, willing to bail):* accept exactly **one** blessed digest, or a small **rolling set** (kits whose bases were built on an earlier `cove-base-image` publish still descend from *that* digest, not the current one). A set is more merciful across republishes but harder to implement — explore it, and fall back to a single embedded digest if it proves costly.

## 5. Session env via a well-known `.env` file

- The kit provides a well-known `.env` file (exact path/format — e.g. `image-files/.cove-env` — is an open detail to settle first in implementation).
- The sealed layer reads it and writes the entries into `/etc/environment`, so `pam_env` exposes them to every SSH session — the same job `apply-image-env.sh` does today for `config.yml`'s `env`/`paths`.
- `PATH` is a well-known entry in that file, handled as today.
- The kit's Dockerfile must **not** rely on `ENV` for session env (it never reaches PAM); the file is the mechanism.

## 6. What changes in the assemble/hardening layer

- **`internal/kit/config.go`** — the `ImageConfig` schema change (§3) + validation (base/Dockerfile mutual exclusion).
- **`internal/assemble/`** — a new pre-hardening stage: detect `image-files/Dockerfile`, build it, resolve the base digest, run the provenance gate; thread the resolved base into the hardening `Dockerfile` as a build ARG (`FROM ${BASE}`).
- **Hardening `Dockerfile`** — `FROM cove-base:latest` → `FROM ${BASE}` (ARG, default the blessed `cove-base-image@digest`); injects the embedded at-task (COV-36).
- **`run-setup.sh`** — deleted (setup-scripts gone).
- **`apply-image-env.sh`** — retargeted from `config.yml` env/paths to the well-known `.env` file.
- **Migration** — `.at-cove/config.yml` and `kits/reference-worker/config.yml` both use `setup-scripts` today; convert each to an `image-files/Dockerfile` (+ a `.env` file if they set env/paths).

## 7. Open decisions to resolve first in implementation

1. **Kit Dockerfile vs the overlay assembly.** Today assembly merges overridable defaults + kit `image-files/` + sealed hardening files into one context, and the hardening `Dockerfile` COPYs the merged tree. Under this design there are two build stages with two contexts: (a) the kit's `Dockerfile`, context `image-files/`, produces the base; (b) the hardening `Dockerfile`, `FROM` that base, COPYs the sealed files. The open question: does `image-files/` serve as *both* the kit-build context *and* still contribute to the hardening COPY (risking double-placement), or does the kit's Dockerfile own all kit-file placement while hardening COPYs only the sealed layer (+ overridable defaults)? Recommended direction: the kit's Dockerfile owns kit content (the "unhide Docker" tradeoff — the author writes the COPYs); hardening COPYs only the sealed files + overridable defaults + reads the well-known `.env`. Confirm and pin the exact overlay boundary before coding.
2. **Blessed-digest cardinality** — one embedded digest vs a rolling set (§4). Explore the set; bail to one if hard.
3. **The `.env` file** — exact path and format (§5); it lives among the kit's overlaid files (read by the sealed layer), so it is independent of whether the base came from a tag or a kit Dockerfile.
4. **`--i-know-what-im-doing`** — flag name/scope and how loud the warning is.

## 8. Testing

- **Hermetic (unit):** config validation (base/Dockerfile mutual exclusion, defaulting); base-resolution selection logic; the provenance-prefix comparison against fixture `diff_ids` lists (descendant passes, non-descendant fails, override downgrades to warn). All drive `internal/runner.Fake` — no real Docker.
- **The provenance check's Docker-inspect and the kit-Dockerfile build** are execution behind the runner seam, tested via the `Fake` with canned inspect output; a real build is an `integration`-tagged test.
- **Migration:** the two migrated kits assemble and build to a working, hardened image (verified interactively / integration).

## 9. Scope and sequencing

Depends on COV-33 (published base — done) and interlocks with COV-36 (embed supplies both at-task and the blessed digest). Decomposes into: the config schema + validation change; the assemble base-resolution + provenance stage; the `.env`/hardening retarget + `setup-scripts` removal; and the two-kit migration — sequenced under a refined COV-34 (or fresh sub-issues) after COV-36's embed lands the digest-plumbing this relies on.

Non-goals: changing the egress/sshd threat model; a kit's ability to weaken the sealed layer (still impossible — it's applied last); public distribution of at-cove (COV-36).
