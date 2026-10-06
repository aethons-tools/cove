# intercom 1a-3d-2: ingress attribution by account

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal:** inbound authors are registry accounts (spec §5, "ingress attribution"), under option B: the log still records `human:<name>`.

## Changes
- `internal/dispatch/linear` comment feed selects `user { id }` → `FeedComment.AuthorID` → `relay.Event.AuthorID` (approved by the user 2026-10-06; additive).
- `directory.recordAuthor(kind, uid, handle, label)`: upsert the author as an account on the connection of kind (created if absent) and return its linked live user.
  - Linear (routed comments only): handle = label = display name (Linear's @-handle), so a member's handle account learns its uid; a linked account's user is the sender, else the display name.
  - Discord: a routed reply's author not attributed by the roster rules (and not a bot) is recorded as an unlinked account; attribution is unchanged.
- Best-effort: a registry failure is logged at debug and attributes nobody.

## Tests
- Linear: member by handle → attributed, account learns uid; renamed on Linear → still attributed by uid; stranger → unlinked account, display name.
- Discord: unknown author recorded; bots never.
