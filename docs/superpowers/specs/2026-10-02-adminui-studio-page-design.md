# Admin UI studio page — design

Date: 2026-10-02 · Scope: `internal/jam/adminui` (`/ui/coves/{id}`). Builds on #296's session
timeline (`/ui/coves/{id}/session`), which it links rather than duplicates.

## Design (approved in review)

- Header: phase, activity, kind (ephemeral / personal+owner / standing+name), project, role,
  unit (linked); Open live timeline; Teardown when a supervisor is configured.
- Runtime: raised, last seen, lease, backend/location.
- Waiting & escalation: waiting since + wait seq, open escalation (tier, when, chain),
  personal-session nags, inbox commit seq.
- Egress: policy fingerprint (short), consecutive re-apply failures flagged.
- Session: the studio's streams (start, last event, count) linking into the timeline.
- Squawks: newest 50 to/from `actor:<id>`, link to Intercom pre-filtered for the rest.
- A torn-down studio (gone from the registry) still renders its session and squawks under a
  "not running" banner; 404 only when nothing is known about the id.
- Studios table: id → studio page, plus a "timeline" link per row.
- Not in scope: a merged session-events + squawks timeline.
