---
kind: design-spec
subject: COV-138 — route a hardened at-cove sandbox's Anthropic + git through harbor, without weakening the sealed egress
status: draft
date: 2026-09-11
prereq: 2026-09-10-harbor-broker-guest-mvp-design.md (the broker + enroll snippet this consumes from inside a cove)
read-when: implementing or reviewing the `harbor:` kit block, cove→harbor egress/routability, or the harbor auth mode in connect
---

# Route a hardened cove through harbor (COV-138)

Let a fully-hardened at-cove sandbox reach harbor's broker from **inside** the
container, so the in-cove `claude` (Anthropic via `ANTHROPIC_BASE_URL`→harbor) and
`git` (via `insteadOf`→harbor) work while the cove holds **only** its harbor
identity token — every real credential stays in harbor. This closes the gap between
"broker works from a host client" (slice #1, validated) and "works from inside a
hardened cove". **The sealed egress model is not weakened**: harbor's host becomes an
additive, scoped allow-list entry reached over 443 through the existing squid funnel.

## Design driver: reach harbor on 443, through squid

The hardening layer only lets the `proxy` user egress on tcp 80/443 (`nftables.conf`)
and squid only `CONNECT`s to `SSL_ports 443` (`squid.conf`). So the cove-facing broker
**must be TLS on :443** — then **no sealed file changes are needed**. A non-443 port
would force widening both sealed files; that is explicitly out of scope. The only new
machinery is (a) an allow-list entry, (b) a routability mapping, (c) session env/git
injection — all additive.

## Scope

**In:**
- **`harbor:` kit block** in `.at-cove/config.yml`:
  ```yaml
  harbor:
    host: harbor.local.aethons.tools    # the :443 TLS broker host
    identity: harbor-identity           # a host-SUPPLIED secret name/mint (see below)
    via-host-gateway: true              # default true; add --add-host host:host-gateway
  ```
  Absent → nothing changes (today's behavior). `identity` names a value resolved
  through at-cove's existing **demand/supply** secret model — supplied host-side via
  `~/.config/at-cove/secrets.yml` or a `minters` profile (e.g. an `at-harbor
  enroll`-backed mint) — **never a literal token in `config.yml`**.
- **Egress allow-list.** `host` is folded into the kit-root domain set
  (`RootDomains`), so it lands in the baked `allowed_domains.kit.txt` (tier 2) and is
  permitted for **every** session (incl. plain chat). 443 is already allowed by
  nftables/squid — no sealed edits.
- **Routability.** When `via-host-gateway` (default), at-cove adds
  `--add-host <host>:host-gateway` at container create (persistent + ephemeral), so a
  host-run harbor on loopback is reachable from the container. Routable-DNS harbors
  set it `false`.
- **Harbor auth mode.** A new `connect.Options.Harbor` (mirroring `Options.Vertex`).
  When set, at-cove **supersedes** the subscription-OAuth/Vertex seeding and instead
  sources the harbor connector setup into the session's tmpfs env-script: the
  enroll-snippet's `ANTHROPIC_BASE_URL`/`ANTHROPIC_API_KEY` env **and** the git
  `insteadOf` + credential-helper config. Both connectors, on the interactive/managed
  **chat** path (`connect.Connect`) this cut. Routability (`--add-host`) is applied at
  container **create**, so every session type of the kit is reachable; the
  connector *injection* for **teammate** (`LaunchTeammate`) and **dispatch workers**
  uses distinct launch paths and is deferred (see Out). (Implementation note: the env
  vars ride the existing launch `env` map; the token-free git config runs once over
  ssh via `snippet.GitConfig`, so both transports work unchanged.)
- **Token delivery.** The `identity` source is resolved **host-side** and injected
  **env-only** via the sourced tmpfs script — never in gitconfig-on-disk, argv, logs,
  or the kit secret store (same treatment as the Vertex ADC / workspace-clone token).

**Out (deferred):**
- **Teammate + dispatch-worker connector injection.** These sessions use distinct
  launch paths (`LaunchTeammate`, the work path) that don't yet thread
  `Options.Harbor`; only the interactive/managed chat path injects the connector
  this cut. Their containers still get harbor routability + the allow-list (applied
  at create), so wiring the injection is a contained follow-up.
- **Auto-enrollment** via harbor's admin API (mint/revoke a per-cove identity at
  session start) — the fast-follow; MVP takes a pre-supplied token.
- **Non-443 harbor ports** (would require widening the sealed nftables/squid — the
  thing we're avoiding).
- **GitLab connector** from inside a cove (snippet rewrites github.com only, as in
  slice #1); harbor-side gitlab route is a later addition.
- Reaching harbor when it runs **as a container on the cove's docker network** (only
  host-run / routable-DNS harbors this cut).

**Revisit flag:** superseding the operator's OAuth/Vertex whenever `harbor:` is set is
the chosen default, but the operator noted we may want per-class or opt-in-per-connector
control later (e.g. harbor-git while keeping local Anthropic auth). Kept simple here.

## Mechanisms (grounded in the current code)

### Config (`internal/kit/config.go`)
- New `Harbor *HarborConfig` on the kit config with `Host string`, `Identity string`
  (a supply name resolved through at-cove's secret demand/supply, like other kit
  secrets), `ViaHostGateway *bool` (nil→true). Parsed + validated (host non-empty
  when block present; host is a bare hostname, no scheme/port; identity non-empty).
- `RootDomains(c)` unions in `c.Harbor.Host` when set — so it reaches
  `allowed_domains.kit.txt` via the existing `writeAllowedDomains` path
  (`internal/assemble/assemble.go`). No new tier.

### Routability (`internal/backend/…`)
- `backend.CreateContext` gains an `ExtraHosts []string` (or `HostGateway string`);
  the colima backend renders `--add-host <host>:host-gateway` in both the persistent
  `Create` (`colima.go`) and `RunEphemeral` (`dispatch.go`) argv, only when set.

### Auth injection (`internal/connect/connect.go`)
- `Options.Harbor *HarborAuth{ Host, Token string }`. In the `if !o.SkipAuth { … }`
  region, a harbor branch runs **instead of** the OAuth/Vertex seeding: it appends the
  connector setup to the sourced env-script (the `StdinScript` tmpfs seam) — reusing
  the exact connector contract from `harbor.RenderEnrollSnippet(baseURL, token)` so
  cove-side and `at-harbor enroll` never drift. (`baseURL = https://<host>`.)
- The composition root (`cmd/at-cove`) builds `Options.Harbor` when the kit has a
  `harbor:` block, resolving the token host-side via `secret.Resolve`.

**Snippet reuse vs. dependency:** `RenderEnrollSnippet` lives in `internal/harbor`,
which also imports go-oidc; importing it into the at-cove binary pulls that transitive
dep. Acceptable (host binary, go-oidc already a module dep). If undesirable, the
implementation may split a stdlib-only leaf holding just the snippet contract — the
plan decides. Either way there is **one** renderer.

## Error handling

| Situation | Behavior |
|-----------|----------|
| `harbor.host` empty but block present | config validation error at load |
| `harbor.host` has a scheme/port | validation error ("host only, TLS :443 is implied") |
| identity token unresolvable at session start | fail closed (as other secret resolves do) — no session without the token |
| harbor unreachable at runtime | the in-cove `claude`/`git` fail as any egress failure would; harbor stays the only path (no silent fallback to direct egress — nftables prevents it) |

## Testing

Hermetic (plan-vs-execute via `runner.Fake`, no Docker/VM):
- Config: parse a `harbor:` block; `ViaHostGateway` defaults true; validation rejects
  empty/scheme/port host.
- Allow-list: `RootDomains` / the assembled `allowed_domains.kit.txt` includes
  `harbor.host` when the block is set, and does not when absent.
- Routability: the `docker run` argv gains `--add-host <host>:host-gateway` when
  `via-host-gateway` (default) and omits it when false.
- Auth injection: with `Options.Harbor` set, `connect` (a) does **not** seed
  OAuth/Vertex and (b) the launched env-script contains the harbor connector setup
  (`ANTHROPIC_BASE_URL=https://<host>/anthropic`, `ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN`,
  the git `insteadOf` + helper), with the raw token appearing once (env-only).
- Secret hygiene: assert the token is not in argv/logs.

Real end-to-end behind `//go:build integration` / manual DoD (needs a built cove + a
running harbor on :443).

## Manual verification (definition of done)

A fully-hardened at-cove cove of a `harbor:`-enabled kit, holding only its identity
token, can:
1. run `claude` — a real turn served via the harbor Anthropic connector; and
2. `git clone` a private repo through the harbor git connector —
the slice-#1 DoD, but from **inside** a hardened sandbox. No real Anthropic key or git
PAT is present in the cove; egress still funnels through squid to harbor's host only.

## Hardening constraints (must NOT be weakened)

1. All egress stays funneled through squid; harbor is reached **via** squid, never by
   adding it to `no_proxy` or opening direct egress.
2. The allow-list stays additive/allow-only; harbor's host is one new entry, applied
   through the existing root-domain path — the agent workload cannot widen its own egress.
3. **No sealed nftables/squid edits** — 443-only keeps the port policy intact.
4. Token stays env-only (tmpfs sourced script), out of disk/argv/logs/kit-secret-store.
5. The snippet must set only non-protected env (`ANTHROPIC_BASE_URL`/`API_KEY`) — never
   proxy vars / `CLAUDE_CONFIG_DIR` / `PATH` (those stay sealed so egress still funnels).
