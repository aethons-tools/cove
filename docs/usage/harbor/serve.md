---
summary: Running the harbor service — `at-harbor serve`, the serve-config YAML (listen, admin-listen, tls/admin-tls, store, credentials), the credential-broker model, managing destinations, and the off-loopback fail-closed rule.
read_when: You are standing up or configuring a harbor service — writing its serve config, wiring the real credentials it brokers, adding the destinations coves reach, or exposing the admin API beyond loopback.
owns: the `at-harbor serve` command + serve-config schema (listen/admin-listen/tls/admin-tls/store/credentials), the broker model, the `destination` verb, and the off-loopback exposure guard
prereqs: INDEX.md for the service overview; operators.md for the `operator-auth.oidc` block referenced here
tier: leaf
updated: 2026-09-13
---

# Running harbor (`at-harbor serve`)

`at-harbor serve --config <file>` runs one process that is both the **broker**
(the reverse proxy coves send Anthropic/git through) and a **loopback admin API**
(the control plane the `at-harbor` admin verbs talk to). Both are backed by a
single JSON **store** file. The serve config is bootstrap-only: destinations,
roles, and enrollments are managed at runtime via the admin API, not this file.

```
at-harbor serve --config /etc/harbor/harbor.yml
```

## The serve config

```yaml
listen: ":443"                 # broker listener (coves connect here; TLS in prod)
admin-listen: "127.0.0.1:8081" # admin API listener (operator surface)
tls:                           # broker server cert (required for a real :443)
  cert: /etc/harbor/tls/fullchain.pem
  key:  /etc/harbor/tls/privkey.pem
admin-tls:                     # optional; admin-API cert. Falls back to tls: if unset
  cert: /etc/harbor/tls/admin-fullchain.pem
  key:  /etc/harbor/tls/admin-privkey.pem
store: /var/lib/harbor/store.json   # the live data file (identities, roles, kits, destinations)
credentials:                        # the REAL downstream secrets harbor injects
  anthropic-key:
    command: ["at-mint", "anthropic", "--audience", "…"]   # a resolver run on the host
  git-pat:
    value: "ghp_…"                                         # or a literal value
operator-auth:                      # see operators.md — omit for loopback-only admin
  oidc:
    issuer:   https://YOUR_TENANT.us.auth0.com/
    audience: https://harbor.example.com/admin
    require-scope: harbor:admin
    device-client-id: "…"
    device-scope: "openid profile"
runtime:                            # optional — supervisor lease/reconcile timing
  lease-ttl: 60s
  reconcile-interval: 30s           # must be < lease-ttl
  listen: "127.0.0.1:9090"          # OPTIONAL plaintext Attach gRPC dev listener; prod uses the :443 mux
  launcher:                         # optional — enables the real Colima cove launcher
    install-manifest: /etc/harbor/install.json  # → the pre-built image (Image + ImageDigest)
    runtime-addr: harbor.example.com:443        # what a raised cove dials (AT_HARBOR_RUNTIME_ADDR)
    harbor-host: harbor.example.com             # added to the cove's /etc/hosts; connector base host
    identity-file: /var/lib/harbor/at-cove/id_ed25519  # SSH key matching the image's baked authorized_keys
    known-hosts-dir: /var/lib/harbor/known_hosts.d
    dns: []
    docker: false
```

