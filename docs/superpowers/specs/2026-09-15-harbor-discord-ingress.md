# harbor: Slice 5c — Discord ingress + reply-routing (COV-183)

**Status:** design approved (reply-to-message-id + receipts mechanism; human-inbox loop scope), pre-plan
**Issue:** COV-183. **Foundation:** COV-182 (5b Discord egress — the discord engine, `discordSurface`, service-aware `Resolve`), COV-181 (5a — `Human.DeliveryFor("discord")`, `Project.ChatService`), COV-175 (wake-on reads the Log inbox — the reply wakes the cove for free). **Reuses/extends:** `internal/switchboard.RESTClient`.

## Summary

Close the Discord loop: a human's **reply** to a cove's Discord message lands in the Log addressed to that cove, waking it. Uses **reply-to-message-id + a receipt store**: egress records `discord-msg-id → cove`; an inbound message that replies-to one of those ids routes to that cove. Scope = the **human-inbox reply loop** (a discord roster-*channel* ingress path is deferred).

## Reply-routing (from brainstorm)

One inbox channel receives `"cove-A: …"` / `"cove-B: …"`; only a Discord **reply** to a specific cove message carries the disambiguating `message_reference`. **The "must reply to a known cove message to route" rule doubles as the self-post filter** — harbor's own `"cove-X: …"` posts are new (no reference) → dropped → no bot-identity lookup, no echo.

## 1. `internal/switchboard` — minimal additive extension

- `Message` gains `ReferencedID string` (the id of the message this one replies to; "" = not a reply).
- The poll decoder struct gains:
  ```go
  MessageReference struct {
  	MessageID string `json:"message_id"`
  } `json:"message_reference"`
  ```
  and `Poll` sets `ReferencedID: m.MessageReference.MessageID` on each mapped `Message`.
