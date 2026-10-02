# Admin UI kits page + nameless kits — design

Date: 2026-10-02 · Scope: `internal/studio` (schema), `internal/jam` (push path), `cmd/at-jam`
(`kit push`/`show`), `internal/jam/adminui` (`/ui/kits`).

## Problems

- The UI's kit push stored the pasted text raw — no StudioKit parse/validation, no canonical
  JSON — so a typo'd kit was accepted and failed later at raise.
- A kit carried `name:` in its YAML as well as the registry key it's pushed under; nothing kept
  them equal, and raises key on the registry name.

## Decisions (review)

- **Drop `name` from the StudioKit schema.** The registry key is the name (must be tag-safe).
  The in-file `name:` becomes `LegacyName`: accepted on input (existing files and stored rows
  still parse), never stored, and must equal the pushed name (`CheckName`).
- **An unchanged push makes no new version** (`jam.PushStudioKit`, idempotent against
  current; `KitResult.Unchanged`; `kit push` prints `web unchanged (current vN)`).

## Design

- `jam.PushStudioKit(store, name, config)` is the one push path (API + UI): parse, `CheckName`,
  validate, canonical JSON, compare with current under a lock.
- List: name, current `vN of M`, base kind, egress count, roles using it (default counts
  roles with no kit), invalid marker. New-kit form: name + YAML, create-only.
- Kit page: version rail (short build digest, Pin, diff vs previous / vs current); version
  view (base incl. packed-context entry list from tar headers, egress + ceiling/excluded,
  build args, secret demands, prompt, digest, YAML with the packed context abbreviated, Copy);
  `?v=N&diff=M` line diff (LCS, collapsed context) + whether the build digest changed;
  push-new-version pre-filled with the viewed YAML; used-by; Delete disabled while used and
  for `default`. Legacy unparseable rows show the parse error and raw text.
