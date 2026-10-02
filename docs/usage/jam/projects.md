---
summary: The Project lifecycle on a Jam — `at-jam project create|list|rm`, the rule that roles, grants, roster, escalation and chat service may only name an existing project, the `default` exception, and how pre-existing data is backfilled.
read_when: You are starting a new project on a Jam, a role/grant/roster/escalation write failed with "project not found", you want to delete a project, or you upgraded a Jam whose projects used to exist only as names.
owns: the Project lifecycle — create/list/rm, existence enforcement, the default-project exception, the in-use refusal, and the upgrade backfill
prereqs: roster.md for what Roles and Grants are; operators.md for the admin-client flags
tier: leaf
updated: 2026-10-02
---

# Projects

A **Project** is a first-class record on a Jam: you create it before anything
can live in it, and you can remove it only once nothing references it. It is
the top of the config tree: it owns its [roster](comms-addressing.md),
[escalation policy](escalation.md) and [chat service](discord.md), and every
[Role and Grant](roster.md) is keyed by it.

## Verbs

```
at-jam project create acme   # a new, empty project
at-jam project list          # every project, one per line
at-jam project rm acme       # refused while a role or grant references it
```

Each is an admin-API client; for its target and auth flags see
[operators.md](operators.md).

## Existence is enforced

Every project-scoped write names a project that must already exist:
`role add`, `grant`, `enroll`, `project roster add-human|add-channel`,
`project escalation set`, `project chat-service set`. Naming an unknown project
fails (HTTP **404**, and the error says to run `at-jam project create`). Nothing
is created as a side effect.

**The `default` exception.** The `default` project (also what an omitted
`--project` means) is created by the first write that names it. That way a
zero-config, single-project Jam keeps working without a `project create` step.

**Removal.** `project rm` refuses (HTTP **409**) while any role in the project
or any actor's grant into it still exists, and names one such reference.
Remove those first (`role rm`, `ungrant`/`revoke`). The project's roster,
escalation and chat service go with it.

## Admin API

| Route | Result |
|-------|--------|
| `GET /admin/projects` | **200** a sorted JSON array of project names. |
| `POST /admin/projects` body `{"name":"acme"}` | **201**. **409** if it exists, **400** if the name is empty. |
| `DELETE /admin/projects/{project}` | **204**. **409** while referenced, **404** if absent. |

## Upgrading an existing Jam

Before this change a project existed only as a string on a role or grant. On
upgrade every such name gets an empty project record, so nothing dangles:

- **Postgres** (`store-postgres`): migration `0003` inserts the missing
  `projects` rows from `roles` and actor grants, then adds a foreign key from
  `roles.project` to `projects.name`. Grants live inside the actor document, so
  the store enforces their project reference itself.
- **File store**: the records are added when the store loads and are written
  out with the next save.
- **`at-jam import`**: an older backup whose roles or grants name projects it
  has no record for gets the same empty records (see [backup.md](backup.md)).
