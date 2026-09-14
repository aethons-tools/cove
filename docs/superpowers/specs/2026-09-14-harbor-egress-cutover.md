# harbor: msgport Slice 3 — egress cutover (COV-176)

**Status:** design approved (brainstorm + clean-switch fork), pre-plan
**Issue:** COV-176. **Foundation:** COV-174 (1b ingress — feeds the Log), COV-173 (1a dual-write — first writer), COV-172 (spine — egress engine + EgressMark), COV-171 (msglog).

## Summary

The cutover: the `/messages` **POST** send path stops calling Linear `PostComment` directly and instead **appends the logical message to the Log**; a now-live egress engine reads the Log and delivers to Linear, reproducing today's rendering **byte-for-byte**. **Clean switch** — `handlePost` is append-only unconditionally, `EgressEnabled: true`, revert = redeploy the prior binary.

Scope is **only the POST send path**. Escalation pings and the GET/read path still post directly (Slice 4).

## 1. `msgport.Delivery.BodyPrefix` (`internal/msgport`)

The generic `Delivery` is opaque to the engine (only `.Service` is inspected in `owned()`) — `Resolve → Deliver` is a private channel. Add one field so `Resolve` fully determines the rendered form and `Deliver` is a dumb pipe:

```go
type Delivery struct {
	Service      string
	Address      string
	BodyPrefix   string // literal string prepended to m.Body at delivery ("" = none)
	SenderName   string // (unchanged; unused by the Linear adapter — would break byte-parity)
	SenderAvatar string
}
```

A string field adds no import — msgport stays **msglog + stdlib only**. This is the only change to the generic package.

## 2. Concrete `directory.Resolve` (`cmd/at-harbor`)

Replace the stub. **Pure** — roster/instance lookups only, no network. `Address` is the ticket **identifier** (e.g. `ACME-42`); the surface resolves it to an internal id at delivery.

```go
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	if service != "linear" {
		return msgport.Delivery{}, false
	}
	// The sender's own cove instance (for own-ticket / human-mention delivery).
	var self harbor.Instance
	var haveSelf bool
	if from.Kind == "actor" {
		for _, inst := range d.store.ListInstances() {
			if inst.ActorID == from.Ref {
				self, haveSelf = inst, true
				break
			}
		}
	}
	switch to.Kind {
	case "human":
		// Deliver on the SENDER's own ticket, @-mentioning the recipient (parity with C1).
		if !haveSelf {
			return msgport.Delivery{}, false
		}
		r, ok := d.store.GetRoster(project)
		if !ok {
			return msgport.Delivery{}, false
		}
		for _, h := range r.Humans {
			if h.Name == to.Ref {
				return msgport.Delivery{Service: "linear", Address: self.Unit, BodyPrefix: "@" + h.Handle + " "}, true
			}
		}
		return msgport.Delivery{}, false
	case "channel":
		// Own-ticket channel (Ref == the sender's Unit) → own ticket, raw body.
		if haveSelf && to.Ref == self.Unit {
			return msgport.Delivery{Service: "linear", Address: self.Unit}, true
		}
		// Roster channel by Name → its own thread (Channel.Ref), raw body.
		if r, ok := d.store.GetRoster(project); ok {
			for _, ch := range r.Channels {
				if ch.Name == to.Ref {
					return msgport.Delivery{Service: "linear", Address: ch.Ref}, true
				}
			}
		}
		return msgport.Delivery{}, false
	default: // actor → internal, in-band, never egressed
		return msgport.Delivery{}, false
	}
}
```

