# harbor: msgport Slice 1b — ingress-shadow (Linear comments feed → the Log) (COV-174)

**Status:** design approved (brainstorm + fork resolved), pre-plan
**Issue:** COV-174. **Foundation:** COV-173 (1a dual-write — the Log + writer), COV-172 (msgport spine), COV-171 (msglog).

## Summary

Stand up the msgport **ingress** engine for Linear: a resident loop polls Linear's filtered top-level `comments` feed and appends inbound human replies to the Log, idempotently, so Slice 2 (wake-on onto the Log) can consume them. **Egress stays off** (the 1a dual-write is still the live outbound path — running egress would double-post). **No live-behavior change:** wake-on still polls Linear directly until Slice 2; this only *populates* the Log with inbound.

## 1. Linear comments feed (`internal/dispatch/linear`)

Two new `*Client` methods (tested via the existing injected-`RoundTripper` pattern; the const endpoint is intercepted at the transport):

```go
// FeedComment is one comment from the team-scoped feed.
type FeedComment struct {
	ID              string
	Body            string
	CreatedAt       time.Time
	Author          string // user.displayName
	IssueIdentifier string // issue.identifier, e.g. "ACME-42"
	ParentID        string // parent comment id, "" if top-level
}

// CommentFeed returns comments in the Client's team created after `since`,
// oldest-first, capped at `limit`. Used by the msgport linear ingress adapter.
func (c *Client) CommentFeed(ctx context.Context, since time.Time, limit int) ([]FeedComment, error)
// Query:
//   query($key:String!,$since:DateTimeOrDuration!,$first:Int!){
//     comments(filter:{issue:{team:{key:{eq:$key}}}, createdAt:{gt:$since}},
//              orderBy: createdAt, first:$first){
//       nodes{ id body createdAt user{displayName} issue{identifier} parent{id} }
//     }
//   }
// (Confirm the exact CommentFilter path — issue→team→key mirrors the existing
//  issues() filters in this file — with schema introspection if the query errors.)

// Viewer returns the display name of the identity the Client's token
// authenticates as (harbor's own Linear user), for the ingress self-post filter.
func (c *Client) Viewer(ctx context.Context) (string, error) // query{viewer{displayName}}
```

- The existing count-based `Comments(issueID)` is **untouched** (wake-on keeps using it until Slice 2).
- `since` is passed as a Linear `DateTime` string; the caller (the surface) formats it. `limit` bounds a tick (e.g. 100); the moving cursor + id-dedup make a partial page over the next ticks safe (over-report never under-reports).

## 2. `msgport.Config.EgressEnabled` (`internal/msgport`)

Add `EgressEnabled bool` to `Config`. `Run` starts the egress loop **only when true**; the ingress loop always runs:

```go
func (e *Engine) Run(ctx context.Context) {
	if e.cfg.EgressEnabled {
		go e.loop(ctx, e.cfg.EgressPoll, e.egressTick)
	}
	e.loop(ctx, e.cfg.IngressPoll, e.ingressTick) // inline
}
```
Default false. Slice 3 (egress cutover) flips it true. This is the only spine change.

## 3. Concrete `linearSurface` (`cmd/at-harbor`)

`msgport.Surface` over `*linear.Client` (via a narrow feed interface for testability):

```go
type commentFeeder interface {
	CommentFeed(ctx context.Context, since time.Time, limit int) ([]linear.FeedComment, error)
}
type linearSurface struct {
	feed    commentFeeder
	started time.Time // baseline for an empty cursor (shadow forward, not all history)
}
func (s *linearSurface) Service() string { return "linear" }
func (s *linearSurface) Poll(ctx context.Context, project, since string) (events []msgport.Event, next string, err error) {
	from := s.started
	if since != "" { from, _ = time.Parse(time.RFC3339Nano, since) }
	cs, err := s.feed.CommentFeed(ctx, from, 100)
	// map each FeedComment → msgport.Event{ForeignID:c.ID, Surface:c.IssueIdentifier,
	//   Author:c.Author, Body:c.Body, ReplyToForeign:c.ParentID, At:c.CreatedAt}
	// next = the max CreatedAt seen (RFC3339Nano), else `since` unchanged.
}
func (s *linearSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	return "", fmt.Errorf("linear egress not enabled (Slice 3)") // never called: EgressEnabled=false
}
func (s *linearSurface) Close() error { return nil }
```

