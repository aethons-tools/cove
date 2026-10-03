---
summary: Loose backlog of known follow-ups not yet filed as tickets.
read_when: You want the loose backlog of known follow-ups not yet filed as tickets.
owns: the informal TODO backlog
prereqs: none
tier: leaf
updated: 2026-10-03
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
* Managed studios: flag a running studio whose *image* predates the current assembly fingerprint
  (record the tag on the Instance), mirroring the connector `stale` column.
* Session context: show the restated-fact lint warnings to the author when saving a role,
  project or Jam layer (API/CLI/UI). Today they only reach Jam's log at raise. Needs the
  Studio facts for a hypothetical session of that scope, and changes the PUT response
  (204 → a body with warnings) — design first ([session-context.md](usage/jam/session-context.md)).
* Session context: report the applied context fingerprint up the Attach stream and show a
  `context` column (current/stale) for studios, like the connector's `stale` column.
* Session context: a timed self-wake — let an agent end its turn asking to be woken at
  time T / after D (a wake-on timer plus an intercom `sleep` tool); until then the
  Boilerplate only promises "ending your turn waits for a message".
* Session context: a Postgres restart test for `jam_settings` (the Jam-wide layer
  survives a store reopen), in the store-integration suite.
