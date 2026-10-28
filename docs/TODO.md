---
summary: Loose backlog of known follow-ups not yet filed as tickets.
read_when: You want the loose backlog of known follow-ups not yet filed as tickets.
owns: the informal TODO backlog
prereqs: none
tier: leaf
updated: 2026-10-28
---

* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.
* Studio kits: resolve a tag-form `base.image` to its `@sha256` digest before `BuildDigest`,
  so a moved tag rebuilds (spec 2026-10-02-kit-build-drift).
* Studio kits: garbage-collect superseded `jam-kit:*` images on the substrate
  (every assembly-fingerprint change leaves the old tags).
* Session context: show the restated-fact lint warnings to the author when saving a role,
  project or Jam layer (API/CLI/UI). Today they only reach Jam's log at raise. Needs the
  Studio facts for a hypothetical session of that scope, and changes the PUT response
  (204 → a body with warnings) — design first ([session-context.md](usage/jam/session-context.md)).
* Session context: report the applied context fingerprint up the Attach stream and show a
  `context` column (current/stale) for studios, like the connector's `stale` column.
* Turn end: cap consecutive failed agent turns. A session whose agent crashes every turn
  (revoked auth/model) is kept alive by its alarms or an on-idle `wake` idle timeout and
  never ends, holding its slot; after N failed turns it should end (a ticket marked
  blocked). See [turn-end.md](usage/jam/turn-end.md).
* Context lifecycle for long-running (resident) sessions: automatic compact/clear policies.
  Manual Compact and Clear exist ([session-view.md](usage/jam/session-view.md#session-controls));
  resident sessions otherwise `--continue` forever and rely on claude's auto-compaction. Design
  first; a timer or idle wake is a natural seam.
* Session context: a Postgres restart test for `jam_settings` (the Jam-wide layer
  survives a store reopen), in the store-integration suite.
* Connections: more than one connection of a kind (two Linear workspaces, two Discord
  bots). Serve config names one connection per block today, the relay runs one engine
  per service, and its cursor/marker state files are keyed by service: re-key them by
  connection id (spec slice 1 §7) when relays run per connection.
* Studio SSH: Jam-signed **host** certificates. Sign each studio's sshd host key with the
  SSH CA at raise (`HostCertificate`), and have `jam ssh-config`/`ssh` trust it via a
  `@cert-authority` known_hosts entry, so no studio's key is ever prompted for or pinned:
  a rebuilt or upgraded studio stops failing with "REMOTE HOST IDENTIFICATION HAS CHANGED".
  Today `StrictHostKeyChecking accept-new` only removes the first-connect prompt
  ([collaboration.md](usage/jam/collaboration.md)).
* Kit git creds: honour a `user:token` value on github.com and `gitlab` hosts too.
  `basicAuth` (`internal/jam/gitsource/creds.go`) sends the whole value as the password
  there (user `x-access-token`/`oauth2`), so `user:token` gets a 401, contradicting
  [kits-git.md](usage/jam/kits-git.md). Keep the host defaults for a bare token.
* Kit git creds: trim surrounding whitespace from a resolved `kits.git` value (kit-git
  path only — not `secret.Resolve`, whose other consumers are byte-exact). A literal is
  not trimmed at all and a command loses only one trailing `\n`, so a `\r\n` token file
  fails as an opaque 401. An all-whitespace value should error, naming the cred.
* Model-specs: warn (never refuse) when a role's spec names a `principal.credential`
  other than the `cred_name` of the role's provider destination (matched as for header
  rules). The broker injects the destination's credential and ignores the principal
  ([model-specs.md](usage/jam/model-specs.md)), so the spec can silently misreport the
  account. Surface it on the spec/role UI pages and as a WARN at serve start.
* UI auth: don't fall back silently to the loopback operator `local` when a request
  carries a session cookie that fails verification (`browserauth.Gate`, loopback trust).
  A wrong `audience`/`require-scope` then reads as "sign in to request an agent" with
  no hint; log the verification failure and/or show it in the UI.
* Structured turn end (deferred minors from the 2026-10-28 review):
  * a compact asked for with `turn_end` while a wake is pending runs one turn late (the resume is written first);
  * an inbox entry over the 32 KiB batch cap blocks auto-commit until the agent commits it itself (logged);
  * a kept process's shell writes through a pipe to jam-studio-agent, so it can die of SIGPIPE while the agent is down,
    and its surviving children then escape restart cleanup — consider an `*os.File` output with rotation at start.
