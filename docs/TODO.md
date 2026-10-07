---
summary: Loose backlog of known follow-ups not yet filed as tickets.
read_when: You want the loose backlog of known follow-ups not yet filed as tickets.
owns: the informal TODO backlog
prereqs: none
tier: leaf
updated: 2026-10-07
---

* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.
* Studio kits: resolve a tag-form `base.image` to its `@sha256` digest before `BuildDigest`,
  so a moved tag rebuilds (spec 2026-10-02-kit-build-drift).
* Studio kits: garbage-collect superseded `cove-kit:*` images on the substrate
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
* Context lifecycle for long-running (resident) sessions: compaction, clearing and memory.
  Today resident sessions `--continue` forever and rely on claude's auto-compaction. Design
  first; a timer or idle wake is a natural seam to start a fresh episode.
* Session context: a Postgres restart test for `jam_settings` (the Jam-wide layer
  survives a store reopen), in the store-integration suite.
* Connections: more than one connection of a kind (two Linear workspaces, two Discord
  bots). Serve config names one connection per block today, the relay runs one engine
  per service, and its cursor/marker state files are keyed by service: re-key them by
  connection id (spec slice 1 §7) when relays run per connection.
* `/me` writes (`/me/send`, `/me/read`, `/me/join|leave|call-in`): add an Origin check
  like `/ui`'s `originGuard` (configured `ui-origins`). Today they rest on the
  `SameSite=Lax` session cookie alone, which a sibling subdomain can get past.
