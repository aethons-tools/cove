---
summary: Loose backlog of known follow-ups not yet filed as tickets.
read_when: You want the loose backlog of known follow-ups not yet filed as tickets.
owns: the informal TODO backlog
prereqs: none
tier: leaf
updated: 2026-10-02
---

* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.* Studio kits: resolve a tag-form `base.image` to its `@sha256` digest before `BuildDigest`,
  so a moved tag rebuilds (spec 2026-10-02-kit-build-drift).
* Studio kits: garbage-collect superseded `cove-kit:*` images on the substrate
  (every assembly-fingerprint change leaves the old tags).
* Managed studios: flag a running studio whose *image* predates the current assembly fingerprint
  (record the tag on the Instance), mirroring the connector `stale` column.
