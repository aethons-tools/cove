---
summary: The admin UI's Config tab (/ui/config) — the tracked config source and its last apply, the managed objects and their state (in sync, drifted, missing), Diff/Apply/Track/Untrack/Release — and the `managed · config @<commit>` badge and drift warning on object pages.
read_when: You are using the admin UI's Config tab to review or apply the tracked config repo, see a `managed · config @…` badge or a "This will drift from the config repo" prompt on an object page, or want to stop the repo managing an object from the UI.
owns: the Config page (source card, managed-objects table and states, the rendered plan, Apply's digest binding and force checkbox, Track/Untrack/Release from the UI), the managed badge and the drift-on-save warning
prereqs: config-git.md (plan actions, marks, drift); config-git-sources.md (the tracked source); ui-editing.md (the write banner, CSRF and audit rules)
tier: leaf
updated: 2026-10-22
---

# The Config tab and managed objects

Jam's **Config** tab (`/ui/config`, between Specs and Intercom) is the UI for
a Jam's configuration in git ([config-git.md](config-git.md)). It works on the
**tracked source** ([config-git-sources.md](config-git-sources.md)); `--git`
other sources and `--dir` checkouts stay in the CLI.

## The page

- **Source** — the tracked repo, dir and ref, and the commit and time of the
  last apply from it, linked on GitHub and GitLab. **Untrack** forgets it
  (objects and marks stay). With none tracked, a **Track** form fetches and
  plans a source, shows the plan and records it; nothing is applied. Track is
  offered only then: to switch sources, Untrack first (a Track while one is
  tracked is refused, 409).
- **Managed objects** — every object the repo manages: the commit it was
  applied from, when, and its state: `in sync`, `drifted` (edited in Jam
  since), or `missing` (removed in Jam since). The header counts those that
  differ. With no source tracked (a `--dir` Jam) the header shows the newest
  apply. A missing object and a connection (no page) are not linked.
  **Release** stops the repo managing one, as `config release` does
  ([config-git.md](config-git.md#drift-and-release)). If the live Jam cannot
  be compared (any kit that does not parse), states read `unknown`.

Untrack and Release wait behind a running apply or sync, up to its fetch
deadline of 4 minutes ([locks](config-git-sources.md#locks)).

## Diff and Apply

**Diff** plans the tracked source and renders the plan: each changing op
with its file and a line diff (a drifted op opens Jam's side against the
repo's; a kit's config is shown indented), kit sources and their commits,
forgotten marks, unmanaged objects, the serve-config comparison, and any
refusals. **Apply** sends back the digest of the plan on screen: if Jam or
the repo changed since, it answers 409 and writes nothing — Diff again. It
is offered only when nothing is refused.

When the plan has drift, a **Force** checkbox re-plans as a forced apply
(drift becomes `update`/`delete`, not a refusal); Apply then carries that
plan's digest and `force`. The checkbox does not appear otherwise. Without a
git fetcher on the serve, Diff, Apply and Track are not offered.

## Badges and the drift warning

An object the repo manages shows a **`managed · config @<commit[:7]>`** badge
(`@dir` for a `--dir` apply) by its name, linking here: destinations,
model-specs, kits, users, projects, roles, and the Jam context card on the
Dashboard. Every write to one — save, delete, rename, member, room,
escalation and context edits, kit push, pin, sync, track and untrack — first
asks **"This will drift from the config repo (managed · config @…).
Save anyway?"**. Fields and actions the repo does not hold do not ask: a
user's OIDC identities and accounts, standing reset/upgrade and request,
grants, creating a role, kit import, and the create forms. The prompt is the
browser's; the admin API and CLI do not ask. A drifted object raises
`config.drift` within a minute ([config-git.md](config-git.md#drift-and-release)).

Every write here obeys the UI's gate, CSRF origin check and audit logging
([ui-editing.md](ui-editing.md)); errors show in the page banner with the
admin API's status and text.
