# Manual-first operating model — design

**Status:** first-pass draft 2026-11-05; pilot started 2026-11-06 (decisions below). The user asked for a proposal to react to ("Cunningham's Law"); decisions they made are marked *(user)*, everything else is a proposal.
**Context:** today, work starts as a ticket or a chat and the end experience is discovered at review. The user wants to move faster and ship a better product by making **the user's experience — the manual — the object of every discussion**, with a pipeline of agent roles from idea intake to delivery, and a picture of the product and the work that can be reasoned over at a glance.

## Intent

1. Product and feature discussions revolve around the **user manual**, with screenshots and mockups, so the end experience is the goal of the discussion.
2. New concepts are **captured in seconds** and triaged into a **mind map** that drives (1).
3. An **org chart of agent roles** carries work from intake through delivery.
4. The product, the work to do, and the work in progress are shown as a **visual metaphor** for reasoning over status.

## Goals

- One chain for all work: **idea → map node → goal manual page → tickets → shipped as-is page**.
- The remaining work is *derived* (a diff), not maintained by hand.
- Both states — what the product does now, and what we want it to do — are visible side by side whenever the product is discussed *(user)*.
- Humans decide at one gate per feature: merging its goal-page change.

## Non-goals

- Replacing Linear. Tickets stay in Linear (open again as of 2026-11-05).
- Developer docs. `docs/` stays the developer documentation; the manual is user-facing.
- Tooling first. The chain is piloted by hand before anything is automated.

## Anti-goals

- A manual that drifts from the product. The as-is manual is wrong if it doesn't match `main`; that is a bug like any other.
- A second backlog to groom. If a status has to be updated by hand, the design failed.
- Ceremony that slows a one-line fix. Fixes that don't change user-visible behavior skip the chain entirely.

## 1. Two manuals, one diff

- **As-is manual** *(user)*: `manual/` on `main`, in the repo, changed in the same PR as the code it describes.
- **Goal manual** *(user: "somewhere else, maybe another branch")*: a long-lived `goal` branch carrying the same `manual/` tree, describing the product we're steering toward.
- **Work to do** *(user)* = `git diff main..goal -- manual/`. Each differing page is a feature not yet built.
- **One goal**, not horizons (proposal; the user's "Continue" accepted it provisionally). Ordering — what's next vs. someday — comes from priority on the map, not from separate branches.

### Format and build *(user: AsciiDoc, compiled to a static view)*

The manual is **AsciiDoc**, built with **Antora** (npm; Asciidoctor.js underneath — no Ruby needed, and npm is in egress). Antora builds a site from several git branches at once as *versions*, which matches the two-manual model directly: `main` builds as **as-is**, `goal` builds as **goal**, in one static site with a version switcher. The side-by-side diff view is ours to add on top (a small extension or page that renders both versions of a page).

As built (pilot): Antora's default UI is fetched from gitlab.com at build time, so the manual carries its own small UI in `manual/ui/` (Jam's look, an As is / Goal switcher, a banner on goal pages). How to build and author: [`docs/manual.md`](../../manual.md).

Layout is Antora's: `manual/antora.yml` plus `manual/modules/<area>/pages/*.adoc`, images in `manual/modules/<area>/images/`. Page metadata is header attributes:

```asciidoc
= Editing a project's configuration
:page-area: projects
:page-id: projects/config
```

Prose uses semantic line breaks (one clause per line; the `sembr-format` skill) so the `main..goal` diff reads clause by clause.

### Cross-cutting conventions *(user)*

User-visible choices that span many pages — which control for which kind of input, dialog vs. tray vs. inline edit, how saves and errors behave, confirmation rules, wording — get their own **Conventions** area in the manual. They are part of the end experience, so they live in the manual and change through the same goal-diff flow: changing a convention is a goal-page change whose tickets touch every page that follows it.

Not every convention is for the user. One source, two builds: internal-only passages sit in `ifdef::internal[]` blocks, and the site is built twice — the **user manual** without the attribute, the **team manual** (what we discuss in) with `internal` set. Each page states which conventions it follows (`:page-conventions: dialogs, saving`), so a convention change can list the pages it touches.

Images: on `main` they are **screenshots** taken of the real app; on `goal` they are **mockups** (static HTML rendered to PNG, or a clickable preview linked from the page). When a feature ships, the screenshot replaces the mockup at the same path — the image diff is part of acceptance.

### Flow

1. **Propose:** a PR *into `goal`* changes or adds pages and mockups. The discussion happens on that PR, with the mockups served as a studio preview.
2. **Agree:** the user merges it. This is the human gate.
3. **Plan:** the page's diff becomes Linear tickets, each labeled `page:<page-id>` — **as many page labels as the change touches** *(user)*. Page labels are plain workspace labels, not a Linear label group (a group allows only one of its labels per issue).
4. **Build:** ticket PRs to `main` carry code *and* the as-is manual change (with fresh screenshots).
5. **Ship:** when the page on `main` matches `goal`, it falls out of the diff.
6. **Sync:** after merges to `main`, `main` is merged into `goal` so shipped work and fixes flow up and the diff only shows real gaps. Mechanical; owned by the Editor role.

### Bootstrap cost

The diff only means something once an as-is manual exists. Before the pilot, the as-is manual is written for **one area**: **project configuration** *(user)* — the admin console's Config › Projects pages (list, overview, Members, Agents and the role page, Intercom, Escalation), with screenshots. Source material: `docs/usage/jam/ui-projects.md` and `projects.md` describe them for developers; the manual rewrites that for the person editing a project. Other areas are added as they are first touched.