## 4. Concrete `Directory` (`cmd/at-harbor`)

`msgport.Directory` mapping the Linear feed into the actor model:

```go
type directory struct {
	store        harbor.Store // ListInstances + GetRoster
	project      string
	selfIdentity string // harbor's Linear viewer displayName (self-post filter)
}
func (d *directory) Projects(service string) []string { return []string{d.project} }
func (d *directory) Route(service, project string, e msgport.Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool) {
	if e.Author == d.selfIdentity { return msglog.Target{}, nil, "", false } // self-post: a cove's brokered outbound
	from = msglog.Target{Kind: "human", Ref: e.Author}
	// issue.identifier → a live cove's own ticket → actor; else a configured channel → channel; else unrouted.
	for _, inst := range d.store.ListInstances() {
		if inst.Unit == e.Surface { to = []msglog.Target{{Kind: "actor", Ref: inst.ActorID}}; break }
	}
	if to == nil {
		if r, ok2 := d.store.GetRoster(project); ok2 {
			for _, ch := range r.Channels {
				if ch.Ref == e.Surface { to = []msglog.Target{{Kind: "channel", Ref: ch.Name}}; break }
			}
		}
	}
	if to == nil { return msglog.Target{}, nil, "", false } // unrouted → skip (cursor advances)
	if e.ReplyToForeign != "" { replyTo = "in:" + service + ":" + e.ReplyToForeign }
	return from, to, replyTo, true
}
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	return msgport.Delivery{}, false // egress stub (Slice 3)
}
```

## 5. Persistence + wiring (`cmd/at-harbor`)

- **`Cursors`** — a small file-backed store (JSON `map["service/project"]→cursor` under the store dir, mutex-guarded, load-at-open, save-on-set). `Markers` — a trivial no-op (unused; egress off).
- In `cmdServe`'s tracker-gated dispatcher block, when `messageLog != nil`: query `self, _ := tracker.Viewer(ctx)` **once**; construct `linearSurface{feed: tracker, started: time.Now()}`, `directory{st, dc.Project, self}`, the file `Cursors`, the no-op `Markers`; `eng := msgport.New(surface, messageLog, markers, cursors, dir, msgport.Config{EgressEnabled: false}, log)`; `go eng.Run(context.Background())`. Log a "harbor msgport (linear ingress): resident, egress off" line. (Default `IngressPoll` — no new config.)

## Tests (hermetic)

- **linear:** `CommentFeed` sends the team-key + since + first variables and parses `id/body/createdAt/user.displayName/issue.identifier/parent.id` (canned RoundTripper response); `Viewer` parses `viewer.displayName`.
- **linearSurface:** a fake `commentFeeder` → `Poll` maps comments → Events (all fields); empty `since` uses `started`; `next` = max CreatedAt; a set `since` parses; `Deliver` returns the not-enabled error.
- **directory:** `Route` — self-post (author == selfIdentity) → ok=false; issue→cove (Instance.Unit match) → `to=actor:<coveID>`, From=`human:<author>`; issue→channel (Channel.Ref match) → `to=channel:<name>`; unknown issue → ok=false; ReplyTo mapped from parent.
- **msgport:** `Run` with `EgressEnabled:false` does not run egress (an outbound message in the Log is never `Deliver`ed) while ingress still appends (extend the spine tests).
- **Cursors:** round-trip + persistence reload.
- (cmd) keep any main/mux test green with the new wiring.

## Deferred

Slice 2 (wake-on onto the Log — consumes this ingress); Slice 3 (egress cutover: `EgressEnabled:true` + real `linearSurface.Deliver`/`directory.Resolve` + golden-output parity + `handlePost` Append-only + file-backed Markers); Slice 4 (GET + escalation); Slice 5 (Discord). Roster-matching the inbound author ref; `first`-page pagination beyond the cap; per-project multi-team.

## Boundaries

`internal/msgport` gains only `Config.EgressEnabled` (still msglog+stdlib only). The concrete `linearSurface`/`directory`/`Cursors`/`Markers` live at `cmd/at-harbor` (like `linearCommenter`); they may use `harbor.Store` + `internal/dispatch/linear`. `internal/dispatch/linear` gains the two query methods (no other consumer changes). No secret/token ever enters an `Event`, the Log, a cursor, or a log line; only structural warns (unrouted event, poll error) — never a comment body.
