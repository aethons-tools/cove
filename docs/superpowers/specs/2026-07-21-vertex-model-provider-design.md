# Claude on Vertex — a `model-provider` for at-cove — Design

**Date:** 2026-07-21
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove`
**Epic:** TBD (to be filed on the board)
**Builds on:** the sealed squid egress layer (COV-34) and the per-class session egress (COV-39) this widens; the `install`/`install.json` manifest (COV-38) that freezes run-config; the demand/supply secret model and `at-mint` minters; and the `chat` OAuth seed/save credential machinery this branches from.

## 1. Purpose

Today every at-cove sandbox authenticates its agent to **Anthropic's first-party API**: interactive `chat` via subscription OAuth (a rolling `credentials.json` seeded onto the `/agent-data` volume), dispatched `work` via a short-lived `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY` bearer. There is no way to point the agent at **Claude on Google Vertex AI** instead — an org that runs Claude through its GCP project, billed and governed by Google Cloud IAM, cannot use at-cove.

This design adds a **`model-provider` block** to the kit that switches a sandbox from first-party Anthropic to Vertex. It is scoped **chat-first** (the immediate need), with the worker path designed here but built second. The sealed hardening layer is untouched: Vertex support only *widens* egress additively and *branches* the credential flow — it never weakens the egress lock, the secret model, or the fail-closed posture.

**Requirements:**
- A kit (or a collaborator/worker class) can declare it targets Vertex, naming its GCP project and region.
- `chat` on a Vertex kit authenticates the agent via **GCP Application Default Credentials (ADC)** instead of Anthropic subscription OAuth, and never runs `claude auth login --claudeai`.
- The egress allow-list gains the GCP endpoints Vertex needs, **derived automatically** from the provider block — no hand-maintained domain list, no sealed-layer edit.
- The GCP credential reaches the VM from the **human's existing host identity** (assume host ADC / gcloud; at-cove mints from it) — at-cove never drives Google's interactive login.
- The worker path is designed consistently (provider-aware fail-closed gate, short-lived host-minted credential) but implemented after chat.
- Never weaken the threat model: sealed squid still wins; the agent workload cannot alter its own egress or credential; secrets stay off argv/logs.

## 2. Pivotal constraint — Claude Code's Vertex credential surface

Claude Code (the CLI the sandbox runs) consumes GCP credentials **only** as an **ADC file** pointed to by `GOOGLE_APPLICATION_CREDENTIALS`, resolved by the google-auth library. There is **no** environment variable that accepts a bare, pre-obtained GCP access token (`GOOGLE_OAUTH_ACCESS_TOKEN` and the like do not exist for it). Claude Code also exposes a `gcpAuthRefresh` settings.json field — a command it runs **in-VM** to regenerate credentials on expiry (stderr output, 3-minute timeout, no interactive input).

Consequences that shape the design:

- **A one-shot injected access token cannot be the chat mechanism.** A chat session is long-lived; a short-lived token injected once would expire mid-session with no re-injection channel, and at-cove has no host↔VM callback mid-session.
- **The natural chat credential is an `authorized_user` ADC file** — exactly what `gcloud auth application-default login` produces (a refresh token + client id/secret). google-auth **refreshes it in-VM** against `oauth2.googleapis.com`, so it survives a long session. This maps directly onto at-cove's existing `credentials.json` seed/save/rolling-refresh machinery.
- **The short-lived-credential model still fits the worker path**, which is one-shot: mint a scoped, short-lived ADC host-side, TTL just covering the run, delivered as a tmpfs file — the same shape as today's Anthropic worker bearer.

So the two sandbox modes want two credential shapes, which is *why* chat-first is the right sequencing.

Confirmed Vertex env vars Claude Code reads: `CLAUDE_CODE_USE_VERTEX=1`, `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION` (accepts a specific region, a multi-region `us`/`eu`, or `global`), optional `ANTHROPIC_VERTEX_BASE_URL`, optional per-model `VERTEX_REGION_CLAUDE_*` overrides.

## 3. Principles

- **Provider presence is the switch.** A kit with a `model-provider.vertex` block is a Vertex kit; without it, nothing changes and the Anthropic paths are byte-for-byte as today.
- **Branch, don't fork.** Vertex reuses the existing credential-seed transport (`chat`), the env-file injection, the `install.json` freeze, and the per-class egress apply. It adds one branch at the auth step and one derived domain set — not a parallel pipeline.
- **Sealed-wins, additive egress.** The GCP domains are added to the kit-root allow-list; the sealed base and `nftables` lock are unchanged. Egress can only widen.
- **Configure via env, deny the sealed keys.** Provider *configuration* is entirely env-driven, so the block's payload is an `env` map. But the map is **kit-authored with no host-side gate** (unlike a secret), so it may set only non-protected keys — a hardening denylist keeps it from shadowing sealed-owned vars. This is the env-block analog of "additive, sealed-wins."
- **Host-side identity, in-VM refresh (chat).** at-cove mints the ADC from the human's host identity; google-auth keeps it alive in-VM. at-cove never drives Google's login UX.
- **Frozen source.** The provider config is read from the currency-pinned `install.json`, not a live `config.yml` — "install froze it" still holds.

## 4. Config schema — the `model-provider` block

A new top-level, non-secret wiring block in `config.yml`, modeled like `source-control`/`tracker` (a union keyed by provider name). Because Claude Code's provider *configuration* is entirely environment-driven (§2), the provider member's payload is an **`env` map of non-secret key/value pairs** rather than a fixed set of typed fields: at-cove demands the keys Vertex requires and passes any other (non-protected) key straight through, so a new Claude Code env knob needs no schema change.

```yaml
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-gcp-project    # required
      CLOUD_ML_REGION: us                       # required (region | "us" | "eu" | "global")
      # CLAUDE_CODE_USE_VERTEX=1 is set by at-cove — implied by the vertex block
      ANTHROPIC_VERTEX_BASE_URL: https://…            # optional pass-through
      VERTEX_REGION_CLAUDE_HAIKU_4_5: us-central1     # optional pass-through
      ANTHROPIC_MODEL: claude-opus-4-8                # optional pass-through