- Uses the **passed `project`** (`m.Project`) for `GetRoster`, mirroring `Route`.
- The own-ticket target is `channel:<inst.Unit>` (as 1a's shadow-write records it); it resolves to the sender's own ticket with **no prefix** — matching handlePost's own-ticket post.

## 3. Concrete `linearSurface.Deliver` (`cmd/at-harbor`)

Replace the stub. Resolves the identifier and posts `BodyPrefix + m.Body`:

```go
// commentPoster is the narrow slice of *linear.Client that linearSurface
// delivers through (identifier→id resolution + comment post).
type commentPoster interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}

type linearSurface struct {
	feed    commentFeeder
	poster  commentPoster
	started time.Time
}

func (s *linearSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	issueID, err := s.poster.IssueByIdentifier(ctx, d.Address)
	if err != nil {
		return "", fmt.Errorf("linear deliver: resolve %q: %w", d.Address, err)
	}
	if err := s.poster.PostComment(ctx, issueID, d.BodyPrefix+m.Body); err != nil {
		return "", fmt.Errorf("linear deliver: post to %q: %w", d.Address, err)
	}
	return "", nil // Linear PostComment returns no id; exactly-once is the EgressMark's job
}
```

- **No idempotency footer** — it would break byte-parity, and `EgressMark` (mark-after-deliver) already gives exactly-once on the normal path. A crash between a successful `PostComment` and the mark persisting could double-post (rare; accepted at-least-once edge, the same tradeoff the spine documents).
- On any error `Deliver` returns it → the engine leaves the target unmarked → retried next tick (at-least-once).

## 4. `handlePost` → append-only (`internal/harbor/messages.go`)

The Log becomes the authoritative delivery path.

- **Keep:** body parse, empty-check (400), oversize (413), and **`DecideSend` authz** (403 `ErrSendDenied` / 404 `ErrSendUnresolved`) → compute `logicalTo`.
- **Remove:** the `@handle` body rendering, the channel `IssueByIdentifier` resolution, and the `PostComment` call. (`deliverIssue`/`body` locals and the `issueID` delivery use go away; `issueID` is still resolved in `ServeHTTP` for the GET path.)
- **Append is authoritative:**
  - `h.lg == nil` → **503** `"messaging not configured"` (sends require the Log under the clean switch).
  - `Append` error → **502** `"send failed"` (no longer swallowed — the append IS the delivery).
  - success → **204**.
- The appended message is unchanged from 1a: `From: actor:<id>`, `To: [logicalTo]`, `Body: req.Body` (raw), `Project: inst.Project`.
- `Commenter` (`h.cmt`) is still used by `handleGet`; only `handlePost` stops using `PostComment`/`IssueByIdentifier`.

Self-scope is preserved by construction: `logicalTo` derives from the actor's own `Instance` + `DecideSend`, never a client-supplied ticket; egress `Resolve` delivers on the sender's own ticket (own/human) or an authz'd channel.

## 5. `fileMarkers` + startup seed (`cmd/at-harbor`)

A file-backed `msgport.Markers` (sibling of `fileCursors`): JSON `map[service]EgressMark`, mutex-guarded, load-at-open, save-on-`SetEgress`. `EgressMark`'s nested maps round-trip through `encoding/json`.

```go
type fileMarkers struct {
	path string
	mu   sync.Mutex
	m    map[string]msgport.EgressMark
}
func newFileMarkers(path string) (*fileMarkers, error) // tolerate missing/torn → empty
func (m *fileMarkers) Egress(service string) msgport.EgressMark          // zero when unset
func (m *fileMarkers) SetEgress(service string, mk msgport.EgressMark) error // persist
func (m *fileMarkers) has(service string) bool                            // seed guard
```

**Seed once** at wiring time (guards against re-delivering 1a's shadow history):

```go
if !markers.has("linear") {
	if err := markers.SetEgress("linear", msgport.EgressMark{LastMsg: logTailID(messageLog)}); err != nil { ... }
}
```

- `logTailID(*msglog.Log)` = the ID of the last message in `List(Filter{})` (time-sorted), `""` for an empty Log.
- Persisted → subsequent restarts see the `"linear"` entry and **do not re-seed** (a re-seed to a newer tail would skip undelivered messages appended since the first cutover).
- Empty Log (fresh harbor, never ran 1a) → empty seed → egress delivers from the start (nothing to skip). Correct.

## 6. cmd wiring (`cmd/at-harbor/main.go`, the tracker-gated block)

In the `if messageLog != nil` block that currently builds the ingress engine:

- `surf := &linearSurface{feed: tracker, poster: tracker, started: time.Now()}` (the same `*linear.Client` backs both).
- `markers, err := newFileMarkers(filepath.Join(filepath.Dir(cfg.Store), "msgport-markers.json"))`.
- Seed as in §5.
- `eng := msgport.New(surf, messageLog, markers, cur, dir, msgport.Config{EgressEnabled: true}, log)` → runs **both** loops.
- Log line: `"harbor msgport (linear): resident, egress ON"`.

`noopMarkers` is deleted (no longer referenced).

## 7. Byte-parity gate — hermetic golden test (`cmd/at-harbor`)

The parity anchor **moves** from the `handlePost` tests to a new egress golden test. A table of logical messages driven through `directory.Resolve` + `linearSurface.Deliver` against a fake `commentPoster` (records `(issueID, body)`), asserting the exact triples today's `handlePost` tests assert:

| target | Resolve → Address | Deliver posts | expected `(issueID, body)` |
| --- | --- | --- | --- |
| own ticket (`channel:ACME-7`) | `ACME-7` | raw | `(iss_7, "hi")` |
| `human:alice` | sender Unit `ACME-7` | `@alice.h ` + raw | `(iss_7, "@alice.h ping")` |
| `channel:eng-help` (roster Ref `ACME-9`) | `ACME-9` | raw | `(iss_9, "heads up")` |

Fake `commentPoster.IssueByIdentifier` maps `ACME-7→iss_7`, `ACME-9→iss_9`. Fake store carries an Instance (`ActorID`, `Unit: ACME-7`) + a roster (`Human{Name: alice, Handle: alice.h}`, `Channel{Name: eng-help, Ref: ACME-9}`).

Also assert: an **unresolvable** target (`human:` not in roster, unknown `channel:`) → `Resolve` `ok=false` → `owned()` excludes it → no `Deliver`; a **non-linear** `Delivery.Service` is skipped by `owned()` (already covered in spine tests, but keep a Resolve `service!="linear"` case).

## 8. Rewrite the `handlePost` tests (`internal/harbor/messages_test.go`)

`handlePost` no longer posts, so the three delivery-rendering tests change to assert the **Append** and the **absence** of any `PostComment`:

- `TestMessagesPostIsSelfScoped`: 204; **no** `PostComment`; exactly one `Append` with `From: actor:<self>`, `To: [channel:<Unit>]`, `Body: "hi"` (raw), `Project` set.
- `TestSendToHumanMentionsOnOwnTicket`: 204; one `Append` `To: [human:alice]`, `Body: "ping"` (raw — **no** `@` prefix in the Log); body never logged.
- `TestSendToChannelPostsOnChannelThread`: 204; one `Append` `To: [channel:eng-help]`, `Body: "heads up"`.
- New `TestMessagesPostAppendFailureIs502`: an `appender` that errors → 502, no 204.
- New `TestMessagesPostNilLogIs503`: `NewMessagesHandler(..., nil, ...)` → POST 503.
- Keep unchanged: 401 (missing/unknown token), 403 (no instance), 413 (oversize), 400 (empty), 405 (method), never-logs-token, authz 403/404 (`DecideSend` denied/unresolved still reject **before** Append).

The fake `Commenter` keeps `PostComment` (still used by GET) but `handlePost` must not call it — assert `len(posted) == 0` on the POST paths.

## 9. Docs (`docs/usage/harbor/messaging.md`)

- The send path is now **Log → egress adapter** (asynchronous, at-least-once): a `send` appends to the message-log and returns 204; delivery to Linear happens on the egress loop (≈`EgressPoll`). A message-log is **required** for sends (no Log → 503).
- **Operator verification (required before relying on ingress):** confirm the Linear `comments` feed — the `$since` scalar type (`DateTimeOrDuration` vs `DateTime`) and the `issue→team→key` filter path — against the **live** Linear GraphQL schema. The code targets the schema introspected during 1b; if it differs, `Poll` errors and the ingress cursor **holds** (no data loss, ingress stalls) — **egress is unaffected**. This cannot be exercised in the egress-locked build sandbox.
- Bump `updated`.

## Tests (hermetic)

- **msgport:** `Delivery` gains a field — extend an egress test to assert `BodyPrefix` reaches `Deliver` (fake surface records it), confirming the engine passes `Delivery` through opaquely.
- **cmd:** the §7 golden parity test; `directory.Resolve` unit cases (human/own/roster-channel/unresolvable/non-linear); `linearSurface.Deliver` (identifier→id→post, prefix applied, resolve-error and post-error propagate); `fileMarkers` round-trip + `has()` + reload persistence; `logTailID` (empty Log → "", non-empty → last id); a seed test (empty markers + non-empty Log → mark seeded to tail; existing entry → not overwritten).
- **Replace/remove existing stub tests** in `cmd/at-harbor/msgport_linear_test.go`: the Deliver-returns-"not enabled"-error assertion and the `noopMarkers` test both go (the code they cover is deleted); the surviving `linearSurface` Poll test keeps working (add a `poster` field, nil is fine when Deliver isn't exercised).
- **harbor:** the §8 rewrites.

## Deferred / boundaries

- **Deferred:** the now-dead `Instance.WaitCursor`/`Supervisor.SetWaitCursor` (from Slice 2) — still a separate cleanup slice, untouched here. Slice 4 = GET + escalation onto the Log. Slice 5 = Discord (2nd Engine + webhook per-sender identity; `SenderName`/`SenderAvatar` become live there).
- **Boundaries:** `internal/msgport` stays msglog + stdlib only (a struct field). The concrete `linearSurface`/`directory`/`fileMarkers` live at `cmd/at-harbor` and may use `harbor.Store` + `internal/dispatch/linear`. `internal/harbor` core unchanged in its import set. at-cove / connect / oidc untouched. No secret/token/body ever enters a `Delivery`, the Log, a marker, a cursor, or a log line — only structural warns (deliver failure: service/msg-id/target/error).
