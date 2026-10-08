---
summary: The Project lifecycle on a Jam — `at-jam project create|list|rename|rm`, the rule that roles, grants, roster, escalation and chat service may only name an existing project, the `default` exception, and how pre-existing data is backfilled.
read_when: You are starting a new project on a Jam, a role/grant/roster/escalation write failed with "project not found", you want to delete a project, or you upgraded a Jam whose projects used to exist only as names.
owns: the Project lifecycle — create/list/rename/rm, tombstones, existence enforcement, the default-project exception, the in-use refusal, and the upgrade backfill
prereqs: roster.md for what Roles and Grants are; operators.md for the admin-client flags
tier: leaf
updated: 2026-10-07
---

# Projects

A **Project** is a first-class record on a Jam: you create it before anything
can live in it, and you can remove it only once nothing references it. It is
the top of the config tree: it owns its [roster](comms-addressing.md),
[escalation policy](escalation.md) and [chat service](discord.md), and every
[Role and Grant](roster.md) is keyed by it. Each project has an id (`prj_…`):
roles, grants, sessions and the allocation ledger refer to it by that id, so
renaming a project leaves them alone; you name it by name everywhere you type
it (CLI, admin API, UI), and those show names back.

## Verbs

```
at-jam project create acme   # a new, empty project
at-jam project list          # every project, one per line
at-jam project rename acme apex   # in place: everything refers to it by id
at-jam project rm acme       # refused while a role or grant references it
```

Each is an admin-API client; for its target and auth flags see
[operators.md](operators.md). The admin UI does the same from the rail (**+ New project**) and each
project's pages ([ui-projects.md](ui-projects.md)). A project's goals and resources for its
sessions are set with `at-jam context --project` ([session-context-authoring.md](session-context-authoring.md)).

## Existence is enforced

Every project-scoped write names a project that must already exist:
`role add`, `grant`, `enroll`, `project member add`, `room add`,
`project escalation set`, `project chat-service set`. Naming an unknown project
fails (HTTP **404**, and the error says to run `at-jam project create`). Nothing
is created as a side effect.

**The `default` exception.** The `default` project (also what an omitted
`--project` means) is created by the first write that names it. That way a
zero-config, single-project Jam keeps working without a `project create` step.

**Removal.** `project rm` (by name or id) refuses (HTTP **409**) while the
project still has a member, a standing session being ended, a role, a session
not yet gone, or an actor's grant into it, and names one such reference.
Remove those first (`project member rm`, `role rm`, `ungrant`/`revoke`). The project's escalation
and chat service go with it, and its rooms and other channels are archived
(their history stays readable). A removed project is a **tombstone**: its id
still resolves (shown as "acme (removed)") and its name is free — a project
created under it later is a new project that inherits nothing.

**Rename.** `project rename <project> <new-name>` (also on the project's UI
page) changes the name only: roles, grants, sessions, members, channels and
the allocation ledger refer to the project by id, so nothing else changes.
The new name must be free and must not look like a project id (`prj_…`);
`default` is never renamed, nor is another project renamed to it. A running
serve keeps dispatching for its Requisitioner's project (resolved to its id at
startup), but **update anything that names the project by name** before the
next restart: a serve config's `runtime.requisitioner.project`, a
`dev-identity`'s deprecated `{project, human}` form, and scripts. Some things
keep the old name: a running session's context until it is next raised, and
the read-only History of the pre-channel-log intercom.

## Admin API

| Route | Result |
|-------|--------|
| `GET /admin/projects` | **200** a sorted JSON array of project names. |
| `POST /admin/projects` body `{"name":"acme"}` | **201**. **409** if it exists, **400** if the name is empty. |
| `PUT /admin/projects/{project}/name` body `{"name":"apex"}` | **204**. **409** if the name is taken, **404** if absent, **400** for an invalid name or the `default` project. |
| `DELETE /admin/projects/{project}` | **204** (a tombstone). **409** while referenced, **404** if absent. |

## Upgrading an existing Jam

Before this change a project existed only as a string on a role or grant. On
upgrade every such name gets an empty project record, so nothing dangles:

- **Postgres** (`store-postgres`): migration `0003` inserts the missing
  `projects` rows from `roles` and actor grants, then adds a foreign key from
  `roles.project` to `projects.name`. Grants live inside the actor document, so
  the store enforces their project reference itself.
- **`at-jam import`**: an older backup whose roles or grants name projects it
  has no record for gets the same empty records (see [backup.md](backup.md)).
