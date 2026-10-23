---
summary: Per-destination troubleshooting header logs in the broker — `log_headers_in` (none|examined|all) and `log_headers_out` (none|changed|all) — what each level logs, how credential-bearing values are redacted (Basic username in clear, secrets as length + sha256 prefix), the rate limit, and how to set them.
read_when: A brokered request fails auth (401/403 from Jam or the upstream) and you need to see which identity header the studio sent or which credential and username Jam forwarded, or you are turning a destination's header logging on or off.
owns: the destination header-logging levels, their log lines and redaction rules, and their rate limit
prereqs: serve.md#destinations for destinations; header-specs.md for identity_in/apply
tier: leaf
updated: 2026-10-23
---

# Destination header logging

Turn on per-destination header logs to see what the broker read from a studio
and what it sent upstream. Secrets never appear: a credential-bearing value is
logged structurally, enough to tell *which* credential went without revealing it.
Both settings default to `none`. Leave them off except while debugging.

## Levels

| Field | Level | Logs (`msg`) |
|---|---|---|
| `log_headers_in` | `none` | nothing |
| | `examined` | `broker headers in`: only the header the identity is read from (the destination's `identity_in` spec), or `missing` |
| | `all` | `broker headers in`: every request header, plus `<identity header>: missing` when it is absent |
| `log_headers_out` | `none` | nothing |
| | `changed` | `broker headers out`: the headers the broker set or changed (the applied credential, [principal header rules](model-spec-headers.md), the pool beta), and those it removed (`<removed>`, e.g. the studio's identity) |
| | `all` | `broker headers out`: every header sent upstream |

`broker headers in` is logged before the identity is checked, so a missing or
malformed one shows too. It carries `destination`, `method`, `path` and
`headers`. `broker headers out` adds `actor` and `upstream`. Hop-by-hop headers
and `X-Forwarded-For`, which Go's proxy sets after the broker, are not shown.

## Redaction

A header is credential-bearing when it is `Authorization`, `Proxy-Authorization`,
`Cookie`, `Set-Cookie`, `X-Api-Key` or `Private-Token`, the destination's own
inbound or outbound spec header, or its name contains `token`, `secret`, `key`,
`auth`, `password`, `cookie`, `session` or `credential`. Its value is shown as:

- `Basic user="me@corp.com" pass=<redacted len=24 sha256:1a2b3c>`: the Basic
  username in clear, the password redacted;
- `Bearer <redacted len=… sha256:…>` (also `token …`);
- anything else: `<redacted len=… sha256:…>`.

To check that the logged secret is the one you expect, compare prefixes:
`printf %s "$TOKEN" | sha256sum | cut -c1-6`. Other headers are logged as is.

## Rate limit

At most 60 lines per destination and direction per minute; the rest are dropped.

## Setting it

- **UI:** the destination form's *Log incoming headers* / *Log outgoing headers*.
- **CLI:** `at-jam destination add … --log-headers-in examined --log-headers-out changed`.
- **Import, admin API, config files:** `log_headers_in: examined`, `log_headers_out: changed`.

Any other value is refused 400.
