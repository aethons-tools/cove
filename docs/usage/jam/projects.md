---
summary: The Project lifecycle on a Jam — a project as `projects/<name>/project.yml` in the config repo, `jam project list`, the rule that roles, grants, members, escalation and chat service may only name an existing project, the `default` exception, removal and tombstones, and how pre-existing data is backfilled.
read_when: You are starting a new project on a Jam, a grant or a config plan failed with "project not found", you want to delete or rename a project, or you upgraded a Jam whose projects used to exist only as names.
owns: the Project lifecycle — adding, listing, renaming and removing a project, tombstones, existence enforcement, the default-project exception, the in-use refusal, and the upgrade backfill
prereqs: roster.md for what Roles and Grants are; operators.md for the admin-client flags
tier: leaf
updated: 2026-11-01
---

# Projects

A **Project** is a first-class record on a Jam: you create it before anything
can live in it, and you can remove it only once nothing references it. It is
the top of the config tree: it owns its [roster](comms-addressing.md),
[escalation policy](escalation.md) and [chat service](discord.md), and every
[Role and Grant](roster.md) is keyed by it. Each project has an id (`prj_…`):
roles, grants, sessions and the allocation ledger refer to it by that id, so
you name it by name everywhere you type it (the config repo, CLI, admin API,
UI), and those show names back.

## Adding, listing and removing

A project is a directory in the config repo, `projects/<name>/` with its
`project.yml` (members, rooms, escalation, chat service), its `context.md` and
its `roles/` ([config-git.md](config-git.md#layout)). It changes only by a
commit: by hand, or on a branch in the admin console (Config › Projects,
**New project**, and each project's pages: [ui-projects.md](ui-projects.md);
[config-branches.md](config-branches.md)). A project's goals and resources for
its sessions are its `context.md` ([session-context-authoring.md](session-context-authoring.md)).

```
jam project list          # every project, one per line
```

Its admin-client flags: [operators.md](operators.md).

## Existence is enforced

Everything project-scoped names a project that must already exist: a role,
a member, a room, escalation and chat service (in the repo, the project's own
directory), and a `grant` or `enroll`. Naming an unknown project fails (a
plan names it, `project <name> is not in the repo or Jam`; a grant answers HTTP
**404**). Nothing is created as a side effect.

**The `default` exception.** The `default` project (also what an omitted
`--project` means) is created by the first write that names it. That way a
zero-config, single-project Jam keeps working without adding it to the repo.

**Removal.** Removing a managed project's directory deletes it; the plan
refuses (`cannot delete project acme: …`) while something still uses it:
live instances, grants into it, standing sessions, roles the repo doesn't
manage ([config-git.md](config-git.md#the-plan) has the full guard). End those
first (`ungrant`/`revoke` for grants).
The project's escalation and chat service go with it, and its rooms and other
channels are archived (their history stays readable). A removed project is a
**tombstone**: its id still resolves (shown as "acme (removed)") and its name
is free — a project created under it later is a new project that inherits
nothing.

**Rename.** A project's name is its directory: moving `projects/acme/` to
`projects/apex/` is a delete of `acme` and a create of `apex`, a new project
with a new id, under the removal rules above. The admin console's **Rename** on
a branch commits the same move. **Update anything that names the project by
name** too: a serve config's `runtime.requisitioner.project`, a
`dev-identity`'s deprecated `{project, human}` form, and scripts.

## Admin API

| Route | Result |
|-------|--------|
| `GET /admin/projects` | **200** a sorted JSON array of project names. |

## Upgrading an existing Jam

Before this change a project existed only as a string on a role or grant. On
upgrade every such name gets an empty project record, so nothing dangles:

- **Postgres** (`store-postgres`): migration `0003` inserts the missing
  `projects` rows from `roles` and actor grants, then adds a foreign key from
  `roles.project` to `projects.name`. Grants live inside the actor document, so
  the store enforces their project reference itself.
- **`jam import`**: an older backup whose roles or grants name projects it
  has no record for gets the same empty records (see [backup.md](backup.md)).
