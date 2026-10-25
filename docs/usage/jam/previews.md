---
summary: Studio previews — an agent exposes a port it serves (`expose(port, name)`) and authorized people open it at `https://<name>--<label>.<domain>/` through Jam: preview hosts and labels, who may open one, the browser login via `jam.<domain>` and its host-only cookie, framing in the Jam pane, bearer access for scripts, what the app sees, operator setup, the web-IDE pattern, limits and troubleshooting.
read_when: You want to open or script against a studio preview, set up previews on a Jam (DNS, wildcard cert, IdP redirect URI, `previews:`), run a web IDE in a studio, write an app that runs behind a preview, or a preview link 404s, 403s, 502s or 504s.
owns: studio previews — expose/unexpose behaviour and limits, preview hosts and labels, the login host and the __Host-jam_preview session, framing in the Jam pane (frame tickets, __Host-jam_frame, frame-ancestors), bearer access to previews, request/response handling by the preview proxy, the previews setup steps, the built-in code-server IDE and a kit's own, preview limits and troubleshooting
prereqs: collaboration.md (who may connect, sharing, the tunnel), serve.md (the previews: block)
tier: leaf
updated: 2026-10-25
---

# Studio previews

An agent serving a web app in its studio — a dev server, an API, a web IDE —
can publish it at an HTTPS URL. Jam proxies each request over a studio
tunnel ([collaboration.md](collaboration.md#how-it-works)) to `127.0.0.1:<port>` inside the studio.

## Exposing a port

The agent calls `expose(port, name)` ([intercom.md](intercom.md)) and gets
`https://<name>--<label>.<domain>/`. Its session context tells it to post the
link in its channel ([session-context.md](session-context.md)). `name` is 1–30
of `[a-z0-9-]`: no leading or trailing `-`, no `--`, not `jam` or `xn`. The
port is 1024–65535, not 2222 (sshd), 3128 (the egress proxy) or 13337 (the built-in IDE, see [A web IDE](#a-web-ide)). A session may expose at most 8, and
exposing a name again moves it to the new port. `unexpose(name)` removes one
and closes its open connections at once. `exposures()` lists them. Changes
are logged (`preview exposed` / `preview unexposed`: session, name, port).
Raising the studio again (a restart or upgrade) clears them, so the agent
exposes again. `<label>` is the session id without `ses_` (26 chars). Any
other actor id (a legacy `standing-…` id, a manual `--id`) gets a 26-char
hash of the id. `studio access` and the agent page's Access card list each
preview with its URL.

## Who may open a preview

The [four rules](collaboration.md#who-may-connect) decide, for the `preview`
right. Project members may unless the role says `--collaborate-preview none`
(the default is `members`). Anyone else needs a grant:
`at-jam studio share <studio> --with user:<name> --rights preview`. Each
request is checked live. Open connections (WebSocket, streaming) end within a
minute of losing the right ([revocation](collaboration.md#sharing)). A paused studio is
resumed for an allowed caller. Each viewer's first request per preview is
logged as `preview opened` (again after 10 min).

## Signing in (browsers)

Preview hosts have no login of their own. A
browser with no session gets a `__Host-jam_preview_login` cookie (10 min)
and goes to the login host `jam.<domain>`. It runs the
same OIDC login as `/app`, so operators count as operators, and anyone else's
login must be linked to a Jam user ([roster.md](roster.md)). It then sends you
back with a single-use code, valid 60 s, only for that preview host and only
in the browser holding that login cookie. Someone else's code therefore cannot
sign you in as them (400). The
preview host trades it for a `__Host-jam_preview` cookie: host-only,
`Secure`, `HttpOnly`, `SameSite=Lax`. The cookie names a server-side session
that lasts 8 h and works only on the host it was issued for. The login's
token is not checked again during those 8 h, also for operators, but your
`preview` right is re-checked on every request. All preview
hosts are one site, so `SameSite` alone would let one studio's page send your
cookie to another's. Jam therefore honours the cookie only on a same-origin
request (`Sec-Fetch-Site: same-origin` or `none`), on a top-level `GET`/`HEAD`
navigation, or when `Origin` is a preview host of the same session. A
WebSocket always needs such an `Origin`. A browser without Fetch Metadata
needs one (or such a `Referer`) for any method but `GET`/`HEAD`/`OPTIONS`.
Anything else — an iframe, or a fetch or form from another studio — gets
403 "cross-site request refused" (the Jam pane uses its own cookie: see
[Framing](#framing-the-jam-pane)). When the session ends, the next visit
logs in again, without a prompt while your IdP session lasts. A request with
more than one session cookie counts as signed out. A non-browser request (no
`Accept: text/html`) with no credentials gets a 401.

## Framing (the Jam pane)

The Jam UI can show a preview in a pane (an iframe). It mints a frame
ticket: single-use, valid 60 s, bound to one viewer (user or operator), one
preview host and the right it was minted under — not to a browser, so for
those 60 s it is a bearer. The iframe opens `https://<host>/__jam/frame?t=…`,
which trades it for a `__Host-jam_frame` cookie (host-only, `Secure`,
`HttpOnly`, `SameSite=None`, `Partitioned`, 8 h) and sends the frame to
`/`. The exchange works only as the UI's iframe load (`Sec-Fetch-Site:
cross-site`, `Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: iframe`), so the
UI must be on a registrable domain apart from `<domain>`
([setup](#setting-up-previews-operator) step 1). It is audit-logged as
"preview frame opened" with `host` (encoding preview name and studio
label), `user` or `operator`, and `right`; never the ticket or cookie.
The cookie lives only in the UI's partition and counts only on a
same-origin request (`Sec-Fetch-Site: same-origin`) or a WebSocket whose
`Origin` is the preview host itself. So each pane load uses a fresh ticket,
and a redirect or link from one studio into another inside the pane is not
signed in. Top-level it is ignored (you log in); in a same-site frame (a
sibling studio in the pane) refused. Your right is re-checked every time.

Every preview response carries `Content-Security-Policy: frame-ancestors`
listing only the [`ui-origins`](serve.md) (`'none'` when unset; list the
UI's own origin there to enable the pane). Any CSP the app sends stays
beside it, and a frame nested under another preview is blocked. The app's
`X-Frame-Options` is dropped only on a pane (frame-cookie) request, which
then carries `Vary: Cookie`. Jam's own error pages tell the pane their
status (`postMessage` to the **first** `ui-origins` entry, so put the
production UI origin first): inside any iframe, the 404 and the 403s
"cross-site request refused" and "this login is not linked to a Jam user";
on a pane request, the 403 "you may not use …" and the proxy's errors
(502, 503, 504, 429, 409). Other errors stay plain text. A pane load with
no frame session — a browser refusing the partitioned cookie (Safari), or
a cross-site iframe navigation without a ticket — gets a 401 page with
**Open in new tab**. `/__jam/logout` ends your preview and pane sessions on
that host server-side, honouring each cookie only where it counts, so a
sibling studio cannot log you out (say, with an `<img>`). The browser's
partitioned cookie lingers harmlessly, naming no session. Threat note: an
app in the pane can still navigate it to another studio's
`/__jam/frame?t=<ticket>` with a ticket its author minted for themselves,
showing you that studio signed in as the attacker (needs a ticket the
attacker legitimately holds, used within its 60 s).

## API clients and scripts

Scripts and API clients send `Authorization: Bearer <token>` instead. Use
an operator's `at-jam login` access token, or a participant's
`login --participant` ID token ([collaboration.md](collaboration.md#signing-in-as-a-participant-login---participant)).
To keep the token off argv, pipe the header in:

```
jq -r '"Authorization: Bearer " + .access_token' ~/.config/at-jam/default-admin-token.json \
  | curl -H @- https://app--<label>.jam-previews.example/
# participant: .id_token from ~/.config/at-jam/default-participant-token.json
```

## What the app sees

Requests keep their `Host` and carry
`X-Forwarded-Proto: https`, `X-Forwarded-Host` and `X-Jam-User` (the caller's
display name, for information only). Jam never forwards its session cookie
(the app's own cookies pass through), a Jam bearer, or a client's own
`X-Jam-User`, `Forwarded` or `X-Real-IP`. In responses Jam drops any
`Set-Cookie` that uses a Jam cookie name, drops `Clear-Site-Data`, and
removes `Domain` from the app's cookies, so they stay on its own host. Paths
under `/__jam/` belong to Jam and never reach the app. Callers may use
HTTP/1.1 or HTTP/2, and Jam uses HTTP/1.1 to the app. A WebSocket uses an
HTTP/1.1 `Upgrade`, as browsers do (Jam does not offer WebSockets over
HTTP/2). `CONNECT` is refused (405).

## Setting up previews (operator)

1. Pick a `<domain>` on its own registrable domain, apart from the admin
   site: `jam-previews.example` beside `jam.example`, not
   `preview.jam.example`. Preview apps can set cookies on their registrable
   domain, which the admin site would then receive. serve logs a warning when
   they share one. Point DNS `*.<domain>` at the Jam host.
2. Get a wildcard certificate for `*.<domain>`.
3. In the IdP, add `https://jam.<domain>/__jam/auth/callback` as a redirect
   URI of the `/app` browser client ([operators.md](operators.md)). Add
   `:<port>` when the admin listener is not on 443: previews are served on
   the admin listener, so preview URLs then carry its port (e.g.
   `https://app--…<domain>:8443/`); `:443` is the cove-facing listener.
4. Add the [`previews:` block](serve.md) and restart `at-jam serve`. Its row
   lists the requirements. Keep the admin API and UI on a name outside
   `<domain>`.
5. Raise studios again (`at-jam standing upgrade` for a standing session).
   Older cove-masters ignore the exposed ports, which gives a 504.

## A web IDE

Every studio image includes code-server. cove-master starts it on first use
(a tunnel to `127.0.0.1:13337`) and stops it after 30 idle minutes or when
cove-master exits.

**The built-in IDE (`ide`).** `https://ide--<label>.<domain>/` is the
studio's IDE, served without any `expose`. An IDE is a shell, so it needs the
**ssh** right, not `preview` ([who may connect](collaboration.md#who-may-connect)):
a preview-only viewer gets 403 "you may not use ssh on this studio". A kit or
agent that runs its own IDE on another port and calls `expose(<port>, "ide")`
replaces the built-in one; it still needs ssh. Port 13337 is reserved for the
built-in IDE and cannot be exposed. The first request may wait up to 15 s
while code-server starts (Jam gives an `ide` tunnel 30 s to open); unexposing never closes built-in IDE connections.

## Limits

Jam must terminate TLS itself, because the login callback's scheme
comes from Jam's own connection. A TLS-terminating proxy in front breaks the
login. A login only returns to the default port or the admin listener's own
port, so forwarding another external port (e.g. 9443 → 8443) fails at login.
Each viewer gets up to 6 connections per studio port, so many long-polls can
queue. A studio's previews share its 16 tunnels with SSH but hold at most 12,
so 4 stay free for SSH. Past that a preview request gets 429. An idle pooled
connection closes after 30 s, giving its tunnel back. If two names share a port, unexposing one leaves connections already
open through it until they close. A brand-new preview may 404 for a moment
after another lookup; reload.

## Troubleshooting

| You see | Meaning / fix |
|---------|---------------|
| 404 "nothing is exposed at this address" | The name is not exposed (ask the agent to `expose` it again, e.g. after a restart), the studio is gone, or the Jam has no `previews:` block (its `expose` answers 409). |
| 401 | No credentials, or the bearer is invalid or expired: log in again. |
| 403 | No rule gives you `preview` (`ssh` for `ide`), or your login is not linked to a Jam user. Ask for a share. |
| 403 "cross-site request refused" | Another page (an iframe, or another studio's fetch, form or WebSocket) used your preview session. Open the preview directly, or from the Jam pane ([Framing](#framing-the-jam-pane)). |
| 502 "the app at port … is not answering" | Nothing is listening on that port in the studio, or the app failed. |
| 504 "studio could not open the preview (is its image current?)" | The studio's cove-master predates previews. Raise the studio again. |
| 429 "… kept for SSH" | The studio's previews hold all 12 of their tunnels (many viewers or long-polls). Retry once some close. |
| 503 / 409 | The studio is not connected, or is pausing or stopping. Retry. |
