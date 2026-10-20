# Studio collaboration: previews and IDE access through Jam

**Status:** design approved in brainstorming (2026-10-19/20); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Replaces:** sharing an agent's workspace from the operator's host drive to run its app on the host.

## Summary

Some agents work *with* a human — on docs, service UIs, APIs. Today that means
sharing the workspace from the host drive. This design lets an authorized human
reach a running studio **through Jam alone**: a browser **preview** of any port
the agent exposes, and **SSH** (for VS Code Remote-SSH, JetBrains Gateway,
`ssh`/`scp`) into the agent's studio. Studios keep dialing out only; every
inbound path rides a studio-initiated tunnel to Jam.

## Decisions (brainstorming record)

1. **Reach:** collaborators can be anywhere; the only connection guarantee is to
   Jam (human → Jam, studio → Jam). Nothing assumes studio hosts are reachable.
2. **Client:** a small client is acceptable — `at-jam` provides SSH; previews are
   plain browser URLs. A browser IDE is just another exposed port (code-server).
3. **Who:** project members via a per-role policy, plus ad-hoc per-studio share
   grants. **`preview` and `ssh` are separate rights.**
4. **How a port becomes a preview:** only the agent opens it, at runtime, with a
   brokered `expose` tool. No static declaration.
5. **Transport:** per-connection reverse `Tunnel` gRPC streams, signalled over the
   existing Attach stream (chosen over multiplexing inside Attach — head-of-line
   blocking — and over off-the-shelf tunnels, which add a non-Jam path).
6. **Addressing:** wildcard subdomains only (`<name>--<label>.preview.<jam-host>`);
   no path-prefix fallback.
7. **SSH auth:** Jam is an SSH certificate authority issuing short-lived certs.

## 1. Tunnel core

**Protocol** — on the existing `Runtime` service (cove-facing `:443`):

```proto
rpc Tunnel(stream TunnelFrame) returns (stream TunnelFrame); // studio-dialed, one per human connection
message TunnelFrame { oneof msg { TunnelOpen open = 1; bytes data = 2; TunnelClose close = 3; } }
message TunnelOpen  { string id = 1; }
message TunnelClose { string reason = 1; }
// new ControlDown variant on Attach:
message OpenTunnel  { string id = 1; int32 port = 2; }
```

**Flow, per human connection:**

1. Jam authorizes the human (§3), creates a pending tunnel `id` (single-use,
   bound to that studio, 10 s deadline), and pushes `OpenTunnel{id, port}` down
   the studio's Attach stream.
2. `cove-master` checks `port` against its **allow-list** (sshd's loopback port;
   in slice 2, currently exposed ports), dials **`127.0.0.1:port` only**, opens a
   `Tunnel` stream authenticated like Attach (identity token + launch secret), and
   sends `TunnelOpen{id}`.
3. Jam splices the human connection to the stream (`data` frames; HTTP/2 flow
   control per stream); either side's close sends `close`.
4. No dial-back in 10 s, no live Attach, or a refused port → the human gets a
   clean error (`studio not connected` / `port not exposed`). Teardown closes all
   of a studio's tunnels.

**Limits:** per-studio concurrent tunnels (default 16); idle timeout (default
30 min). **Safety:** no new listening port in the studio; egress unchanged (443
via squid); cove-master never dials non-loopback addresses; a studio cannot claim
another studio's pending id.

## 2. SSH and IDE access

**Human entry point:** the **admin/UI listener** (TLS + OIDC when off-loopback),
in two planes; the cove-facing `:443` stays studio-only. *(Amended 2026-10-20,
operator decision, option A.)*

- **Operators** — `/admin/studios/{id}/ssh*`, authenticated by the existing admin
  bearer (the operator token).
- **Participants** — `/me/studios/{id}/ssh*`, authenticated by
  `Authorization: Bearer <OIDC ID token>` from `at-jam login --participant`,
  verified with audience = the device client id and mapped to a Jam user
  (`jam.ParticipantByIdentity`).

On each plane:

- `POST …/studios/{id}/ssh/cert` — body: an ed25519 public key → a short-lived cert.
- `GET …/studios/{id}/ssh` — HTTP Upgrade to a raw byte stream, spliced to the
  studio's sshd over a §1 tunnel.

**Jam SSH CA:** an ed25519 key supplied as a credential (`ssh: { ca-cred: <name> }`
in the serve config; value from `credentials.yml`), resolved at startup, never
written elsewhere. Certs: principal `agent`; key-id `user=<user-id>
studio=<session-id>`; validity 8 h. The sealed hardening layer adds
`TrustedUserCAKeys /etc/ssh/jam_user_ca.pub`; the launcher writes that public key
at setup.

