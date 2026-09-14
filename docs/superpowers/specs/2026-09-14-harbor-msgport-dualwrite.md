# harbor: msgport Slice 1a — outbound dual-write shadow (COV-173)

**Status:** design approved, pre-plan
**Issue:** COV-173 (msgport Slice 1a — the egress-shadow half). **Foundation:** COV-172 (msgport seam spine), COV-171 (msglog substrate).

## Summary

`/messages` sends start **shadow-writing** into the durable message Log: after the existing `DecideSend` + `PostComment` (the unchanged *live* delivery), `handlePost` also **best-effort appends** the logical, raw message. Nothing reads the Log yet — this just begins populating it from the real send path, exactly the data Slice 3's egress cutover will later deliver. **Zero live-behavior change.**

## 1. The dual-write (`internal/harbor/messages.go`)

`MessagesHandler` gains a narrow appender dep (nil-safe):

```go
type appender interface {
	Append(m msglog.Message) (msglog.Message, error)
}
// MessagesHandler gains: lg appender  (nil when message-log is unconfigured)
```
`NewMessagesHandler(store messagesStore, cmt Commenter, lg appender, log *slog.Logger) *MessagesHandler` — the `lg` param is inserted; a nil `lg` disables the dual-write.

In `handlePost`, after `PostComment` succeeds and before the `204`, append the **logical, raw** message (best-effort — an append error is logged, never fails the send):

```go
if h.lg != nil {
	to := msglog.Target{Kind: "channel", Ref: inst.Unit} // no `to` → the cove's own ticket-as-channel
	if req.To != "" {
		to = msglog.Target{Kind: st.Kind, Ref: st.Name} // st = the DecideSend SendTarget: human:<Name> / channel:<Name>
	}
	if _, err := h.lg.Append(msglog.Message{
		From:    msglog.Target{Kind: "actor", Ref: actor.ID},
		To:      []msglog.Target{to},
		Body:    req.Body, // RAW body — @handle rendering is the adapter's job at egress (Slice 3)
		Project: inst.Project,
	}); err != nil {
		h.log.Warn("messages: shadow append failed", "actor", actor.ID, "error", err.Error())
	}
}
```

- `st` is the `SendTarget` already returned by `DecideSend` in the `req.To != ""` branch — it must be in scope at the append point (it is authorized+resolved before delivery). For `req.To == ""`, the logical target is the cove's own ticket as `channel:<inst.Unit>`.
- The **raw** `req.Body` is stored, NOT the `@`-prefixed `body` used for the live `PostComment`. The Log is Service-agnostic; the `@handle` prefix is a Linear rendering detail `linearSurface.Deliver` re-applies at egress (Slice 3), where a golden-output parity test will confirm it matches today.
- `read`/`GET`, `/messages/targets`, `/escalate`, and the escalation engine are UNCHANGED (escalation dual-write is Slice 4).

## 2. Wiring (`cmd/at-harbor/main.go`)

The Log is already opened in `cmdServe` inside the admin block (`ml`, for the read-only adminui `MessageReader` — added anticipating "the deferred writer slices"). **Hoist that open** to a single point before the dispatcher block, open the one `*msglog.Log` once (guarded by `cfg.MessageLog != ""`, `defer ml.Close()`), and share it:

- `NewMessagesHandler(st, linearCommenter{tracker}, ml, log)` — `ml` (or nil when unconfigured) is the writer.
- the adminui `MessageReader` keeps using the SAME `ml` (remove the duplicate open in the admin block).

When `cfg.MessageLog == ""`, `ml` is nil → the handler skips the dual-write and the adminui view renders its existing "not configured" notice — unchanged behavior.

## 3. Docs

One line in `docs/usage/harbor/messaging.md`: when `message-log` is configured, `/messages` sends are now also **shadow-recorded** to the durable Log (visible in the read-only admin message view); delivery is unchanged. (This is the first writer to the Log — link to the msgport arc / spec for the bigger picture.) Bump `updated`.

## Tests (hermetic)

Extend `internal/harbor/messages_test.go` with a `fakeAppender` (records appended messages, optional error):
- **own-ticket:** `send(body)` (no `to`) → appended `To == [channel:<inst.Unit>]`, `From == actor:<actor.ID>`, `Body == raw`, `Project == inst.Project`; AND the existing `PostComment` still fired (live path intact).
- **human:** `send(body, to=human:alice)` → appended `To == [human:alice]`, `Body == raw` (NOT `@handle …`).
- **channel:** `send(body, to=channel:eng)` → appended `To == [channel:eng]`.
- **append error is swallowed:** a `fakeAppender` returning an error → response is still `204` and `PostComment` still succeeded.
- **nil appender:** `NewMessagesHandler(store, cmt, nil, log)` → send works, no panic, nothing appended.
- (cmd) if `cmd/at-harbor` has a wiring/mux test, keep it green with the new `NewMessagesHandler` arity.

## Deferred (later slices)

1b (ingress-shadow: the filtered team-comments feed → `linearSurface.Poll`, `Directory.Route`, the msgport `Engine` with egress-off); Slice 2 (wake-on onto the Log); Slice 3 (egress cutover: `linearSurface.Deliver` + `Directory.Resolve` + golden-output parity + `handlePost` Append-only); Slice 4 (GET + escalation onto the Log); Slice 5 (Discord).

## Boundaries

`internal/harbor` may import `internal/msglog` (stdlib-only, no cycle) — it already does elsewhere. The dual-write is best-effort and never alters the live send outcome. No secret value is ever part of an appended message beyond the message body the cove already sent (bodies are not logged at any level; only a Warn on append error, with no body).
