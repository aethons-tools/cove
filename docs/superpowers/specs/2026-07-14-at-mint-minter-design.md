# `at-mint` + demand/supply secret model — Design

**Date:** 2026-07-14
**Status:** Proposed (pre-implementation)
**Repo:** `github.com/aethons-tools/cove` (binaries `at-cove`, `at-task`; new: `at-mint`)
**Builds on:** the unified kit config (`2026-07-10-unified-kit-config-design.md`), the run-param passthrough + reference minter (`2026-07-10-minter-run-param-passthrough-design.md`), and the user-secrets model (`2026-06-27-user-secrets-design.md`). Founded on the WIF end-to-end proof (Auth0 client-credentials → Anthropic federation → `sk-ant-oat01`, verified 2026-07-14).

## 1. Purpose

Two coupled changes.

**First, formalize minting into a tested binary.** The GitHub token minter is today an ad-hoc shell script (`kits/reference-worker/mint-github-token.sh`) shipped in the kit. Replace it with `at-mint`, a real Go binary in the `at-cove` family, with a provider subcommand per token type (`at-mint github`, `at-mint anthropic`). This adds the second minter we now need — an Anthropic Workload Identity Federation token, minted host-side from an Auth0 client-credentials JWT — without a second shell script, and makes both minters unit-testable.

