# intercom 1a-3d-2: ingress attribution by account

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal:** inbound authors are registry accounts (spec §5, "ingress attribution"), under option B: the log still records `human:<name>`.

## Changes
- `internal/dispatch/linear` comment feed selects `user { id }` → `FeedComment.AuthorID` → `relay.Event.AuthorID` (approved by the user 2026-10-06; additive).
- `directory.recordAuthor(kind, project, uid, label)`: record the author as an account on the connection of kind (created if absent) **by uid only** (label = display name; never matched by it, since anyone can set a display name) and return its linked live user when a member of the project. No write when nothing changed.
  - Linear (routed comments only): an operator-linked account's user is the sender, else the display name.
  - Discord: a routed reply's author not attributed by the roster rules (and not a bot) is recorded as an unlinked account; attribution is unchanged.
- Best-effort: a registry failure is logged at debug and attributes nobody.

## Tests
- Linear: a stranger using a member's handle as display name stays a stranger (no handle takeover); a linked account → attributed, across Linear renames; a linked non-member → display name.
- Discord: unknown author recorded; bots never.
