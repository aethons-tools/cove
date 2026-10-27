---
summary: Studio collaboration — reaching an agent's running studio through Jam: SSH and IDEs (VS Code Remote-SSH, JetBrains Gateway) via `jam ssh`, who may connect to a studio (operators, the session's requester, role `collaborate` policy, share grants — for SSH and previews alike), and how it works (Jam-issued short-lived certs over a studio-dialed tunnel). Previews themselves are in previews.md.
read_when: You want to SSH or point an IDE at an agent's studio, let a teammate in (`studio share`), set a role's `collaborate` policy, enable the `ssh:` serve config, or a `jam ssh` connection was refused or timed out.
owns: the studio-collaboration user and operator guide — jam ssh-config/ssh-proxy/ssh and login --participant, the access rules and share grants (studio share/unshare/access, revocation) for every right, the ssh: serve-config block's setup steps, and the tunnel/cert mechanism at the level an operator needs
prereqs: serve.md (serve config, credentials), roster.md (roles, users), operators.md (login)
tier: leaf
updated: 2026-10-25
---

# Studio collaboration (SSH, IDEs and previews)

A person can work *in* an agent's running studio — a shell, VS Code
Remote-SSH, JetBrains Gateway — or open a web app the agent serves there
([previews.md](previews.md)), through Jam alone: no host drive, no new port in the
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
   ([serve.md](serve.md)) and restart `jam serve`.
4. Let members in where wanted: `jam role add … --collaborate-ssh members`
   ([roster.md](roster.md)).

Studios raised after the restart trust the CA; one already running does not
until its next raise. The same holds for rotating the CA key: a studio picks
up the new key at its next raise, not before.

**Collaborator:**

```
jam login --participant          # once; again when it expires (below)
jam ssh <studio>                 # a shell
jam ssh-config --write <studio>  # or: an ~/.ssh/config entry for an IDE
```

Then in VS Code: *Remote-SSH: Connect to Host… → `jam-<studio>`* (JetBrains
Gateway: an SSH connection to the same host). Operators can do all of this with
their operator login instead (`--operator`).

## Signing in as a participant (`login --participant`)

`login --participant` runs the operator device flow ([operators.md](operators.md))
but signs you in as a **participant** — a Jam user, not an operator. It caches
only the OIDC **ID token** (audience = `device-client-id`, so `device-scope`
must include `openid`) in `~/.config/jam/{app}-participant-token.json`,
never touching the operator token. Your IdP login must be linked to a Jam user
([roster.md](roster.md)). `logout --participant` clears it.

ID tokens are short-lived — their lifetime is set by your IdP, typically
about 1 h — and the device flow gives no refresh, so when one expires (a 401), run `jam login --participant` again.

Jam mounts the participant routes (`/me/studios/{id}/ssh*`) only when
`operator-auth.oidc.device-client-id` and `ssh:` are both set. Operators use
`/admin/studios/{id}/ssh*` with the admin bearer.

## The ssh commands

| Command | What it does |
|---------|--------------|
| `jam ssh-config [--write] <studio>` | Prints a `Host jam-<studio>` block (`User agent`, `ProxyCommand <this jam's absolute path> ssh-proxy …` — so it works when `jam` isn't on ssh's PATH, e.g. a `dist/` build; rerun `ssh-config --write` after moving the binary —, `IdentityFile`/`CertificateFile` under `~/.config/jam/ssh/`, `StrictHostKeyChecking accept-new`) and fetches the studio's cert. `--write` installs it in `~/.ssh/config` between `# >>> jam <studio>` markers — at the top, since OpenSSH takes the first value it finds and an earlier `Host *` would win — ending in `Match all` so options below it still apply to every host; a rerun replaces the block in place. |
| `jam ssh-config --prune [--dry-run]` | Removes the `~/.ssh/config` blocks of studios their Jam no longer has, with each one's cert and its pinned host key (`ssh-keygen -R jam-<studio>`). Each block is checked against the Jam, app and plane its ProxyCommand pins (`GET …/studios/<studio>/access`); only a definite "no such studio" (404) removes it — a refusal, an unreachable Jam or one with `ssh:` off keeps it, with a note on stderr. `--dry-run` only reports. |
| `jam ssh-proxy <studio>` | The ProxyCommand: renews the cert when under 1 h remains, then tunnels ssh's stdio through Jam (`GET …/studios/<studio>/ssh`, `Upgrade: jam-tunnel`). |
| `jam ssh <studio> [ssh args…]` | Renews the cert if needed, then execs `ssh jam-<studio>` with the same options and your extra args — no `~/.ssh/config` edit needed. |

All three take `--app`, `--admin-url`, and `--participant` / `--operator` to
pick the plane; the default is the participant login when one is cached and
unexpired, else the operator token, and `ssh-config` pins the choice in the
ProxyCommand. The key (`id_ed25519`, 0600) is generated once; certs
(`<studio>-cert.pub`, under `<app>/` for a non-default `--app`) last 8 h.