## 2. Capture and the mind map

- **Capture:** an idea is a Linear issue with label `idea`, in a triage state. Ways in: a message to the Intake session ("idea: …"), or Linear directly (mobile, anywhere). Intake files it immediately with no questions back.
- **Map:** the goal manual's table of contents **is** the skeleton — areas (Conventions among them), then pages. Ideas hang off pages via `page:<id>` labels; an idea touching several pages hangs off each. Unattached ideas sit in an "unsorted" cloud.
- **Triage** (Intake, daily batch): each idea goes to one of
  - an existing page → *proposed change* (stays an idea, labeled with the page),
  - a new page → a stub page proposed on `goal`,
  - parked, or dropped (closed with a reason).
  Duplicates are merged.
- **Priority:** the user ranks pages (not tickets) on the map. Rank lives in Linear (priority on the page's tracking issue) so the map and the board agree.
- **Rendering:** v0 is a Jam whiteboard Intake redraws after triage; the user can rearrange it, and Intake reads moves back as priority changes.

## 3. Org chart

```
                         You (decide, rank, approve goal pages)
                                       │
          ┌───────────────┬────────────┴───────────┬───────────────┐
       Intake           Editor                  Planner          Admin
    (standing)        (standing)              (on demand)      (standing)
   capture/triage   goal pages, sync,        page diff →      roles, kits,
   map upkeep       runs discussions         tickets          pipeline health
                          │                        │
                      Designer                 Builders ── Reviewer ── Verifier
                    (on demand)            (ticket sessions) (on demand) (on demand)
                  mockups, prototypes        ticket → PR     code + "matches     screenshots,
                                                              the page?"         as-is page done
```

| Role | Jam shape | Inputs → outputs | Human touchpoint |
|---|---|---|---|
| Intake | standing session | messages, Linear `idea`s → triaged ideas, map board, daily digest | digest |
| Editor | standing session | ranked pages → goal-page PRs; runs `main`→`goal` sync | reviews PRs with the user |
| Designer | raised by Editor | page draft → mockups, clickable preview | via the PR |
| Planner | raised by Editor after merge to `goal` | page diff → `page:`-labeled Linear tickets (board-plan) | none |
| Builder | ticket session (Requisitioner) | ticket → PR to `main` with as-is page change (board-execute) | merge (as today) |
| Reviewer | raised per PR | PR + goal page → findings | none |
| Verifier | raised per PR | built app → screenshots; confirms as-is page matches goal | none |
| Admin | standing (admin-spider) | role/kit/egress requests, stuck work | escalations |

Only three sessions stand; the rest exist only while they have work.

## 4. The visual: the book filling in

One view of the manual's table of contents, each page a tile:

| Tile | Meaning | Derived from |
|---|---|---|
| full color | shipped — page on `main` equals `goal` | no diff |
| striped | building | diff + open `page:` tickets |
| sketch | agreed, waiting | diff, no open tickets |
| grey | idea only | `idea`s labeled with a page that has no goal page yet |

Agent avatars sit on the page they're working on. Clicking a tile opens the page **as-is | goal** side by side, changed passages highlighted, screenshot next to mockup. A thin strip underneath counts pages per stage and flags anything stuck (a striped page with no PR activity for N days).

- v0: the Editor redraws a Jam whiteboard daily.
- v1: a page in the Jam app (user area) that renders both manuals from git and the tickets from Linear.

## Pilot (by hand, before any tooling)

1. Write the as-is manual for one area.
2. Pick one upcoming feature in that area; draft its goal page and mockup on `goal`; discuss and merge.
3. Plan → build → verify through the normal ticket flow, with the as-is page updated in the PR.
4. Retro: what was friction, what should be automated first.

Then stand up Intake and Editor as roles, then the v1 view.

### Pilot decisions *(user, 2026-11-05/06)*

- **Change piloted:** the project page's *Agent context* card goes from one YAML box to fields and grids. The card is shared (`web/app/src/admin/ui/ContextPanel.tsx`) by the project page, each role's page and the Jam-wide dashboard, so the change covers all three.
- **Renames on screen:** *Core* → **Core instructions**, *Leaves* → **Progressive instructions**. The `jam context` YAML and admin API keep `core`/`leaves`.
- **Conventions settled** (they go in the goal manual's Conventions area):
  1. Labels, headings and buttons use **sentence case**.
  2. Short lists are a **quiet grid**: it looks like a plain table, and a cell becomes a field only while you edit it.
  3. Long items (a progressive instruction) are **collapsible rows**.
  4. Items are added from a **“+ new …” row at the bottom** of the list; there is no add button at the top.
  5. Every length-limited field has a **thermometer** below it; a limit shared by a whole list sits below the list.
  6. **One layout for viewing and editing:** read-only by default, *Edit* switches the whole card to editing, then *Cancel* or *Save*. One save per card.
  7. **Errors appear at the field**, and Save stays disabled until there are none.
  8. **Markdown shows rendered** when viewing and as source when editing (core instructions and bodies).
- **Mockups** are clickable HTML prototypes, served as a studio preview while discussed and kept with the goal page as an Antora attachment.

## Left to the builder

- How mockups are produced (hand HTML vs. generated) and the screenshot tooling (Chrome is in the kit).
- Exact label names and the Linear state used for triage.
- Whiteboard layout.

## Open

- Whether merges to `main` also need the user, or only goal-page merges.
