---
summary: Operator notes — the `operator-notes:` markdown field every config object carries (the Jam in jam.yml, destinations, model specs, users, connections, kits, projects, roles), seen only by operators and never by agents; where each kind keeps it and where the console shows and edits it.
read_when: You want to record why a config object exists or how to handle it, for other operators only; are wondering whether agents see an object's notes; or a notes edit, its plan op or its console section behaves unexpectedly.
owns: the operator-notes field (per-kind location, the no-agent guarantee, the Jam's notes object, the console's Operator notes section and its writes)
prereqs: config-git.md (file kinds, the plan); config-branches.md (editing on a branch)
tier: leaf
updated: 2026-11-03
---

# Operator notes

Every config object can carry **operator notes**: free markdown of any length,
written for the operators who run the Jam. Agents never see them, and they have
no effect on anything Jam does. They are config like any other field: kept in
the repo, applied, exported and backed up. An edit is an ordinary `update` in
the plan.

## Where they live

The key is `operator-notes:` in every kind. A YAML block scalar keeps the
markdown readable:

```yaml
# destinations/gh.yml
kind: destination
version: 1
route: /gh/
upstream: https://api.github.com
operator-notes: |
  ## Why a PAT
  The GitHub App can't reach the enterprise org yet. Rotate the PAT with **ops**.
```

| Object | File | Console |
|---|---|---|
| the Jam | `jam.yml` | the Dashboard (shown only) |
| destination, model spec, user, project, role | its `.yml` | its page; edit on a branch |
| connection | `connections/<n>.yml` | not shown (connections have no page) |
| kit | its own `kit.yml`, in the config repo or at its git source | its page (shown only) |

The planner treats `jam.yml`'s `operator-notes` as the Jam's own config object
(`jam-notes jam` in a plan). They never count as a serve setting, so editing
them never makes jam.yml "differ" or need a restart. `jam config write` writes
them back into jam.yml, and a branch never writes jam.yml
([config-branches.md](config-branches.md)). A non-string value is refused:
`jam.yml: operator-notes must be a string`.

A kit's notes belong to that kit's own kit.yml. A kit that builds on another
does not inherit them, and an edit is a new kit version, as any kit.yml edit is.
They sit outside the build digest, so editing them never rebuilds an image.
`kits/<n>.source.yml` has no notes: the kit's kit.yml at its source holds them.

## Never seen by agents

The notes are not part of any session-context layer. A role's or project's
`context:` and the Jam's `context.md` are what sessions read
([session-context-authoring.md](session-context-authoring.md)).

Do not confuse them with the agent-facing fields that sound alike:

- A destination's `note:` is shown to sessions granted that destination.
- A kit's `notes:` are context leaves that ship into its sessions.

Some objects reach a studio. A model spec's notes are stripped from the spec
delivered to a cove. kit.yml is never part of a kit's build context.
Everything else reaches studios only field by field.

Every console and admin API that returns the notes needs operator rights.

## In the console

Each page above opens with an **Operator notes** card: the markdown rendered as
a message body is (no raw HTML), marked "for operators only". On a branch, the
card has **Add notes** / **Edit**: a markdown text box whose **Save notes**
commits to the branch. Off a branch, a page with no notes shows no card.

The writes are `POST /app/admin/api/<object>/operator-notes` with
`{"notes": "…"}`. `<object>` is `users/<u>`, `destinations/<n>`,
`model-specs/<n>`, `projects/<p>` or `projects/<p>/roles/<r>`. The answer is
`{markdown, html}`. Each write changes only the notes, and the destination and
model-spec form saves keep them. Off a branch the write is refused like any
config write (409).