The cove-facing `listen:`/`tls:` endpoint serves **both** the HTTP broker and the
[Attach](coves.md#the-attach-stream) gRPC stream on the one :443 TLS port: harbor
terminates TLS once, then multiplexes the decrypted stream by `content-type`
(`application/grpc` → the Attach server, everything else → the broker). This is
why a hardened cove — whose sealed egress only permits `CONNECT … :443` — can
reach the Attach stream at all. `runtime.listen` is now only an **optional
plaintext dev listener** (no TLS, for local testing), not the production path.

| Key | Required | Purpose |
|-----|----------|---------|
| `listen` | yes | Address the cove-facing endpoint serves on — **both** the broker and the [Attach](coves.md#the-attach-stream) gRPC stream, multiplexed by `content-type`. Use `:443` in production — a sealed cove can only `CONNECT` to 443. |
| `admin-listen` | no | Address the admin API serves on. Omit to run the broker alone. |
| `tls.cert` / `tls.key` | for a real broker | The broker's own server certificate (it serves its own TLS per connector — no MITM CA). |
| `admin-tls.cert` / `admin-tls.key` | no | A separate cert for the admin API; falls back to `tls:` when unset. |
| `store` | yes | Path to the JSON store (created on first write; migrated forward across versions). |
| `credentials.<name>` | as needed | The real secrets the broker injects, each a `{command: [...]}` resolver or a literal `{value: "..."}`. Referenced by a destination's `cred-name`. Values are resolved on the host, in memory — never written to the store. |
| `operator-auth.oidc` | to gate the admin API | OIDC operator identity — see [operators.md](operators.md). Omitted ⇒ the admin API trusts loopback only. |
| `runtime.lease-ttl` / `runtime.reconcile-interval` | no | Managed-cove supervisor timing (defaults 60s / 30s; reconcile must be < ttl). See [coves.md](coves.md). |
| `runtime.listen` | no | Optional **plaintext** Attach gRPC dev listener (no TLS), for local testing. Omit in production — the Attach gRPC is served on the `:443` mux alongside the broker. |
| `runtime.launcher` | no | Enables the real Colima cove launcher (omit ⇒ a placeholder that records instances without a backend). Requires `install-manifest`, `runtime-addr`, `harbor-host`; `identity-file`/`known-hosts-dir` default to the at-cove config dir. See the launcher note below. |
| `runtime.dispatcher` | no | Enables the resident dispatcher: harbor polls a tracker and raises a managed cove per ready ticket. Requires `role`, `max-concurrent` (>0), and a `linear` block. See [dispatcher.md](dispatcher.md). |

### The launcher (`runtime.launcher`)

With a `launcher` block, `at-harbor cove raise` starts a **real** cove on the Colima
backend from the pre-built image named by `install-manifest` (the frozen
`install.json` an `at-cove install` produced — its `Image` + `ImageDigest`),
injects the connector + prompt over SSH, and starts `cove-master` in it. The cove
dials harbor's Attach stream at `runtime-addr` (`harbor.host:443`) through its own
squid proxy; `harbor-host` is added to the cove's `/etc/hosts` so that name resolves
to the host gateway.

**Deployment constraint:** harbor must be given the **same SSH key** that
`at-cove install` baked into the image's `authorized_keys` — point `identity-file`
at that private key (it defaults to `~/.config/at-cove/id_ed25519`, the at-cove
default). A key harbor generates fresh would not be authorized by the image. Harbor
must also reach the Colima backend (run it where `docker`/Colima is available). Omit
the whole block to keep the placeholder launcher (dev/tests).

## The broker model

The broker is **client-addressed, per-connector** — there is no MITM CA to
install. A cove re-addresses harbor per service (`ANTHROPIC_BASE_URL=<base>/anthropic`,
git `insteadOf` → `<base>/git/`), presents only its **identity token**, and harbor:

1. authenticates the identity (by token hash) and authorizes the request against
   the actor's role scope (see [roster.md](roster.md)),
2. swaps the identity token for the **real** downstream credential (from
   `credentials:`), and
3. re-originates the request upstream.

The conscious trade: harbor sees the plaintext of brokered traffic (the client
sent it there on purpose), in exchange for collapsing N cove-side secrets into one
defended harbor.

## Destinations

A **destination** is one brokered upstream: a path prefix on the broker that maps
to an upstream base URL plus how the identity arrives and how the real credential
is applied. Destinations are managed at runtime via the admin API:

```
at-harbor destination add \
  --name anthropic --route /anthropic/ --upstream https://api.anthropic.com \
  --identity-in x-api-key --cred-name anthropic-key --apply x-api-key
at-harbor destination add \
  --name git --route /git/ --upstream https://github.com \
  --identity-in basic-password --cred-name git-pat --apply basic-password --repo-scoped
at-harbor destination list
at-harbor destination rm <name>
at-harbor destination import <file.yaml>   # bulk add from a YAML with a `destinations:` list
```

- `--identity-in` / `--apply` are one of `bearer | basic-password | x-api-key` —
  how the cove presents its identity, and how harbor applies the real credential.
- `--repo-scoped` marks a git-style destination whose path is `<route>/<owner>/<repo>/…`,
  so a role's `repos` globs can scope it.
- `--cred-name` must resolve to a `credentials:` entry in the serve config
  (validated at add time).

These admin verbs take the standard client flags (`--app`/`--admin-url`/`--token`);
see [operators.md](operators.md).

## Exposing the admin API (fail-closed)

A loopback `admin-listen` stays plain HTTP. The admin API **refuses to bind
off-loopback unless both** a TLS cert (`admin-tls` or `tls`) **and**
`operator-auth.oidc` are configured — an unauthenticated, plaintext control plane
can never be exposed to the network by accident. To administer a harbor from
another machine, give it a routable `admin-listen`, TLS, and OIDC, then point
clients at its `https://` admin URL ([operators.md](operators.md)).

For the cove side of the broker connection — the `harbor:` kit config, auto-enroll,
and reaching a host-run harbor — see [`../at-cove-config.md#harbor`](../at-cove-config.md).
For the broker's threat model and boundary rationale, follow the design-history
pointer in [INDEX.md](INDEX.md).
