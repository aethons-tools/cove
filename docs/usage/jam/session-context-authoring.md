---
summary: Writing the authored layers of a Jam session's context — a role's rules, a project's goals and resources, and Jam-wide standing rules — with `at-jam context` or the admin API.
read_when: You want sessions of a role or project (or every session on the Jam) to know something at raise — rules, goals, repos — and need the file format, commands, limits or API.
owns: the `at-jam context show|set|clear` command, the context YAML format, authored-layer limits, and the role/project/jam context admin routes
prereqs: session-context.md for how layers are compiled and delivered
tier: leaf
updated: 2026-10-03
---

# Authoring session context

Three layers of a session's [context](session-context.md) are written by operators;
each is applied at the session's **next raise**:

| Layer | Scope flag | Core budget | Delivered |
|-------|------------|-------------|-----------|
| Project | `--project p` | 1200 B, including the one-line resources pointer | after Studio |
| Role | `--role [p/]r` (p defaults to `default`) | 1200 B | after Project |
| Jam | `--jam` | 800 B | last — wins on conflict |

A layer is a short **core** (always in the session's system prompt) and optional
**leaves** (files under `/agent-data/context/<layer>/`, opened when their
`read-when` matches). Keep rules in the core; put detail in leaves.

## The file

```yaml
core: |
  Ship the release by Friday. Ask in channel:ops before touching prod.
leaves:
  - name: release.md            # lowercase [a-z0-9._-], ends .md
    read-when: you are cutting a release
    file: release.md            # relative to this YAML; or `body: |` inline
resources:                      # --project only; at most 50
  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}
```

`kind` is `repo`, `doc`, `tracker` or `url`. Resources become a
`project/resources.md` leaf with a pointer line in the project core.

## Commands

```
at-jam context set   --project acme --file acme-context.yml
at-jam context set   --role acme/reviewer --file reviewer.yml
at-jam context set   --jam --file jam-rules.yml
at-jam context show  --role acme/reviewer      # prints the same YAML (bodies inline)
at-jam context clear --jam
```

They take the usual admin-client flags ([operators.md](operators.md)). The admin
API routes are `GET|PUT|DELETE /admin/roles/{project}/{role}/context`,
`/admin/projects/{project}/context` and `/admin/jam/context`; the JSON body is
`{core, leaves: [{name, read_when, body}], resources}`.

## Limits and rules

- An over-budget core, a bad or duplicate leaf name, a leaf without a read-when (or
  one over 160 bytes), more than 20 leaves or 64 KiB of leaf bodies, a bad resource,
  or a project leaf named `resources.md` (reserved for the generated list) is
  refused (400) and nothing changes. An unknown role or project is a 404.
- `set` refuses a file that sets nothing; use `clear` to remove a layer.
- A role re-put keeps its context; only these routes change it.
- Raise warns in Jam's log when an authored core restates an egress host or a
  message target — the Studio layer already lists those.
- Context is plain text in every session's prompt: **never put a credential in it.**
- Role, project and Jam context are part of `at-jam export` ([backup.md](backup.md)).
