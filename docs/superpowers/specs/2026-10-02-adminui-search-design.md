# Admin UI search — design

Date: 2026-10-02 · Scope: `internal/jam/adminui` (`/ui/search`). Last item of the admin-UI
redesign ("option C").

## Design (approved in review)

- Top-bar box on every page; `/` focuses it (outside text fields). Enter submits with `go=1`:
  a single entity whose name is the whole query → 303 to its page; otherwise the results page.
- `/ui/search?q=` — case-insensitive substring, ≥ 2 chars; groups (studios, roles, projects,
  kits, destinations, actors, roster humans, roster channels, squawks) with counts, ≤ 20 hits
  shown per group, matches highlighted (escaped, then `<mark>`). Live as you type via htmx
  (results fragment, URL pushed).
- Fields: studios id/unit/owner/standing name/project-role; roles project/name, kit,
  destinations; projects name; kits name + current prompt/egress; destinations name, route,
  upstream, env keys; actors id + grants; humans name/handle/login/delivery/identity subject;
  channels name/ref; squawk bodies (newest 10, rest via Intercom `q=`).
- Not searched: session event streams.
