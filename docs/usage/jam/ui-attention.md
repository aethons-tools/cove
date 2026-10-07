---
summary: The admin UI's attention badges — what counts as needing attention (broken studios, out-of-date studios, config that names something missing), which scope and tab each item belongs to, how the rail, tabs and rows show it, and the Needs attention cards.
read_when: You see a red or amber badge in the admin UI's rail, on a tab or on a row and want to know what it counts and where to fix it, or you want to know why a project or Jam is (or isn't) flagged.
owns: the attention model (kinds, severity, scope and tab of each item), the rail/tab/row badges and the Needs attention cards
prereqs: ui.md for the rail and tabs; ui-projects.md for a project's tabs; coves.md for studio phases, image and connector status
tier: leaf
updated: 2026-10-07
---

# Attention badges

The admin UI computes one list of things that need an operator's attention and
shows it in three places: a **badge** on each rail entry, a badge on the tab
that owns each item, and a **⚠** on the row it names. A badge is a count of
items, **red** when any of them is broken, **amber** otherwise; hover for the
breakdown (`1 broken · 2 out of date`). The rail refreshes every 3 seconds, so
its badges stay live on every page.

| Kind | Severity | Raised when | Scope · tab |
|---|---|---|---|
| broken | red | a studio is `lost` or `terminating`, or its egress re-apply is failing | its project · Agents |
| out of date | amber | a studio runs a stale image or connector ([coves.md](coves.md#the-studio-verbs)) | its project · Agents |
| config | amber | an escalation target names nobody on the project | the project · Escalation |
| config | amber | a role's kit or model-spec no longer exists | the project · Roles |
| config | amber | a destination is in a connector conflict ([ui-pages.md](ui-pages.md#destination-pages)) | Jam · Specs |
| config | amber | a kit's current version does not parse | Jam · Specs |

One studio counts once, at its worst (a lost studio on a stale image is one
broken item). **Jam's badge counts only Jam's own items** — never the sum of its
projects; each project's badge counts that project's.

**Needs attention.** A project's Overview and Jam's Dashboard each list their
scope's items, with why and a link to where to fix it, or say nothing needs
attention. An agent linked from there opens under its project.
