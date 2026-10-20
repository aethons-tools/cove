---
summary: Studio collaboration — reaching an agent's running studio through Jam: SSH and IDEs (VS Code Remote-SSH, JetBrains Gateway) via `at-jam ssh`, browser and API previews of ports the agent exposes, who may connect (operators, the session's requester, role `collaborate` policy, share grants), and how it works (Jam-issued short-lived certs and a preview proxy over a studio-dialed tunnel).
read_when: You want to SSH or point an IDE at an agent's studio, open or script against a studio preview, let a teammate in (`studio share`), set a role's `collaborate` policy, enable the `ssh:` or `previews:` serve config, or an `at-jam ssh` connection or a preview link was refused, 404'd or timed out.
owns: the studio-collaboration user and operator guide — at-jam ssh-config/ssh-proxy/ssh and login --participant, the access rules and share grants (studio share/unshare/access, revocation), the ssh: and previews: setup steps, studio previews (expose/unexpose, preview hosts and labels, the login host and preview cookie, bearer access, what the app sees, the web-IDE pattern, preview limits), and the tunnel/cert mechanism at the level an operator needs
prereqs: serve.md (serve config, credentials), roster.md (roles, users), operators.md (login)
tier: leaf
updated: 2026-10-20
---

# Studio collaboration (SSH, IDEs and previews)

