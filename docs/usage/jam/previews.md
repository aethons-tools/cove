---
summary: Studio previews — an agent exposes a port it serves (`expose(port, name)`) and authorized people open it at `https://<name>--<label>.<domain>/` through Jam: preview hosts and labels, who may open one, the browser login via `jam.<domain>` and its host-only cookie, bearer access for scripts, what the app sees, operator setup, the web-IDE pattern, limits and troubleshooting.
read_when: You want to open or script against a studio preview, set up previews on a Jam (DNS, wildcard cert, IdP redirect URI, `previews:`), run a web IDE in a studio, write an app that runs behind a preview, or a preview link 404s, 403s, 502s or 504s.
owns: studio previews — expose/unexpose behaviour and limits, preview hosts and labels, the login host and the __Host-jam_preview session, bearer access to previews, request/response handling by the preview proxy, the previews setup steps, the code-server pattern, preview limits and troubleshooting
prereqs: collaboration.md (who may connect, sharing, the tunnel), serve.md (the previews: block)
tier: leaf
updated: 2026-10-20
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
port is 1024–65535, not 2222 (sshd). A session may expose at most 8, and
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
browser with no session goes to the login host `jam.<domain>`. It runs the
same OIDC login as `/ui`, so operators count as operators, and anyone else's
login must be linked to a Jam user ([roster.md](roster.md)). It then sends you
back with a single-use code, valid 60 s and only for that preview host. The
preview host trades it for a `__Host-jam_preview` cookie: host-only,
`Secure`, `HttpOnly`, `SameSite=Lax`. The cookie names a server-side session
that lasts 8 h and works only on the host it was issued for. One studio's page
therefore never acts as you on another. When the session ends, the next visit
logs in again, without a prompt while your IdP session lasts. A request with
more than one session cookie counts as signed out. A non-browser request (no
`Accept: text/html`) with no credentials gets a 401.

## API clients and scripts

Scripts and API clients send `Authorization: Bearer <token>` instead. Use
an operator's `at-jam login` access token, or a participant's
`login --participant` ID token ([collaboration.md](collaboration.md#signing-in-as-a-participant-login---participant)).
To keep the token off argv, pipe the header in:

```
jq -r '"Authorization: Bearer " + .access_token' ~/.config/at-jam/default-admin-token.json \
  | curl -H @- https://app--<label>.preview.jam.example/
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

1. DNS: point `*.<domain>` (e.g. `*.preview.jam.example`) at the Jam host.
2. Get a wildcard certificate for `*.<domain>`.
3. In the IdP, add `https://jam.<domain>/__jam/auth/callback` as a redirect
   URI of the `/ui` browser client ([operators.md](operators.md)). Add
   `:<port>` if viewers reach Jam on a port other than 443.
4. Add the [`previews:` block](serve.md) and restart `at-jam serve`. Its row
   lists the requirements. Keep the admin API and UI on a name outside
   `<domain>`.
5. Raise studios again (`at-jam standing upgrade` for a standing session).
   Older cove-masters ignore the exposed ports, which gives a 504.

## A web IDE

A web IDE needs no Jam support. Have a kit install code-server and run it
on a port (e.g. 8443), then the agent calls `expose(8443, "ide")`.

## Limits

Jam must terminate TLS itself, because the login callback's scheme
comes from Jam's own connection. A TLS-terminating proxy in front breaks the
login. A login only returns to the default port or the admin listener's own
port, so forwarding another external port (e.g. 9443 → 8443) fails at login.
Each viewer gets up to 6 connections per studio port, so many long-polls can
queue. If two names share a port, unexposing one leaves connections already
open through it until they close. A brand-new preview may 404 for a moment
after another lookup; reload.

## Troubleshooting

| You see | Meaning / fix |
|---------|---------------|
| 404 "nothing is exposed at this address" | The name is not exposed (ask the agent to `expose` it again, e.g. after a restart), the studio is gone, or the Jam has no `previews:` block (its `expose` answers 409). |
| 401 | No credentials, or the bearer is invalid or expired: log in again. |
| 403 | No rule gives you `preview`, or your login is not linked to a Jam user. Ask for a share. |
| 502 "the app at port … is not answering" | Nothing is listening on that port in the studio, or the app failed. |
| 504 "studio could not open the preview (is its image current?)" | The studio's cove-master predates previews. Raise the studio again. |
| 503 / 409 | The studio is not connected, or is pausing or stopping. Retry. |
