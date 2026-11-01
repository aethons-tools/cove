---
summary: The admin console's Tracking page (Config › Tracking, /app/admin/config/tracking) — the followed config repo, its last apply and the follower's state, the objects the repo manages, and Diff then Apply this plan.
read_when: You are using the admin console's Tracking page to review or apply the followed config repo, or to confirm its first apply.
owns: the Tracking page (source card, managed-objects table, the rendered plan, Apply's digest binding, the follower's state on the source card)
prereqs: config-git.md (plan actions, marks); config-git-sources.md (the followed repo); ui-editing.md (the write banner, CSRF and audit rules)
tier: leaf
updated: 2026-11-01
---

# The Tracking page

The **Tracking** page (`/app/admin/config/tracking`, the first node of the
[Config tree](ui-console.md#finding-a-page) and where the **Config** tab opens) is the UI for
a Jam's configuration in git ([config-git.md](config-git.md)). It works on the
**followed repo** (`config.github`, `config.gitlab` or `config.git`, [config-git-sources.md](config-git-sources.md));
`--git` sources and `--dir` checkouts are planned from the CLI (`config diff`).

Changing config happens on a branch of the followed repo: [config-branches.md](config-branches.md).

## The page

- **Source** — the followed repo, dir and branch, and the commit and time of
  the last apply from it, linked on GitHub and GitLab. **Following** is the
  follower's state (`in sync`, `waiting for you`, `failed`) and when it last
  checked; **Head** is the branch head it saw. `waiting for you` is the first
  apply's confirm: **Diff**, review the plan, **Apply this plan**. A failed check shows its
  error; the Jam retries every minute. Without a config repo the card says
  to set `config.github`, `config.gitlab` or `config.git`. There is no Track or Untrack: the serve config names the repo.
- **Managed objects** — every object the repo manages, linked to its page
  (a connection has none), with the commit it was applied from and when.

## Diff and Apply

**Diff** plans the followed repo and renders the plan: each changing op
with its file and what it overwrites (a line diff; a kit's version diff),
kit sources and their commits, forgotten marks, unmanaged objects, the
serve-config comparison, and any refusals. **Apply this plan** sends back
the digest of the plan on screen: if Jam or the repo changed since, it
answers 409 and writes nothing — Diff again. It is offered only when
nothing is refused. Without a git fetcher on the serve, Diff and Apply are
not offered.

Every write here obeys the UI's gate, CSRF origin check and audit logging
([ui-editing.md](ui-editing.md)).
