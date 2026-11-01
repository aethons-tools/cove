---
summary: Writing the authored layers of a Jam session's context — a role's rules, a project's goals and resources, and Jam-wide standing rules — in the config repo, with `jam context show` to read them back.
read_when: You want sessions of a role or project (or every session on the Jam) to know something at raise — rules, goals, repos — and need where it goes in the config repo, its format, limits or API.
owns: where each authored layer lives in the config repo and its format, the `jam context show` command, authored-layer limits, and the role/project/jam context admin routes
prereqs: session-context.md for how layers are compiled and delivered
tier: leaf
updated: 2026-11-01
---

# Authoring session context

Three layers of a session's [context](session-context.md) are written by operators;
each is applied at the session's **next raise**:

| Layer | In the config repo | Core budget | Delivered |
|-------|------------|-------------|-----------|
| Project | `projects/<p>/context.md` (resources: `project.yml`) | 1200 B, including the one-line resources pointer | after Studio |
| Role | `context:` in `projects/<p>/roles/<r>.yml` | 1200 B | after Project |
| Jam | `context.md` at the root | 800 B | last — wins on conflict |

A layer is a short **core** (always in the session's system prompt) and optional
**leaves** (files under `/agent-data/context/<layer>/`, opened when their
`read-when` matches). Keep rules in the core; put detail in leaves.

## The files

Each layer changes only by a commit to the config repo
([config-git.md](config-git.md#layout)), by hand or on a branch in the admin
console ([config-branches.md](config-branches.md)). A `context.md` (Jam and
project) is optional YAML front matter holding `leaves:`, then the core as
the markdown body:

```markdown
---
leaves:
  - name: release.md            # lowercase [a-z0-9._-], ends .md
    read-when: you are cutting a release
    body: |
      Tag from main, then …
---
Ship the release by Friday. Ask in channel:ops before touching prod.
```

A role's layer is inline in its file:

```yaml
# projects/acme/roles/reviewer.yml
context:
  core: Review for security first.
  leaves: [{name: style.md, read-when: you comment on style, body: "…"}]
```

A project's resources are in its `project.yml` (at most 50):

```yaml
resources:
  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}
```

`kind` is `repo`, `doc`, `tracker` or `url`. Resources become a
`project/resources.md` leaf with a pointer line in the project core.

## Reading them back

```
jam context show --role acme/reviewer      # or --project acme, or --jam: the layer as YAML (bodies inline)
```

The **Agent context** card on the role page, the project page, or (for the
Jam-wide layer) the dashboard in the [admin UI](ui-pages.md) shows the same
YAML, with a byte counter against the budget; on a branch it edits it.
`show` takes the usual admin-client flags ([operators.md](operators.md)). The
admin API route is `GET /admin/roles/{project}/{role}/context`
(`/admin/projects/{project}/context`, `/admin/jam/context`), answering
`{core, leaves: [{name, read_when, body}], resources}`.

## Limits and rules

- An over-budget core, a bad or duplicate leaf name, a leaf without a read-when (or
  one over 160 bytes), more than 20 leaves or 64 KiB of leaf bodies, a bad resource,
  or a project leaf named `resources.md` (reserved for the generated list) is
  refused by the plan and nothing changes.
- Raise warns in Jam's log when an authored core restates an egress host or a
  message target — the Studio layer already lists those.
- Context is plain text in every session's prompt: **never put a credential in it.**
- Role, project and Jam context are part of `jam export` ([backup.md](backup.md)).