**Client (`at-jam`):**

- `at-jam ssh-config <studio>` writes a `Host jam-<studio>` block —
  `ProxyCommand at-jam ssh-proxy <studio>`, `IdentityFile`/`CertificateFile`
  under `~/.config/at-jam/ssh/`, `User agent`.
- `at-jam ssh-proxy` renews the cert when < 1 h remains (via the `at-jam login`
  token), then opens the upgrade stream and pipes stdio.
- `at-jam ssh <studio>` ensures config + cert and execs `ssh jam-<studio>`.
- **Verify early:** if OpenSSH loads the cert before running the ProxyCommand, a
  renewal applies from the next connection; 8 h validity with renew-at-1 h keeps
  this invisible.

**Scope of an SSH session:** everything the agent's user can do (hence a separate,
opt-in right). Port forwarding stays enabled (VS Code's server needs it); it
reaches only the studio's inside, and outbound traffic still passes squid/nftables.

**Revocation:** removing a right blocks new certs and tunnels at once; Jam drops
live tunnels; an issued cert is useless without a tunnel.

## 3. Authorization

Rights: `preview`, `ssh` — checked independently on every request, evaluated live.
A human holds a right on a studio if, in order:

1. they are an **operator**;
2. they are the **requester** of a personal session (both rights on their own);
3. they are a **member of the studio's project** and the role allows it:
   ```yaml
   collaborate:
     preview: members   # members | none — default members
     ssh:     none      # members | none — default none
   ```
4. they hold an unexpired **share grant**:
   `at-jam studio share <studio> --with user:<name> --rights preview[,ssh] [--for 4h]`
   / `studio unshare`. A grant is `{session_id, user_id, rights, expires_at,
   granted_by}`, default 8 h, max 7 d, keyed to the **session** (survives studio
   restart/upgrade; dies with the session). Grantors: operators, or a holder of
   the same right on that studio.

**Identity:** OIDC login mapped to a user via its OIDC bindings (as `/me` does);
operators via operator login; unmapped logins are refused.

**Audit & UI:** every cert issue, tunnel open/close, share and unshare is logged
with user, session, right and port. The studio page shows who can reach it and
why, live connections, share/unshare (CSRF-guarded). An SSH connect posts a
one-line notice to the session's channel ("alice connected over SSH").

## 4. Previews

**Agent tools** (brokered, beside `send`/`alarm`):

- `expose(port, name)` — `name` `[a-z0-9-]{1,30}`; port 1024–65535, not sshd's;
  returns `https://<name>--<label>.preview.<jam-host>/`; re-exposing a name
  replaces its port.
- `unexpose(name)`, `exposures()`.

Exposures are session-scoped and logged; the agent re-exposes after a studio
restart. Session context tells it to post the link in its channel.

**Serving:** serve config `previews: { domain: preview.jam.example, tls: {cert,
key} }` (a wildcard cert; unset ⇒ previews off). Jam serves `*.<domain>` on the
admin/UI listener: host → (session, name) → port → `preview` right → §1 tunnel to
`127.0.0.1:<port>`. HTTP/1.1, HTTP/2 and WebSocket upgrades pass through.

**Auth:** *(superseded by Amendments 1–2 below: a host-only cookie per preview
host, via the login host `jam.<domain>`.)* First visit → OIDC login → a cookie
scoped to `.<domain>` only (never the admin UI's origin). No cookie ⇒ redirect (browsers) or 401 (APIs). API
clients may send `Authorization: Bearer <at-jam login token>`. Jam strips its
cookie and `Authorization` before forwarding, and adds `X-Forwarded-Proto/Host`
and an informational `X-Jam-User`. The studio page links each preview.

**Web IDE:** a kit installing code-server lets the agent `expose(8443, "ide")` —
no extra Jam code.

**Amendments (2026-10-20, operator decision at slice-2 planning, and rulings
made while building it).** These supersede the text above where they differ;
the user guide is [previews.md](../../usage/jam/previews.md).

1. **Cookie:** not one cookie on `.<domain>` — that would let one studio's
   preview page drive another studio's app as the viewer. Each preview host gets
   its own **host-only** cookie, `__Host-jam_preview` (`Secure`, `HttpOnly`,
   `SameSite=Lax`, `Path=/`, no `Domain`), set from a **single-use code**
   (60 s, bound to that host) that the reserved login host `jam.<domain>` hands
   back after the OIDC login. The cookie carries a random server-side session id
   (8 h, bound to its host), never a token. The `__Host-` prefix (also on the
   login flow's temporary cookies) stops a sibling app tossing a cookie with
   `Domain`; a request carrying more than one session cookie is treated as
   signed out (a fresh login).
2. **Identity:** the preview login requests the same token as `/ui`, so
   operators are recognized as operators; everyone else maps to a Jam user by
   OIDC binding. Bearer callers present an operator access token or a
   participant (`login --participant`) ID token.
3. **Allow-list:** cove-master's dial-back allow-list is `{2222} ∪ exposed
   ports`, pushed by Jam as `ControlDown.SetTunnelPorts` (tag 8) first on every
   Attach stream and after each change; an older cove-master ignores it and the
   viewer gets a 504.
4. **Label:** `<label>` is a `ses_` session id's 26-char body; any other actor
   id (legacy `standing-…`, manual `--id`) gets the first 26 chars of the
   lowercase unpadded base32 of its SHA-256 — always 26 of `[a-z0-9]`.
5. **Hosts:** every host equal to or under `<domain>` goes to the preview
   handler (404 unless it is a preview or the login host), so the admin/UI host
   must lie outside `<domain>`; the listener presents the wildcard cert only to
   an SNI name under `<domain>`, the admin cert otherwise.
6. **Responses and requests:** Jam drops app `Set-Cookie`s for Jam cookie names
   and `Clear-Site-Data`, removes `Domain` from the app's cookies (host-only),
   strips client `Forwarded`/`X-Real-IP`, and answers `CONNECT` with 405 —
   HTTP/2 WebSockets (extended CONNECT) are not offered; browsers use the
   HTTP/1.1 `Upgrade`, which passes through. Paths under `/__jam/` are Jam's.
7. **Cross-preview requests (final review):** every `*.<domain>` host is one
   site, so `SameSite=Lax` still sends a preview's cookie on a fetch, form,
   iframe or WebSocket that another studio's page starts. The cookie is
   honoured only for `Sec-Fetch-Site` `same-origin`/`none`, a top-level
   `GET`/`HEAD` navigation (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest:
   document`), or an `Origin` that is a preview host of the same session label;
   a WebSocket always needs that `Origin`, and without Fetch Metadata an unsafe
   method needs it (or such a `Referer`). Anything else is 403 "cross-site
   request refused" (no login redirect). Bearers are no ambient credential and
   are unaffected.
8. **Registrable domain (final review):** preview apps run untrusted code that
   can set cookies on the previews domain's registrable domain (eTLD+1), so the
   previews domain must be a registrable domain apart from the admin site's
   (e.g. `jam-previews.example` beside `jam.example`). serve logs a WARN at
   startup when the `admin-listen` host or a `ui-hosts` entry shares it
   (public-suffix list, `golang.org/x/net/publicsuffix`).

## 5. Testing

- **Unit:** tunnel splice and dial-back deadline (fake Attach + Tunnel streams);
  authz matrix (operator / requester / role policy / grant / expired grant /
  none); cert contents (principal, key-id, TTL); preview host routing; cookie
  scoping and credential stripping.
- **cove-master:** loopback-only dialing, port allow-list, caps, idle timeouts.
- **Integration (`-tags integration`):** a real sshd behind a tunnel, driven by
  `ssh -o ProxyCommand="at-jam ssh-proxy …"`; a WebSocket echo server through a
  preview.
- **Manual:** VS Code Remote-SSH and a Vite dev server with HMR on a real Jam.

## Slices

1. **Tunnel core + SSH** — `Tunnel` RPC and `OpenTunnel`, cove-master dial-back,
   SSH CA and cert endpoint, `at-jam ssh-config|ssh-proxy|ssh`, sshd
   `TrustedUserCAKeys` in hardening, `collaborate.ssh`, share grants (`ssh`),
   audit, session-channel notice, studio-page access panel.
2. **Previews** — `expose`/`unexpose`/`exposures`, wildcard preview listener and
   cookie login, `collaborate.preview`, `preview` grants, UI links, code-server
   pattern documented.

## Out of scope

- Path-prefix previews; non-Jam tunnels (frp, tailnets).
- Static, kit-declared preview ports.
- Per-port rights finer than `preview` (e.g. one preview but not another).
- Recording or replaying SSH sessions.
