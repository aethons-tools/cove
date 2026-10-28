---
summary: The admin console's attention badges — what counts as needing attention (broken or out-of-date studios, config that names something missing, open Health conditions), where each item is counted, how the title-bar tabs, Config tree nodes and rows show it, and the Dashboard's Needs attention card.
read_when: You see a red or amber badge in the admin console's title bar or on a Config tree node, or a ⚠ on a row, and want to know what it counts and where to fix it, or you want to know why a project or Jam is (or isn't) flagged.
owns: the attention model (kinds, severity, where each item is counted), the title-bar/tree/row badges and the Needs attention card
prereqs: ui-console.md for the tabs and the Config tree; coves.md for studio phases, image and connector status
tier: leaf
updated: 2026-10-28
---

# Attention badges

The admin console computes one list of things that need an operator's
attention and shows it as a **badge** on the title-bar tabs and on the
[Config tree](ui-console.md#finding-a-page)'s nodes, and as a **⚠** on the row
it names. A badge is a count of items, **red** when any of them is broken,
**amber** otherwise; hover for the breakdown (`1 broken · 2 out of date`). The
tree refreshes every 3 seconds, and at once after a write, so its badges stay
live; the title-bar badges refresh with each page load.

| Kind | Severity | Raised when | Counted at |
|---|---|---|---|
| broken | red | a studio is `lost` or `terminating`, or its egress re-apply is failing | Agents |
| out of date | amber | a studio runs a stale image or connector ([coves.md](coves.md#the-studio-verbs)) | Agents |
| config | amber | an escalation target names nobody on the project | the project's Escalation node |
| config | amber | a role's kit or model-spec no longer exists | the role's node |
| config | amber | a destination is in a connector conflict ([ui-pages.md](ui-pages.md#destination-pages)) | the destination's node |
| config | amber | a kit's current version does not parse | the kit's node |
| condition | red (critical) · amber (warning) | an open operator [condition](monitoring.md) | Health |

One studio counts once, at its worst (a lost studio on a stale image is one
broken item). On the title bar, **Config** counts the config items (the
tree's), **Agents** the studios' broken and out-of-date items, **Health** the
open conditions, and **Dashboard** everything. A tree node counts the config
items at it or under it, so a project's node sums its roles' and its
Escalation's.

**Needs attention.** The Dashboard lists Jam's own items — open conditions,
kits and destinations — each with why and a link to where to fix it, or says
nothing needs attention.