- `RESTClient` gains `PostID(ctx, channel, content string) (string, error)` — posts and returns the created message's `id` (Discord's create-message returns the message object). Existing `Post` delegates: `_, err := c.PostID(...)` (signature unchanged → switchboard's `Discord` interface + loop untouched).
- Tests: `PostID` parses the returned id (canned transport); `Poll` decodes `message_reference.message_id` into `ReferencedID`.

## 2. Receipt store (`cmd/at-harbor`, file-backed like `fileCursors`)

```go
type fileReceipts struct { path string; mu sync.Mutex; m map[string]string } // discord-msg-id → actorID
func newFileReceipts(path string) (*fileReceipts, error)          // tolerate missing/torn → empty
func (r *fileReceipts) Record(discordMsgID, actorID string) error // set + persist
func (r *fileReceipts) Lookup(discordMsgID string) (string, bool)
```
- Values are immutable strings → **no nested-map aliasing hazard** (unlike 5b's `EgressMark`). Mutex-guarded (written by the egress goroutine's `Deliver`, read by the ingress goroutine's `Route` — concurrent within the one discord engine).
- **Growth is unbounded for v1**; pruning is deferred (note in docs).

## 3. `discordSurface` — real ingress + receipt-writing egress (`cmd/at-harbor`, replaces the 5b shape)

```go
// discordClient is the narrow slice of *switchboard.RESTClient the surface uses;
// a fake in tests, the real client (over an injected transport) in wiring.
type discordClient interface {
	PostID(ctx context.Context, channel, content string) (string, error)
	Poll(ctx context.Context, cursors map[string]string) ([]switchboard.Message, map[string]string, error)
}

type discordSurface struct {
	dial        func(channels []string) discordClient // real: wraps switchboard.NewRESTClient(token, channels, opts…)
	channelsFor func(project string) []string          // roster-derived discord inbox channels
	receipts    *fileReceipts
}

func (s *discordSurface) Service() string { return "discord" }

// Deliver posts to the human's inbox channel and RECORDS a receipt so a reply
// can be routed back. A receipt-write failure is swallowed (warn): the post
// already happened, so returning an error would make the engine retry and
// DOUBLE-POST. Returns the discord message id as the seam's foreignID.
func (s *discordSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	id, err := s.dial(nil).PostID(ctx, d.Address, d.BodyPrefix+m.Body)
	if err != nil {
		return "", fmt.Errorf("discord deliver: post to %q: %w", d.Address, err)
	}
	if err := s.receipts.Record(id, m.From.Ref); err != nil {
		// warn only (no body/token); a lost receipt only means a future reply
		// to THIS message won't route — never a double-post.
	}
	return id, nil
}

// Poll fetches new messages across the project's discord inbox channels and maps
// each to an Event (ReplyToForeign = the replied-to message id). The opaque
// `since` is a JSON per-channel cursor map.
func (s *discordSurface) Poll(ctx context.Context, project, since string) ([]msgport.Event, string, error) {
	channels := s.channelsFor(project)
	if len(channels) == 0 {
		return nil, since, nil
	}
	cursors := decodeCursors(since) // "" → empty map
	msgs, next, err := s.dial(channels).Poll(ctx, cursors)
	if err != nil {
		return nil, since, err
	}
	events := make([]msgport.Event, 0, len(msgs))
	for _, m := range msgs {
		events = append(events, msgport.Event{
			ForeignID: m.ID, Surface: m.Channel, Author: m.Author, Body: m.Content,
			ReplyToForeign: m.ReferencedID, // "" if not a reply
		})
	}
	return events, encodeCursors(next), nil
}

func (s *discordSurface) Close() error { return nil }
```
- `decodeCursors`/`encodeCursors`: JSON `map[string]string` ↔ opaque string ("" ↔ empty map; tolerate a torn cursor → empty).
- **Deliver preserves 5b behavior** (posts `BodyPrefix+m.Body` to `d.Address`) plus the receipt.
- The msgport spine already dedups inbound by the deterministic `in:discord:<ForeignID>` id + seen-set, so re-polling is idempotent; the per-channel cursor bounds re-polling.

## 4. `directory.Route` becomes service-aware (`cmd/at-harbor`)

```go
func (d *directory) Route(service, project string, e msgport.Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool) {
	if service == "discord" {
		return d.routeDiscord(e)
	}
	return d.routeLinear(project, e) // the existing linear logic, extracted verbatim
}

// routeDiscord maps a human's Discord reply to the cove it replies to, via the
// receipt store. A message that is NOT a reply, or replies to an unknown id
// (not a receipt), is unroutable and dropped — which also drops harbor's own
// non-reply posts (the self-post filter).
func (d *directory) routeDiscord(e msgport.Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool) {
	if e.ReplyToForeign == "" {
		return msglog.Target{}, nil, "", false
	}
	actorID, ok := d.receipts.Lookup(e.ReplyToForeign)
	if !ok {
		return msglog.Target{}, nil, "", false
	}
	return msglog.Target{Kind: "human", Ref: e.Author},
		[]msglog.Target{{Kind: "actor", Ref: actorID}},
		"in:discord:" + e.ReplyToForeign, true
}
```
- `directory` gains a `receipts *fileReceipts` field (nil when discord unconfigured — `routeLinear` never touches it, and only the discord engine calls `Route` with `service=="discord"`).
- `routeLinear` = today's `Route` body, moved unchanged (self-post filter via `selfIdentity`, issue→cove, linear-channel→Ref). Linear ingress behavior is byte-identical.

## 5. Wiring (`cmd/at-harbor/main.go`, the 5b discord block)

Replace the 5b `dsurf := &discordSurface{poster: …}` construction:
```go
			receipts, err := newFileReceipts(filepath.Join(filepath.Dir(cfg.Store), "discord-receipts.json"))
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: discord receipts:", err)
				return 1
			}
			token := tokEnv["AT_DISCORD_BOT_TOKEN"]
			dir.receipts = receipts // the shared directory routes discord replies via receipts
			dsurf := &discordSurface{
				dial:        func(channels []string) discordClient { return switchboard.NewRESTClient(token, channels) },
				channelsFor: func(project string) []string { return discordInboxChannels(st, project) },
				receipts:    receipts,
			}
			// (seed + msgport.New(dsurf, …, EgressEnabled:true) + go deng.Run — unchanged from 5b)
```
- `discordInboxChannels(store, project) []string`: the distinct non-empty `h.DeliveryFor("discord").Address` across `GetRoster(project).Humans`.
- The discord engine's **ingress loop was already running** in 5b (with the stub `Poll`); this makes `Poll` real. No wake-on / engine change: an external inbound to `actor:<cove>` in the Log wakes the cove via the Slice-2 wake-on loop.
- The 5b `discordPoster` interface + `fakeDiscordPoster` test go away (replaced by `discordClient` + a fake).

## Tests (hermetic)

- **switchboard:** `PostID` returns the created id (canned transport JSON `{"id":"D1"}`); `Poll` decodes `message_reference.message_id` into `Message.ReferencedID`.
- **fileReceipts:** Record + Lookup + reload persistence; missing/torn file → empty.
- **discordSurface** (fake `discordClient`): `Deliver` posts `BodyPrefix+body` to `d.Address` and records `receipt[id]=m.From.Ref`; a Record error does NOT fail Deliver (no double-post); `Poll` maps messages→Events with `ReplyToForeign` from `ReferencedID`, empty channel list → no-op, cursor JSON round-trips (encode∘decode identity, torn → empty).
- **directory.Route:** discord — reply-to a receipt → `To=[actor:<cove>]`, `From=human:<author>`, `ReplyTo=in:discord:<ref>`; no reference → unroutable; reference to an unknown id → unroutable. linear — `routeLinear` unchanged (keep the existing `TestDirectoryRoute` passing).
- **cmd:** `discordInboxChannels` collects distinct discord addresses; the end-to-end shape (Deliver records → Route resolves the same id → cove) via fakes.

## Docs (`docs/usage/harbor/comms-addressing.md` / `messaging.md`)

- The Discord reply loop: a human **replies** (Discord's reply feature) to a cove's message → the cove wakes with that reply. State the constraint: in a shared inbox, only a reply routes; a bare message is dropped. Note receipt growth is unpruned for now. Bump `updated`.

## Deferred / boundaries

- **Deferred:** discord roster-*channel* ingress (a human posting in a shared cove channel); receipt **pruning/rotation**; **seeding newly-rostered channels** (a freshly-added channel's ≤100 recent non-reply messages get polled-and-dropped once — harmless, since only replies-to-receipts route); webhook per-sender identity; the `internal/discord` extraction. Closes COV-179's ingress-`Route` half (discord routing is explicit; a linear channel is still matched by `Ref` in `routeLinear`, unchanged).
- **Boundaries:** `internal/msgport`/`internal/harbor` core unchanged (no import added; the discord surface/receipts/route live at `cmd/at-harbor`). cmd extends+imports `internal/switchboard` (wiring-layer reuse). No secret/token/message-body in any log line, receipt, cursor, or `Delivery`. The discord bot token stays host-resolved, never logged/injected. at-cove / connect / oidc untouched.