**Host keys.** Both `ssh-config` and `jam ssh` set `StrictHostKeyChecking
accept-new`: an IDE runs ssh with no terminal, so it could not answer a
first-connect prompt. The first connection pins the studio's host key under the
`jam-<studio>` alias (the hop is Jam's authenticated tunnel to that studio); a
later change is still refused — see Troubleshooting.

OpenSSH reads `CertificateFile` *before* it runs the ProxyCommand, so a renewal
made by `ssh-proxy` applies from the next connection; `jam ssh` renews before
connecting, and an IDE simply reconnects. `ssh-proxy` dials Jam directly: it
does not honour `HTTPS_PROXY`/`HTTP_PROXY`.

## Who may connect

Jam checks each cert request and each connection against four rules, in order:

1. **Operators** — always.
2. **The requester** of a personal session ([personal-sessions.md](personal-sessions.md)).
3. **Project members**, as the role's collaboration policy says:
   `--collaborate-ssh members|none` (default `none`), `--collaborate-preview`
   (default `members`; see [previews.md](previews.md)) — see [roster.md](roster.md).
4. **A share grant** for the user on this session, unexpired, holding the right.

A removed user matches none of them. The `ide` preview (the studio's web
IDE, [previews.md](previews.md#a-web-ide)) checks `ssh`, not `preview`. A studio that is terminating or gone
takes no certs or tunnels (409); a paused one is resumed for an allowed caller
([Idle studios](#idle-studios)). The rules need no project membership: a
share grant works for a user who is in no project.

## Sharing

| Command | What it does |
|---------|--------------|
| `jam studio share <studio> --with user:<name> --rights preview,ssh [--for 8h]` | Grants the user those rights on the studio (`POST …/studios/<studio>/shares` `{user, rights, for}` → 201) until `--for` runs out: a Go duration (`4h`, `90m`) or whole days (`7d`); default 8 h, at most 7 d (longer → 400). Sharing again replaces the user's grant (dropping `ssh` closes all their open connections to the studio — SSH, IDE and previews — as unshare does). The grant is keyed to the session and removed when it ends. |
| `jam studio unshare <studio> --with user:<name>` | Removes the grant (`DELETE …/shares/<user>` → 204; none → 404; the user may also be a `usr_` id, or the name of a removed user with a leftover grant) and closes all the user's open connections to the studio (SSH, IDE and previews), unless they still hold `ssh` another way. |
| `jam studio access <studio>` | Who can reach it and why (`GET …/access` → `{requester, role, grants, live, previews_on, previews}`): operators, the requester, the role's policy for members, each grant (rights, expiry, granted by), the open connections (each with its right, `ssh` or `preview`) and a `PREVIEWS` section (name, port, URL). |

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

1. `jam ssh-proxy` asks Jam's admin/UI listener for the tunnel
   (`GET …/studios/<studio>/ssh`, HTTP/1.1 `Upgrade: jam-tunnel`).
2. Jam authorizes the caller, then pushes `OpenTunnel{id, port 2222}` down the
   studio's Attach stream ([coves.md](coves.md#the-attach-stream)).
3. The studio's jam-studio-agent dials the `Tunnel` RPC back to Jam on 443 (the
   same egress as Attach), claims the id, and connects it to its loopback sshd
   (`127.0.0.1:2222`). It dials only loopback, and only sshd's port and the
   ports the session exposes as [previews](previews.md)
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
studio (previews may hold at most 12 of them, [previews.md](previews.md#limits)), 30 min idle, 10 s for an SSH dial-back (a preview's waits as long as its request: 15 s, 30 s for `ide`); a teardown or a pause closes a
studio's tunnels and it takes none until it is resumed or raised again.

## Previews

An agent can also expose a web app it serves in its studio as an HTTPS
preview, which people open in a browser or call with a bearer token. That is
covered in [previews.md](previews.md).

## Troubleshooting

| You see | Meaning / fix |
|---------|---------------|
| 401 / "participant sign-in is missing or expired" | Run `jam login --participant` again (the ID token's lifetime is your IdP's). |
| 403 "not linked to a Jam user" | Ask an operator to link your IdP login to your Jam user. |
| 403 "you don't have SSH access" | No rule allows you; ask for a share (`jam studio share`). |
| 404 | No such studio, or `ssh:` is not configured on this Jam. |
| 409 not running / "pausing or stopping" | The studio is terminating or gone — or was mid-pause; retry, and the pause is undone. |
| 429 too many tunnels | 16 are open to this studio; close one and retry. |
| 503 not connected / "could not resume" | The studio's Attach stream is down, or resuming the paused studio failed; retry shortly. |
| 504 "resumed but did not reconnect" | The paused studio was resumed but its stream took over 10 s to return; retry. |
| 504 dial-back timeout | The studio's image predates collaboration — re-raise it (`jam standing upgrade` for a standing session). |
| `Permission denied (publickey)` on connect | Once, after a long gap: the cert on disk had expired (OpenSSH read it before `ssh-proxy` renewed it); reconnect — `jam ssh` renews first. Every time: the studio's sshd does not trust Jam's CA — look in serve's log for `SSH user CA install failed` for that session, then re-raise the studio — `jam standing upgrade --force` for a standing session (its image is current, so `--force`), else end it and raise a new one; reconnecting won't help. |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | The studio was rebuilt or upgraded and its sshd has a new host key. Drop the old one — `ssh-keygen -R jam-<studio>` (it is pinned in `~/.ssh/known_hosts` under that alias) — and reconnect. |
| Behind a corporate proxy, `ssh-proxy` can't reach Jam | It does not use `HTTPS_PROXY`; Jam's admin URL must be directly reachable. |
