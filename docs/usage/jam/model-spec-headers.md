---
summary: Model-spec principal header rules (`principal.headers`) — extra headers the broker sets or list-appends on a principal's requests to its provider destination, after the credential; the `set`/`ensure-list-item` rule kinds, validation, which destination they apply to, and the admin UI line syntax.
read_when: You need a model-spec's principal to send an extra upstream header (e.g. the `oauth-2025-04-20` `anthropic-beta` for a pool principal), a header-rule write was refused, or a rule is not reaching (or is skipped at) the upstream.
owns: model-spec principal header rules — their schema, validation, broker application and UI syntax
prereqs: model-specs.md for the model-spec entity; header-specs.md for destination identity/apply headers
tier: leaf
updated: 2026-10-05
---

# Model-spec principal header rules

A [model-spec](model-specs.md)'s principal may carry **header rules**: extra
headers the [broker](serve.md#the-broker-model) applies to that principal's
requests. Use them for headers an upstream needs from *this kind of principal* —
for example a [pool](pool.md) principal needs Anthropic's `oauth-2025-04-20`
beta on every request.

```yaml
principal:
  credential: pool
  headers:                              # at most 16 rules, applied in order
    - name: anthropic-beta
      ensure-list-item: oauth-2025-04-20
    - name: X-Team
      set: platform
```

Each rule has a `name` and **exactly one** action:

| Action | Effect on the forwarded request |
|--------|---------------------------------|
| `set: VALUE` | Replaces the header with `VALUE` (any value the cove sent is dropped). |
| `ensure-list-item: ITEM` | Treats the header as a comma-separated list and appends `ITEM` unless it is already there (items compare with surrounding whitespace trimmed); absent header → just `ITEM`. Idempotent. |

## Where rules apply

Rules apply only on the **destination serving the spec's provider route**, and
only **after** the credential is set. For `type: claude` with
`provider: anthropic` that is the destination named `anthropic`, or — when none
has that name — the one routed at `/anthropic/` (the same rule the
`claude-default` seed uses). Other destinations a role reaches (git, Linear, …)
never see the rules, and `vertex`/`bedrock` specs have no brokered provider
destination, so their rules apply nowhere today.

The broker resolves the actor's model-spec exactly as the
[connector](connector.md) does (one spec per actor; a conflict or a missing
bound spec applies no rules and logs a WARN). It resolves it only for a
provider-destination request, and per request, so an edit applies to the next
request. A spec without rules forwards exactly as before.

A destination's `oauth_beta` flag ([pool.md](pool.md)) still works, and composes
with an `ensure-list-item` rule for the same beta: the beta appears once.

## Validation

At every write (`at-jam model-spec add`/`update`, the admin UI, config import),
each rule is checked; a refusal is a 400 naming `principal.headers[i]` and never
echoes a value:

- `name` is a valid HTTP header name the proxy can carry — not hop-by-hop
  (`Connection`, `Te`, `Upgrade`, …) and not `Host`;
- `name` is never `Authorization`, `X-Api-Key`, `Cookie` or any `Proxy-*`
  header (credentials, sessions and the proxy's own headers);
- exactly one of `set` / `ensure-list-item`, non-empty;
- the value is single-line (no CR, LF or NUL); an `ensure-list-item` is one item
  (no comma) without leading or trailing whitespace;
- at most 16 rules.

At apply time the broker re-checks each rule and also **skips** one that names
a header the destination's [identity-in or apply spec](header-specs.md) uses
(so a rule can never overwrite the credential or leak the identity). A skipped
rule logs a WARN naming the actor, destination and header — never the value.
Values are not secrets, but they are kept out of logs anyway.

## In the admin UI

The model-spec form has a **Principal header rules** textarea, one rule per line:

```
anthropic-beta += oauth-2025-04-20
X-Team = platform
```

`NAME += ITEM` is `ensure-list-item`; `NAME = VALUE` is `set`. The line splits
at the first `=` (header names never contain one), so values may contain `=`;
surrounding whitespace is trimmed. The spec's page lists the rules in the same
syntax. See [ui-pages.md](ui-pages.md#model-spec-pages).
