---
summary: The read-only harbor admin UI — a loopback-only, server-rendered web view of the live coves and the control-plane roster/roles/kits/destinations, served by `at-harbor serve`.
read_when: You want to watch a running harbor in a browser — the live cove fleet and the roster/roles/kits/destinations — without running admin CLI verbs.
owns: the `/ui/` read-only observability surface (what it shows, how to reach it, its loopback-only exposure)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; INDEX.md for the service overview
tier: leaf
updated: 2026-09-13
---

# The harbor admin UI (`/ui/`)

`at-harbor serve` serves a **read-only** web UI on the same **admin listener** as
the JSON admin API. Point a browser at the admin URL and open `/ui/` (`/`
redirects there):

```
http://127.0.0.1:8081/ui/
```

It renders, all read-only:

- **Dashboard** (`/ui/`) — the live cove fleet + a roster summary.
- **Coves** (`/ui/coves`) — every managed cove's id, project/role, unit, phase,
  activity, lease holder, raised-at, last-seen. The table **auto-refreshes every
  3 seconds** (htmx polling); no page reload.
- **Roster / Roles / Kits / Destinations** — the control-plane objects as tables.

## Exposure — loopback only (this cut)

The UI is mounted **inside the same operator-auth gate as the admin API**, so its
exposure is exactly the admin API's (see the fail-closed rule in
[serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- On a loopback `admin-listen`, the UI is reachable from the local host only. To
  view it from your laptop against a remote harbor, SSH-tunnel the admin port.
- Off-loopback, the gate requires an OIDC **bearer** token, which a browser does
  not send — so the UI is **not** reachable from a remote browser yet. Browser
  session sign-in is a later increment.

The UI never renders a token, token hash, launch secret, or credential value, and
adds **no mutation paths** — enroll/raise/teardown/edit stay on the
[admin verbs](operators.md).