```

Parsed and validated in `internal/kit/config.go`:
- `vertex` is the only provider member for now; the union shape (a named member carrying an `env` map + a required-key set + a derived domain set — §5) leaves room for `bedrock` etc. without reshaping.
- **Required keys:** `ANTHROPIC_VERTEX_PROJECT_ID` and `CLOUD_ML_REGION` must be present; a missing one is a hard config error. at-cove **sets** `CLAUDE_CODE_USE_VERTEX=1` itself (implied by choosing `vertex`), so the kit need not state it.
- **Hardening denylist (load-bearing).** The `env` map is **kit-authored with no host-side supply gate** — unlike a *secret*, which the kit only *demands* and the machine *supplies* (the operator is the gate). So a committed, untrusted kit could otherwise inject security-relevant env directly. The block may therefore set only non-protected keys; a **protected set is rejected at config validation** (fail loud) **and defensively dropped at injection**:
  - the egress proxy vars (`http_proxy`/`https_proxy`/`no_proxy` and uppercase) — the per-session env-file is *sourced* in the session shell, so an unchecked value would **shadow** the sealed `/etc/environment` proxy vars the hardening layer writes last, quietly defeating egress;
  - `CLAUDE_CONFIG_DIR` (sealed-owned);
  - `GOOGLE_APPLICATION_CREDENTIALS` (at-cove owns it — it points at the seeded ADC file, §6);
  - `PATH` (and any other sealed-owned var the impl enumerates).
  This is the env-block analog of the egress rule "additive, sealed-wins": a kit can *configure* the provider but can never touch a sealed-owned or security-relevant variable.
- **Non-secret only.** The `env` map carries wiring, never credentials — consistent with the config trust boundary. The GCP *credential* is supplied host-side and seeded as a **file** (§6); the env map only needs the at-cove-owned `GOOGLE_APPLICATION_CREDENTIALS` *pointer*, which is protected (not kit-set).
- Absent block → first-party Anthropic (unchanged).

Frozen into `install.json` as resolved run-config (`internal/install`), read by `chat`/`work` like every other run-config field. Whether the block may also live per-collaborator / per-worker-class (so one kit mixes providers across classes) is an **open question** (§9) — v1 targets a kit-global block.

## 5. Egress — auto-derived GCP domains

When `model-provider.vertex` is present, install injects a GCP domain set into the kit-root allow-list (baked into `allowed_domains.kit.txt`), via a new pure helper `providerDomains(cfg) []string`. The set:

| Domain | Why |
|---|---|
| `aiplatform.googleapis.com` + regional/multi-region hosts (`aiplatform.us.rep.googleapis.com`, `aiplatform.eu.rep.googleapis.com`, `<region>-aiplatform.googleapis.com`) | Vertex inference endpoint |
| `oauth2.googleapis.com` | in-VM ADC token refresh (authorized_user) |
| `sts.googleapis.com`, `iamcredentials.googleapis.com` | WIF / service-account impersonation (worker path, and any WIF-based ADC) |

The exact host derivation from `env.CLOUD_ML_REGION` is settled during implementation (region-templated vs. a fixed superset); if `env.ANTHROPIC_VERTEX_BASE_URL` is set, its host is derived and added too (§9). The sealed base list and `squid.conf` are **not** touched — this is purely the kit-root (additive) tier that already exists. The agent workload still cannot widen its own egress.

## 6. Credential flow — chat (built first)

`chat`'s auth step (`internal/connect`, the Authentication data flow) gains a provider branch. On a Vertex kit:

1. **Resolve the GCP ADC host-side** from the human's existing identity — *assume host ADC, mint from it*. Delivered as a new `at-mint` **`vertex` provider** (`internal/mint`) referenced from `secrets.yml` via a `{ mint: <name> }` supply, or a bare host-side resolver `command:`. It reads/derives an `authorized_user` ADC from `~/.config/gcloud/application_default_credentials.json` (the artifact of `gcloud auth application-default login`). at-cove never invokes `gcloud auth login` itself.
2. **Seed the ADC file onto the `/agent-data` volume — seed-only, no save-back.** Seed it before launch and set `GOOGLE_APPLICATION_CREDENTIALS` to the seeded path. Unlike the Anthropic `credentials.json` (which stores the live access token and is rewritten on rotation, hence the save-back), a `gcloud` **`authorized_user` ADC is static**: it holds a long-lived refresh token, and google-auth mints access tokens **in-VM, in memory** over the newly-allowed `oauth2.googleapis.com` without rewriting the file. So a long chat survives token expiry with no save-back path — the file never changes.
3. **Skip the Anthropic OAuth flow entirely** — no `claude auth status` probe, no `claude auth login --claudeai`, no `credentials.json` seed — for a Vertex session.
4. **Inject the non-secret Vertex env** via the existing env-file transport, sourced from the provider `env` map (§4) rather than the secret store: at-cove's auto-set `CLAUDE_CODE_USE_VERTEX=1`, the at-cove-owned `GOOGLE_APPLICATION_CREDENTIALS` pointer to the seeded ADC, and every non-protected key from the map. The denylist is re-checked here (defensive drop) so a protected key can never be emitted regardless of source.

**Placement decision:** the ADC file lives on the persistent `/agent-data` volume (seed-only), the same posture at-cove already documents for the saved OAuth login — a refresh-token file in a user-owned location. This is chosen over a tmpfs/memory-only delivery because a long chat needs a credential that survives token expiry; a one-shot short-lived access token would die mid-session with no re-injection channel. (An `authorized_user` ADC is static, so unlike the Anthropic login there is nothing to *save back* — seeding is the whole of the flow.)

**Trust-boundary note:** like the Anthropic subscription credentials it parallels, the seeded GCP ADC (a refresh-token file) is written to the VM volume — distinct from memory-only *secrets*. This is the same boundary the existing saved-login note documents; the design carries that note across to GCP ADC. The ADC value itself is resolved host-side through the ordinary demand/supply model (a well-known `GOOGLE_APPLICATION_CREDENTIALS_JSON` demand) and seeded as a file, never injected into the agent's session env.

## 7. Credential flow — worker (designed now, built second)

The worker path is one-shot and unattended, so it takes the short-lived-credential shape:

- **Provider-aware fail-closed gate.** Today `cmd/at-cove/main.go` (~L1342) fails a worker closed unless `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY` resolves. This gate becomes provider-aware: on a Vertex kit it instead requires the **GCP credential** to resolve (the `workers.<class>.secrets` GCP-ADC demand / `vertex` minter), and does **not** look for the Anthropic bearer. Bearer-name knowledge stays confined to the non-Vertex branch of the gate.
- **Short-lived host-minted ADC, tmpfs-delivered.** The `vertex` minter produces a scoped, short-lived ADC (e.g. an impersonated-SA or WIF `external_account` credential) whose TTL only has to cover the agent step; it is streamed into a tmpfs file (mode 600), `GOOGLE_APPLICATION_CREDENTIALS` points at it, and it is deleted on teardown — the same memory-only posture as the Anthropic worker bearer.
- **No OAuth seed** (already true on the work path), so a misconfigured Vertex worker fails closed inside the VM too, exactly like the keyless-bearer case today.
- `rejectRootBearers` is unaffected (GCP creds aren't Anthropic bearers), but the analogous "don't leak the worker credential into `chat`" segregation is preserved because a worker GCP demand lives in `workers.<class>.secrets`, which `chat` never resolves.

## 8. Code touchpoints

| Area | Change |
|---|---|
| `internal/kit/config.go` | `ModelProvider`/`Vertex` structs (`env` map + required-key set); validation incl. the **hardening denylist** (reject protected keys, fail loud) |
| `internal/install` | none — the manifest embeds `kit.Config` wholesale, so `model-provider` freezes into `install.json` automatically; and `CurrencyInputs.KitSourceTree` already hashes raw `config.yml`, so currency re-hashes with no change |
| egress derivation (`internal/assemble` / install) | `providerDomains(cfg)` — inject GCP domains into kit-root allow-list when Vertex present |
| `internal/connect` | **the main change** — provider branch in the auth flow: seed GCP ADC vs. Anthropic OAuth; inject the provider `env` map (with defensive denylist re-check) |
| `internal/mint` (`at-mint`) | new `vertex` provider (host ADC → authorized_user for chat; short-lived scoped ADC for worker) |
| `cmd/at-cove/main.go` | worker fail-closed gate becomes provider-aware (phase 2) |
| base-image managed settings | possibly wire `gcpAuthRefresh` as a refresh fallback (evaluated in impl; not required for v1 chat) |
| `docs/` | OVERVIEW Authentication + egress sections; `at-cove-config.md` (provider block); `at-cove-secrets.md` + `at-mint.md` (`vertex` minter) |

## 9. Open questions (resolve during planning/impl)

- **Scope of the block:** kit-global only (v1) vs. per-collaborator / per-worker-class, so one kit mixes Anthropic and Vertex classes.
- **Denylist membership:** the exact protected-key set (proxy vars, `CLAUDE_CONFIG_DIR`, `GOOGLE_APPLICATION_CREDENTIALS`, `PATH`, …) and whether it is provider-specific or a single sealed-owned list shared by any future `env`-bearing block — a hardening call for sign-off.
- **Region → endpoint derivation:** template the regional aiplatform host from `env.CLOUD_ML_REGION`, or allow-list a fixed GCP superset; and whether a custom `env.ANTHROPIC_VERTEX_BASE_URL` host is folded into the derived egress set.
- **`gcpAuthRefresh` role:** whether at-cove sets it (a re-mint command baked into the image) as a refresh fallback, given the in-VM command cannot reach the host minter — likely unused in v1 since google-auth refreshes the seeded authorized_user file itself.
- **Worker credential concretes:** which short-lived GCP credential type (impersonated SA vs. WIF external_account) the `vertex` minter emits for the work path.
- **Model defaults:** whether at-cove pins `ANTHROPIC_MODEL` from `model:` or leaves Claude Code's Vertex model defaults.

## 10. Non-goals

- Driving Google's interactive login (`gcloud auth application-default login`) from at-cove — the human establishes host ADC themselves.
- Bedrock or other providers — the union shape anticipates them, but only `vertex` is specified here.
- Any change to the sealed hardening layer (nftables, squid base, sshd, entrypoint).
- Mixing first-party Anthropic and Vertex within a single session.
