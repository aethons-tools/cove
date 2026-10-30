---
summary: App tokens — the opaque bearer credential a user grants to a client such as the VS Code extension; the consent and PKCE sign-in, lifetimes, operator standing, where a bearer is accepted or refused, the per-user cap, and revocation.
read_when: You are signing a client in to Jam with an app token, debugging a 400/401/403 on a bearer request, deciding what an app token may reach, or revoking one (a user's Connected apps, an operator on the user page).
owns: the consent and PKCE flow and its two routes and parameters; the redirect prefixes and the 60 s single-use codes; app-token lifetimes (30 days, sliding or fixed, the 5-minute touch); operator standing taken at consent; bearer scope (accepted, refused, cookie+bearer 400); the per-user cap; revocation; app-token storage
prereqs: ui.md for the browser login and the Principal
tier: leaf
updated: 2026-10-30
---

# App tokens

An app token is an opaque bearer credential a user hands to a client, so the client acts as that user without a browser cookie.
Jam mints one only after the user signs in in a browser and allows it on a consent page.
The routes exist only when browser login is configured ([ui.md](ui.md#signing-in-one-login-one-principal)).

## Sign-in: consent and PKCE

| Route | Does |
|---|---|
| `GET /app/auth/app-token` | The consent page. Signs the user in first if needed, then asks Allow or Cancel. |
| `POST /app/auth/app-token` | The page's form. Same-origin checked. Redirects (303) to the client with `state` and either `code` or `error=access_denied`. |
| `POST /app/auth/app-token/exchange` | JSON `{code, code_verifier, redirect}`. Returns `{token, expires_at, user}`. |

The consent page takes these query parameters, all required except `client`:

- `redirect`: where to send the browser back.
- `state`: 1 to 512 bytes, echoed back.
- `code_challenge`: the base64url SHA-256 of the verifier, 43 characters.
- `code_challenge_method`: `S256` only.
- `client`: optional label shown on the page and kept on the token (64 characters at most, `VS Code` if empty).

Any bad parameter is a `400`, and Jam never redirects to a bad `redirect`.

### Redirects and codes

`redirect` must start with `vscode://aethons.jam/` or `vscode-insiders://aethons.jam/`, compared as an exact string prefix.

A code lives 60 s and works once.
A failed exchange burns it too.
It is bound to the challenge, the redirect, the user, the operator standing and the label.
At most 1024 codes are outstanding; past that, consent answers `503`.
The exchange answers `{"error":"invalid_grant"}` (`400`) for any failure, and sends `Cache-Control: no-store`.
The consent page sends `no-store`, `X-Frame-Options: DENY` and a CSP that allows only itself and the two redirect schemes as form targets.

## Lifetimes

A token lasts 30 days.

- **Member:** sliding. Each use sets the expiry to 30 days ahead.
- **Operator standing:** fixed at 30 days from issue.
- **Last used** is written at most once per token every 5 minutes, so a busy client writes the store that often at most.

Expired tokens are deleted hourly.

## Operator standing

If the user is an operator at consent, the token records the operator id.
Standing comes from the verified access-token cookie of the browser login, never from loopback trust or the dev identity.
Such a token acts as that operator in the user area and on `/me/studios` (SSH and previews on any session), as the browser session does.
It never reaches `/app/admin/`.

## Bearer scope

Send `Authorization: Bearer <token>`.

| Where | Result |
|---|---|
| `/app/user/*`, `/me/studios/*` and `/me/vscode*` | Accepted. |
| `POST`/`DELETE /app/user/push/subscriptions`, `GET /app/user/push/key` | `403`. Push belongs to a browser. |
| Anything under `/app/admin/`, loopback included | `401`. |
| A bearer together with a `jam_app_id` or `jam_app_at` cookie | `400`. |
| An unknown, expired or removed-user token | `401`. |

A bearer request skips the same-origin check, since it has no cookie to forge, and never gains loopback admin.
`/me/studios` and `/me/vscode` (the extension's download and update check, [vscode.md](vscode.md#updates)) also take a `login --participant` ID token ([collaboration.md](collaboration.md)).

## Per-user cap

A user keeps at most 20 tokens.
Adding a 21st evicts the least recently used one and ends its open requests.

## Revocation

- **The user:** Connected apps in the account menu of the app.
- **An operator:** the App tokens list on the user's admin page ([ui-pages.md](ui-pages.md#user-pages)).

Revoking ends the token's open `/app/user` requests, event streams included.
A token whose user is removed stops working at once, and its open requests end within 5 s.
SSH tunnels opened through `/me/studios` are not tracked: an open tunnel runs on, and new requests fail.

## Storage

Jam stores only the hash of a token, with its id (`apt_…`), label, owner, operator id and times.
Logs name a token by its id, never its value.
App tokens are not in a [`jam export`](backup.md).