A person can work *in* an agent's running studio — a shell, VS Code
Remote-SSH, JetBrains Gateway — or open a web app the agent serves there
([Previews](#previews)), through Jam alone: no host drive, no new port in the
studio. Jam decides who may connect, issues them a short-lived SSH
certificate (or signs their browser in), and splices their connection to the
studio's port over a tunnel the studio dials back.

## Quick start

**Operator, once per Jam:**

1. Make the CA key: `ssh-keygen -t ed25519 -f jam-ssh-ca -N ''`.
2. Demand and supply it in `credentials.yml`
   ([credentials.md](credentials.md)), e.g.
   `jam-ssh-ca: { command: ["cat", "/path/to/jam-ssh-ca"] }`.
3. Add `ssh: { ca-cred: jam-ssh-ca }` to the serve config
   ([serve.md](serve.md)) and restart `at-jam serve`.
4. Let members in where wanted: `at-jam role add … --collaborate-ssh members`
   ([roster.md](roster.md)).

Studios raised after the restart trust the CA; one already running does not
until its next raise. The same holds for rotating the CA key: a studio picks
up the new key at its next raise, not before.

**Collaborator:**

```
at-jam login --participant          # once; again when it expires (below)
at-jam ssh <studio>                 # a shell
at-jam ssh-config --write <studio>  # or: an ~/.ssh/config entry for an IDE
```

Then in VS Code: *Remote-SSH: Connect to Host… → `jam-<studio>`* (JetBrains
Gateway: an SSH connection to the same host). Operators can do all of this with
their operator login instead (`--operator`).

## Signing in as a participant (`login --participant`)

`login --participant` runs the operator device flow ([operators.md](operators.md))
but signs you in as a **participant** — a Jam user, not an operator. It caches
only the OIDC **ID token** (audience = `device-client-id`, so `device-scope`
must include `openid`) in `~/.config/at-jam/{app}-participant-token.json`,
never touching the operator token. Your IdP login must be linked to a Jam user
([roster.md](roster.md)). `logout --participant` clears it.

ID tokens are short-lived — their lifetime is set by your IdP, typically
about 1 h — and the device flow gives no refresh, so when one expires (a 401), run `at-jam login --participant` again.

Jam mounts the participant routes (`/me/studios/{id}/ssh*`) only when
`operator-auth.oidc.device-client-id` and `ssh:` are both set. Operators use
`/admin/studios/{id}/ssh*` with the admin bearer.

## The ssh commands

| Command | What it does |
|---------|--------------|
| `at-jam ssh-config [--write] <studio>` | Prints a `Host jam-<studio>` block (`User agent`, `ProxyCommand <this at-jam's absolute path> ssh-proxy …` — so it works when `at-jam` isn't on ssh's PATH, e.g. a `dist/` build; rerun `ssh-config --write` after moving the binary —, `IdentityFile`/`CertificateFile` under `~/.config/at-jam/ssh/`) and fetches the studio's cert. `--write` installs it in `~/.ssh/config` between `# >>> at-jam <studio>` markers — at the top, since OpenSSH takes the first value it finds and an earlier `Host *` would win — ending in `Match all` so options below it still apply to every host; a rerun replaces the block in place. |
| `at-jam ssh-proxy <studio>` | The ProxyCommand: renews the cert when under 1 h remains, then tunnels ssh's stdio through Jam (`GET …/studios/<studio>/ssh`, `Upgrade: jam-tunnel`). |
| `at-jam ssh <studio> [ssh args…]` | Renews the cert if needed, then execs `ssh jam-<studio>` with the same options and your extra args — no `~/.ssh/config` edit needed. |

All three take `--app`, `--admin-url`, and `--participant` / `--operator` to
pick the plane; the default is the participant login when one is cached and
unexpired, else the operator token, and `ssh-config` pins the choice in the
ProxyCommand. The key (`id_ed25519`, 0600) is generated once; certs
(`<studio>-cert.pub`, under `<app>/` for a non-default `--app`) last 8 h.

OpenSSH reads `CertificateFile` *before* it runs the ProxyCommand, so a renewal
made by `ssh-proxy` applies from the next connection; `at-jam ssh` renews before
connecting, and an IDE simply reconnects. `ssh-proxy` dials Jam directly: it
does not honour `HTTPS_PROXY`/`HTTP_PROXY`.

## Who may connect

Jam checks each cert request and each connection against four rules, in order:

1. **Operators** — always.
2. **The requester** of a personal session ([personal-sessions.md](personal-sessions.md)).
3. **Project members**, as the role's collaboration policy says:
   `--collaborate-ssh members|none` (default `none`), `--collaborate-preview`
   (default `members`; see [Previews](#previews)) — see [roster.md](roster.md).
4. **A share grant** for the user on this session, unexpired, holding the right.

A removed user matches none of them. A studio that is terminating or gone
takes no certs or tunnels (409); a paused one is resumed for an allowed caller
([Idle studios](#idle-studios)). The rules need no project membership: a
share grant works for a user who is in no project.

## Sharing

| Command | What it does |
|---------|--------------|
| `at-jam studio share <studio> --with user:<name> --rights preview,ssh [--for 8h]` | Grants the user those rights on the studio (`POST …/studios/<studio>/shares` `{user, rights, for}` → 201) until `--for` runs out: a Go duration (`4h`, `90m`) or whole days (`7d`); default 8 h, at most 7 d (longer → 400). Sharing again replaces the user's grant (dropping `ssh` closes their open SSH connections, as unshare does). The grant is keyed to the session and removed when it ends. |
| `at-jam studio unshare <studio> --with user:<name>` | Removes the grant (`DELETE …/shares/<user>` → 204; none → 404; the user may also be a `usr_` id, or the name of a removed user with a leftover grant) and closes the user's open SSH connections to the studio, unless they still hold `ssh` another way. |
| `at-jam studio access <studio>` | Who can reach it and why (`GET …/access` → `{requester, role, grants, live, previews_on, previews}`): operators, the requester, the role's policy for members, each grant (rights, expiry, granted by), the open connections (each with its right, `ssh` or `preview`) and a `PREVIEWS` section (name, port, URL). |

They pick the plane as the ssh commands do. An operator may share anything. A
participant must hold some right on the studio to use any of the three, may
grant only rights they hold — and one they hold only by a grant, for no longer
than that grant runs — never to themselves, and may remove (or share over) a
grant whose rights they all hold, or their own. A grant made by a grantee is
its own grant: it is bounded by the grantor's expiry when made, but it stays
if the grantor's grant is later removed. Each share and unshare is logged
(`studio shared` / `studio unshared`: grantor, user, session, rights, expires).

**Revocation.** Every minute Jam re-checks each open SSH and preview
connection (operators' excepted) against the rules above, for the right it
uses, and drops those no longer allowed — the user was removed, the role's
policy or the user's membership changed, the grant expired — logging
`ssh tunnel revoked` / `preview connection revoked` (user, session, right,
reason). The
agent page's **Access** card shows the same, with an Unshare button
([ui-pages.md](ui-pages.md#agent-pages)).

## Idle studios

Jam pauses a studio about a minute after its agent goes quiet. An open SSH or
preview connection (or one still opening) counts as activity: while there is one, Jam
does not pause the studio, tear it down for waiting (wait-max, a role's
on-idle teardown), or nag or reclaim a personal session — the agent is still
woken as usual. When the last connection closes, those timers carry on from
where they were, so a personal session already past its reclaim-after can be
reclaimed soon after you disconnect.

A cert or tunnel request on a paused studio resumes it and waits up to 10 s
for its Attach stream to come back before going on. A studio that is pausing
or stopping takes no new tunnels (409, retry).

## How it works

1. `at-jam ssh-proxy` asks Jam's admin/UI listener for the tunnel
   (`GET …/studios/<studio>/ssh`, HTTP/1.1 `Upgrade: jam-tunnel`).
2. Jam authorizes the caller, then pushes `OpenTunnel{id, port 2222}` down the
   studio's Attach stream ([coves.md](coves.md#the-attach-stream)).
3. The studio's cove-master dials the `Tunnel` RPC back to Jam on 443 (the
   same egress as Attach), claims the id, and connects it to its loopback sshd
   (`127.0.0.1:2222`). It dials only loopback, and only sshd's port and the
   ports the session exposes as [previews](#previews)
   ([allow-list](coves.md#the-attach-stream)).
4. Jam answers `101` and splices the two; every allowed request is audited
   (user, session, reason, port — never a key, token or cert) and the session
   gets a "connected over SSH" notice.

The cert is signed by Jam's CA for principal `agent`, valid 8 h, key-id
`user=<user> studio=<session>` (sshd logs it), usable only from the studio's
loopback (`source-address 127.0.0.1/32,::1/128`) — so only through the
tunnel, never a host-published sshd port. The studio's sshd trusts the CA
through the hardening layer ([OVERVIEW.md](../../OVERVIEW.md)). Nothing new
listens in the studio and its egress is unchanged. Limits: 16 tunnels per
studio, 30 min idle, 10 s for the dial-back; a teardown or a pause closes a
studio's tunnels and it takes none until it is resumed or raised again.

## Previews

An agent serving a web app in its studio — a dev server, an API, a web IDE —
can publish it at an HTTPS URL. Jam proxies each request over a studio
tunnel ([How it works](#how-it-works)) to `127.0.0.1:<port>` inside the studio.

**The agent** calls `expose(port, name)` ([intercom.md](intercom.md)) and gets
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

**Who may open it:** the [four rules](#who-may-connect), for the `preview`
right. Project members may unless the role says `--collaborate-preview none`
(the default is `members`). Anyone else needs a grant:
`at-jam studio share <studio> --with user:<name> --rights preview`. Each
request is checked live. Open connections (WebSocket, streaming) end within a
minute of losing the right ([Revocation](#sharing)). A paused studio is
resumed for an allowed caller. Each viewer's first request per preview is
logged as `preview opened` (again after 10 min).

**Signing in (browsers).** Preview hosts have no login of their own. A
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

**API clients and scripts** send `Authorization: Bearer <token>` instead. Use
an operator's `at-jam login` access token, or a participant's
`login --participant` ID token ([above](#signing-in-as-a-participant-login---participant)).
To keep the token off argv, pipe the header in:

```
jq -r '"Authorization: Bearer " + .access_token' ~/.config/at-jam/default-admin-token.json \
  | curl -H @- https://app--<label>.preview.jam.example/
# participant: .id_token from ~/.config/at-jam/default-participant-token.json
```

**What the app sees.** Requests keep their `Host` and carry
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

### Setting up previews (operator)

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

**A web IDE** needs no Jam support. Have a kit install code-server and run it
on a port (e.g. 8443), then the agent calls `expose(8443, "ide")`.

**Limits.** Jam must terminate TLS itself, because the login callback's scheme
comes from Jam's own connection. A TLS-terminating proxy in front breaks the
login. A login only returns to the default port or the admin listener's own
port, so forwarding another external port (e.g. 9443 → 8443) fails at login.
Each viewer gets up to 6 connections per studio port, so many long-polls can
queue. If two names share a port, unexposing one leaves connections already
open through it until they close. A brand-new preview may 404 for a moment
after another lookup; reload.

| You see | Meaning / fix |
|---------|---------------|
| 404 "nothing is exposed at this address" | The name is not exposed (ask the agent to `expose` it again, e.g. after a restart), the studio is gone, or the Jam has no `previews:` block (its `expose` answers 409). |
| 401 | No credentials, or the bearer is invalid or expired: log in again. |
| 403 | No rule gives you `preview`, or your login is not linked to a Jam user. Ask for a share. |
| 502 "the app at port … is not answering" | Nothing is listening on that port in the studio, or the app failed. |
| 504 "studio could not open the preview (is its image current?)" | The studio's cove-master predates previews. Raise the studio again. |
| 503 / 409 | The studio is not connected, or is pausing or stopping. Retry. |

## Troubleshooting

| You see | Meaning / fix |
|---------|---------------|
| 401 / "participant sign-in is missing or expired" | Run `at-jam login --participant` again (the ID token's lifetime is your IdP's). |
| 403 "not linked to a Jam user" | Ask an operator to link your IdP login to your Jam user. |
| 403 "you don't have SSH access" | No rule allows you; ask for a share (`at-jam studio share`). |
| 404 | No such studio, or `ssh:` is not configured on this Jam. |
| 409 not running / "pausing or stopping" | The studio is terminating or gone — or was mid-pause; retry, and the pause is undone. |
| 429 too many tunnels | 16 are open to this studio; close one and retry. |
| 503 not connected / "could not resume" | The studio's Attach stream is down, or resuming the paused studio failed; retry shortly. |
| 504 "resumed but did not reconnect" | The paused studio was resumed but its stream took over 10 s to return; retry. |
| 504 dial-back timeout | The studio's image predates collaboration — re-raise it (`at-jam standing upgrade` for a standing session). |
| `Permission denied (publickey)` on connect | Once, after a long gap: the cert on disk had expired (OpenSSH read it before `ssh-proxy` renewed it); reconnect — `at-jam ssh` renews first. Every time: the studio's sshd does not trust Jam's CA — look in serve's log for `SSH user CA install failed` for that session, then re-raise the studio — `at-jam standing upgrade --force` for a standing session (its image is current, so `--force`), else end it and raise a new one; reconnecting won't help. |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | The studio was rebuilt or upgraded and its sshd has a new host key. Drop the old one — `ssh-keygen -R jam-<studio>` (it is pinned in `~/.ssh/known_hosts` under that alias) — and reconnect. |
| Behind a corporate proxy, `ssh-proxy` can't reach Jam | It does not use `HTTPS_PROXY`; Jam's admin URL must be directly reachable. |
