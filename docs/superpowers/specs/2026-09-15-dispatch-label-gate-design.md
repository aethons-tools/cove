# Dispatch-label gating for the resident dispatcher

**Date:** 2026-09-15
**Status:** approved (brainstormed with the operator)

## Problem

Harbor's resident dispatcher (`internal/dispatcher`) raises a managed cove for
**every** issue its tracker's `ListReady` returns — i.e. every issue in the
configured `ready` workflow state. Pointing `ready:` at a team's general "Todo"
column therefore dispatches real coves against the whole backlog, with no way to
mark which tickets are actually meant for autonomous work.

## Goal

Dispatch only tickets explicitly **tagged for dispatch**: an issue is
dispatchable iff it carries a label matching a configurable prefix, defaulting to
`dispatch:` (glob `dispatch:*`). Presence-only — the value after the prefix has
no meaning in this change. This is a separate concept from the existing
`class:*` handler-class label.

## Design (Approach A — gate in the dispatcher)

The gate lives in the harbor dispatcher, not in `ListReady`, so the blast radius
is only harbor. The standalone `at-cove dispatch` scheduler shares
`linear.Client` but does not enforce the new signal, so its behaviour is
unchanged.

Data flow:

1. **`kit.LinearTracker`** gains `DispatchLabelPrefix string`
   (`yaml: dispatch-label-prefix`), defaulted to `dispatch:` during config
   parsing — mirroring how `ClassLabelPrefix` defaults to `class:`.
2. **`scheduler.Issue`** gains `DispatchLabeled bool` (presence-only; derived
   from labels the same way `Class` is).
3. **`linear.Client.ListReady`** already loops each issue's labels to parse
   `Class`. In that same loop it sets `DispatchLabeled = true` when a label name
   starts with the prefix.
4. **`dispatcher.tick()`** — the first check in the per-issue loop:
   `if !iss.DispatchLabeled { continue }`. Non-labeled issues are never
   deduped, counted against the cap, claimed, or raised.

## Default behaviour

Gating is **on**: `dispatch-label-prefix` defaults to `dispatch:` when unset
(mirroring `class-label-prefix`'s default of `class:`), so after this change
harbor raises only tickets carrying a `dispatch:*` label. There is no
dispatch-everything switch — an unlabeled backlog is never auto-worked, which is
the whole point. The standalone `at-cove dispatch` scheduler is unaffected: it
never inspects `DispatchLabeled`.

## Scope

- **In scope:** Linear only — the harbor dispatcher is Linear-only today.
- **Out of scope (YAGNI):** GitHub-issues tracker parity (a follow-up if the
  dispatcher ever drives GitHub); per-value/per-class routing (the operator
  chose presence-only); overriding the raised role from the label value.

## Testing (hermetic, TDD)

- **Config parse:** `dispatch-label-prefix` defaults to `dispatch:` when unset;
  an explicit value is honoured.
- **Label parse:** a label matching the prefix ⇒ `DispatchLabeled`; a
  non-matching label ⇒ not. Prefer a pure helper so this needs no HTTP.
- **`dispatcher.tick()`:** with a fake tracker returning mixed labeled/unlabeled
  issues, only labeled ones are raised (subject to dedup + cap).

## Docs

- `docs/usage/harbor/dispatcher.md` — document `dispatch-label-prefix` in the
  `runtime.dispatcher.linear` block and the gating behaviour + default.
- `docs/usage/at-cove-config.md` — note the field next to `class-label-prefix`.