**Second, invert the secret model from *kit-supplies* to *kit-demands*.** Today the source-controlled kit carries the resolver `command:` (e.g. `["mint-github-token.sh"]`), so the kit reaches *up* into machine-level information. With many repos in play this couples them (a shared `~/.config` that can't tell who is asking) or forces machine-level names (`--profile foo`) into checked-in files. Flip the arrow: **the kit only declares the secrets it demands; the machine supplies them, per-kit, out of source control.** `at-mint` becomes the tool the machine-side supply invokes — never named by a kit.

## 2. Governing decisions

- **Kit = demand only.** A kit's `secrets:` entries are a name (the key) + a `description`. No command, no IDs, no profile, no provider. Fully portable; safe in source control; a kit can *ask* but never *dictate how* a secret is produced.
- **Machine = supply, keyed explicitly.** Two host-side files under `~/.config/at-cove/` (honoring `XDG_CONFIG_HOME`): `secrets.yml` (primary) and `secrets.local.yml` (escape hatch). Neither is ever checked in.
- **Four supply sources; exactly one per entry:** `value:` (literal), `command:` (host argv), `global: <name>` (delegate to a named shared supply), `mint: <profile>` (mint via a named minter profile).
- **`global:` and `minters:` are inert libraries.** They are *never* matched by demand name; they are reached *only* through an explicit `global:`/`mint:` reference written under a specific kit (or path). This preserves the core invariant: **a kit receives a secret only through an entry an operator explicitly authored under that kit.** An unmatched demand fails closed. Nothing can be name-squatted or "mined" by a malicious kit.
- **Precedence:** `secrets.local.yml` (keyed by canonical kit path) → `secrets.yml` `kits:` (keyed by kit `name`) → **fail-closed**. The escape hatch is keyed by path precisely because its job is to disambiguate name collisions; it is out of tree, so it may carry commands and needs no provenance guard. Repo-move sensitivity is acceptable for a non-primary escape hatch.
- **`at-mint`: env for secrets, flags for identifiers.** Non-secret parameters are command-line flags (visible, greppable); secret material is env-only, never on argv. A path to a secret file is non-secret and stays a flag; the content it points to is the secret. `at-mint` prints exactly one token to stdout and nothing else; any error is fail-closed (non-zero exit, message on stderr, no stdout).
- **at-cove owns the profile→invocation translation; `at-mint` owns only provider mechanics.** When resolving `{ mint: <profile> }`, at-cove loads the profile, resolves its secret-valued fields with the *existing* resolver, then runs `at-mint <provider>` with non-secret fields as flags and resolved secrets as env (plus `COVE_RUN_*`), and captures stdout. `at-mint` never reads a config file. This keeps at-cove's provider-awareness to "run `at-mint <provider>` and pass fields through," and makes `at-mint` a pure function of flags+env.
- **The air-gap is unchanged.** The GitHub token is minted fresh immediately before each `at-task` git step and withheld from the agent step (structurally separate from the agent's env). The Anthropic bearer is an *agent* credential (`ANTHROPIC_AUTH_TOKEN`), resolved into the agent env; `ANTHROPIC_API_KEY` is unset on the worker so it cannot shadow.
- **No new dependencies.** JWT signing (RS256) and the HTTPS calls use the standard library (`crypto/rsa`, `crypto/x509`, `encoding/json`, `net/http`); YAML stays `gopkg.in/yaml.v3`. Real code-host / Auth0 / Anthropic round-trips are behind the `integration` build tag (maintainer-run); unit tests are hermetic over an injected HTTP seam.

## 3. The two sides

### 3a. Kit (source-controlled) — demand only

```yaml
# <repo>/.at-cove/config.yml
source-control:
  github:
    project: aethons-tools/cove
    secrets:
      AT_TASK_GIT_TOKEN: { description: "push + PR token for this repo, minted per git step" }
secrets:
  ANTHROPIC_AUTH_TOKEN: { description: "short-lived Anthropic bearer for the worker agent" }
```

The kit names *what* it needs and *why*. Nothing about *how*.

### 3b. Machine (host, never committed) — supply

```yaml
# ~/.config/at-cove/secrets.yml
minters:                             # profiles — how an identity mints; inert until referenced
  gh-cove:
    github:                          # provider tag (tagged union)
      app-id: "123"
      install-id: "789"
      app-key: /etc/cove/gh-A.pem    # path flag; or {value|command|global} to supply content
  anthropic-orgA:
    anthropic:                       # provider tag (tagged union)
      oidc:                          # IdP tagged union — mints the upstream JWT
        auth0:
          tenant: cove.us.auth0.com
          client-id: abc
          client-secret: { command: ["pass","cove/auth0-A"] }   # value|command|global
          audience: urn:cove:anthropic-wif
      federation:                    # Anthropic exchange — constant across IdPs
        org: <org-A-uuid>
        rule: fdrl_A                 # the federation rule mapping this identity -> the service account
        service-account: svac_A
        workspace: wrkspc_A          # optional; omit / "default" when the rule spans one workspace
        # oat lifetime is provisioned on the rule (token_lifetime_seconds), not passed at mint

global:                              # named shared supplies; inert until delegated
  shared-tracker: { command: ["gh","auth","token"] }

kits:                                # per-kit authorization: demand -> source (exactly one source key)
  cove:
    AT_TASK_GIT_TOKEN:         { mint: gh-cove }
    ANTHROPIC_AUTH_TOKEN:      { mint: anthropic-orgA }
    AT_DISPATCH_TRACKER_TOKEN: { global: shared-tracker }
  special-repo:
    ANTHROPIC_AUTH_TOKEN:      { mint: anthropic-orgB }
    AT_DISPATCH_TRACKER_TOKEN: { global: shared-tracker }
```

```yaml
# ~/.config/at-cove/secrets.local.yml   (escape hatch; keyed by canonical kit path)
kits:
  /home/me/checkouts/cove:
    ANTHROPIC_AUTH_TOKEN: { value: "sk-ant-oat01-…test…" }   # temp override for testing
minters:                                                     # may also override a profile per path
  anthropic-orgA:
    anthropic:
      oidc: { auth0: { tenant: test.us.auth0.com, client-id: t, client-secret: { env: TEST_SECRET }, audience: urn:test } }
      federation: { org: <test-org>, rule: fdrl_test, service-account: svac_test }
```

## 4. Resolution semantics

For each secret name **S** demanded by kit **K** (at canonical path **P**):

1. **`secrets.local.yml` → `kits[P][S]`** — if present, use it (highest precedence).
2. **`secrets.yml` → `kits[K.name][S]`** — else if present, use it.
3. otherwise **fail closed** (an unresolved demand aborts *before* the VM is built).

The chosen entry has exactly one source key, resolved as:

- **`value: X`** → the literal `X`.
- **`command: [...]`** → run the host argv (via `runner.OutputEnv` with `COVE_RUN_*`), trim trailing newline, use stdout.
- **`global: N`** → look up `global[N]` (a `value`/`command` supply) and resolve it. `global[N]` is *only* reachable this way. `secrets.local.yml` may define its own `global:` consulted first.
- **`mint: M`** → expand the minter profile `minters[M]` (see §5). `minters[M]` is *only* reachable this way; `secrets.local.yml` `minters:` (by path) override `secrets.yml` `minters:`.

`global:`/`minters:`/`value:`/`command:` retain the existing in-memory, never-argv, fail-closed guarantees. Validation: every `kits[...]` and `global[...]` entry has exactly one source key; a `mint:`/`global:` reference to a missing profile/supply is a load-time error.

## 5. `mint:` expansion

`{ mint: M }` on a demand for kit K resolves as:

1. Load `minters[M]` (path-scoped override first, then `secrets.yml`). Validate the provider tagged union — exactly one of `github:`/`anthropic:` (and, for `anthropic`, exactly one `oidc:` IdP).
2. **Resolve the profile's secret-valued fields** — any field whose YAML value is a source object (`{value|command|global}`) is resolved to a literal with the existing resolver. Plain scalars pass through untouched. at-cove need not know a field's *meaning*, only whether it is a source object.
3. Build the invocation: run `at-mint <provider>` with **non-secret fields as flags** and **resolved secret values as env**, alongside the run env (`COVE_RUN_REPO`, `COVE_RUN_ISSUE`, `COVE_RUN_CLASS`, `COVE_RUN_TIMEOUT`).
4. Capture stdout as S's value (trim trailing newline). Everything is in-memory; no secret ever touches argv, a file, or a log.

Because scope is fixed *in* the profile + `COVE_RUN_REPO` (never from kit-declared commands or issue text), the "untrusted brief cannot widen scope" property of the prior minter design is preserved.

## 6. `at-mint` — the binary

`cmd/at-mint/main.go`, dispatched through the existing CLI command registry. One provider subcommand each. Contract: **flags = non-secret, env = secret, one token to stdout, fail-closed.**

### 6a. `at-mint github`

Mints a short-lived GitHub App installation token scoped to a single repo (contents + PR write) — the Go form of today's shell script.

- Flags: `--app-id`, `--install-id`, `--app-key-file <path>` (the PEM path; a path is non-secret).
- Env: `AT_MINT_GITHUB_APP_KEY` (PEM *content*, the alternative to `--app-key-file`); `COVE_RUN_REPO` (the `owner/name` to scope to).
- Flow: build a `RS256` App JWT (iat−60, exp+~9m, iss=app-id) signed with the key; `POST /app/installations/<install-id>/access_tokens` with `{"repositories":["<name>"],"permissions":{"contents":"write","pull_requests":"write"}}`; print `.token`.

### 6b. `at-mint anthropic`

Mints a short-lived Anthropic `sk-ant-oat01` bearer via Auth0 client-credentials → Anthropic federation.

- OIDC (Auth0) flags: `--auth0-tenant`, `--auth0-client-id`, `--auth0-audience`.
- Federation flags: `--anthropic-org`, `--anthropic-rule` (`fdrl_…`), `--anthropic-service-account` (`svac_…`), `--anthropic-workspace` (optional).
- Env: `AT_MINT_AUTH0_CLIENT_SECRET`.
- Flow — hop 1: `POST https://<tenant>/oauth/token`, grant `client_credentials` (client-id/secret + audience) → an RS256 JWT. hop 2: `POST https://api.anthropic.com/v1/oauth/token`, grant `urn:ietf:params:oauth:grant-type:jwt-bearer`, body `{assertion:<JWT>, federation_rule_id:<rule>, service_account_id:<svac>, organization_id:<org>[, workspace_id:<ws>]}` → print `access_token` (`sk-ant-oat01-…`). The oat lifetime is provisioned on the federation rule, not passed at mint.

The IdP is a seam: a future `oidc.okta`/`oidc.projected-token` adds a hop-1 variant; hop 2 is unchanged.

### 6c. Testability

`at-mint` takes an injectable HTTP doer (interface with a single `Do(*http.Request)`), defaulting to `net/http`; unit tests inject a fake that asserts request shape and returns canned responses. RS256 signing uses a test key. Hermetic tests cover: flag/env parsing, the two flows' request construction, `sk-ant-oat01`/`.token` extraction, fail-closed on every error branch, and **no secret on argv** (the process argv never contains the client secret or PEM content). Real round-trips are `integration`-tagged.

## 7. Component changes (summary)

- **`cmd/at-mint/` (new)** — `main.go` + `github.go` + `anthropic.go` + an HTTP seam; registered in the CLI registry.
- **`internal/usersecret`** — extend `Store` from a flat `map[name]Entry` to the sectioned model (`minters`, `global`, `kits`) across `secrets.yml` + `secrets.local.yml`; `Plan(kitName, kitPath, demanded)` implementing §4 precedence and the four sources; the source-object union and tagged-union `minters` types with `Active()` validators.
- **`internal/secret`** — unchanged core (`Resolve`, `OutputEnv`); it gains a caller that expands a resolved `mint:` profile into an `at-mint` argv+env (this may live in a small new `internal/mint` assembler that depends on `secret`+`usersecret`, keeping `at-cove`'s `main.go` thin).
- **`internal/kit/config.go`** — kit `secrets:` become demand-only (`description` only); **drop the kit-side `command:`**; retire `GitTokenSpec()`'s command plumbing (the git token is now a demand named `AT_TASK_GIT_TOKEN`, supplied machine-side).
- **`internal/dispatchrun`** — resolve demands via the new `Store`; keep the per-git-step mint + agent air-gap; the git token demand resolves through `{ mint: <github-profile> }`, the agent bearer through `{ mint: <anthropic-profile> }`.
- **`cmd/at-cove/main.go`** — `doWork`/`doDispatch` build the demand set from the kit and pass `(kitName, kitPath)` into `Plan`.
- **`kits/reference-worker/`** — kit becomes demand-only; delete `mint-github-token.sh`; `RUNBOOK.md` documents the machine-side `secrets.yml` (`minters`/`global`/`kits`), Auth0 M2M + Anthropic federation provisioning, and GitHub App provisioning.
- **Docs** — `docs/OVERVIEW.md` (the demand/supply model + `at-mint`), `docs/usage/at-cove-secrets.md` (the four sources, precedence, `minters` tagged unions), `docs/usage/at-cove-config.md` (kit `secrets:` demand-only), the dispatch interface doc (bearer via `mint:`, git token via `mint:`), and an `at-mint` usage leaf.

## 8. Testing

Hermetic (`runner.Fake` + `at-mint`'s HTTP seam), no real network:

- **`usersecret`**: four-source resolution; precedence `local(path) → yml(name) → fail-closed`; `global:`/`mint:` reachable *only* by explicit reference (a demand whose name equals a `global`/`minter` key but has no `kits:` entry → unresolved); tagged-union validation (exactly one provider; exactly one `oidc` IdP); missing-reference load error; secret-object field resolution inside a profile.
- **`mint` assembler**: a `{mint:M}` profile expands to the right `at-mint <provider>` argv (non-secret flags) + env (resolved secrets) + `COVE_RUN_*`; **no secret value appears on the argv**; captured stdout becomes the demanded value.
- **`at-mint`**: §6c.
- **`dispatchrun`**: demands resolved via `Store`; git token minted before `prepare` and `complete`, withheld from the agent (air-gap test holds); agent env carries the Anthropic bearer; `ANTHROPIC_API_KEY` unset.
- **`kit`**: `secrets:` parse as demand-only; absence of a kit-side `command:`.
- **`integration`** (maintainer-run): real GitHub App round-trip; real Auth0 → Anthropic exchange → a `/v1/messages` ping and a worker `claude -p` using the minted `ANTHROPIC_AUTH_TOKEN`.

## 9. Risks / non-goals

- **`ANTHROPIC_AUTH_TOKEN` carrying an `sk-ant-oat01`** is supported (CC forwards it as `Authorization: Bearer`) but not doc-exemplified for oat-shaped values; the risk is low (identical to the proven exchange test) and covered by the `integration` worker smoke test. Claude Code does **not** natively consume WIF/federation env vars — that path is SDK-only — which is exactly why `at-mint` does the full exchange host-side.
- **Fail-closed timing:** demands are validated before `BuildImage`, but a `mint:` runs `at-mint` just before its git step / at agent-env assembly — so a *minter* misconfiguration (bad Auth0 secret, wrong svac) surfaces after the image build, not before. Acceptable (one wasted build, torn down on error); an optional up-front smoke-mint could restore strict fail-before-build.
- **Non-goals:** additional IdPs (`oidc` union leaves room, only `auth0` implemented); additional code hosts (`minters` union leaves room, only `github`); in-VM token refresh (runs are minutes; mint once per run); changing the dispatch bracket / air-gap; a repo-agnostic multi-repo kit.

## 10. Decomposition (plans)

Sequenced so each plan is independently green and hermetic:

1. **Demand/supply `Store`** — the sectioned `usersecret` model (`minters`/`global`/`kits`), the `value`/`command`/`global` sources, `local(path) → yml(name) → fail-closed` precedence, tagged-union `minters` + source-object validators. Kit `secrets:` become demand-only; drop the kit-side `command:`. The `minters:` section and `mint:` references *parse and validate* here (a `mint:` to a missing profile is a load error), but `mint:` *expansion* is deferred to Plan 3 — Plan 1's runtime resolution covers `value`/`command`/`global`.
2. **`at-mint` binary** — `cmd/at-mint` with the `github` and `anthropic` subcommands, the HTTP seam, RS256 signing, flag/env contract, fail-closed; hermetic tests; retire `mint-github-token.sh`.
3. **Wire `mint:` end-to-end** — the `internal/mint` assembler (profile → `at-mint` argv+env), `dispatchrun`/`at-cove` resolving git-token and agent-bearer demands through `mint:`, the reference kit + RUNBOOK, and the docs.
