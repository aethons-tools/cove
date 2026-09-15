# harbor: Slice 5b — Discord egress (COV-182)

**Status:** design approved (prefix-not-webhook + graceful-Linear-fallback decisions), pre-plan
**Issue:** COV-182. **Foundation:** COV-181 (5a delivery-profile — `Human.Delivery`/`Human.DeliveryFor`, `Project.ChatService`), COV-176 (egress cutover — `fileMarkers`/`logTailID`/seed pattern, `directory`/`linearSurface` shape), COV-172 (msgport seam). **Reuses:** `internal/switchboard.RESTClient` (hermetic Discord REST client). **Subsumes:** COV-179 (channel `Service` check).

## Summary

Stand up a **second msgport engine** for Discord so coves deliver outbound messages there: a `human:<name>` target on a discord-project → the human's Discord **inbox channel**; a discord roster `channel` → its channel. **Egress only** — ingress + reply-routing are 5c (the discord surface's `Poll` is an inert stub this slice, exactly as `linearSurface.Deliver` was before Slice 3). `discord.com` is already egress-allow-listed; no kit change.

## Decisions (from brainstorm)

- **Sender identity = a `"<cove>: "` body prefix**, not a webhook. Reuses the existing `Delivery.BodyPrefix` pipe and the bot `Post`. Webhook per-sender username/avatar (`Delivery.SenderName`/`SenderAvatar`) is a deferred v2 polish (needs per-channel webhook URLs).
- **Graceful fallback:** a `human:<name>` on a discord-project **without** a discord profile still reaches them via the Linear @mention (today's behavior) — not dropped.
- **Reuse, don't extract:** import `internal/switchboard.RESTClient` at the cmd wiring layer behind a narrow `discordPoster` interface; the `internal/discord` extraction is a later cleanup.

## 1. `discordSurface` (`cmd/at-harbor`, mirrors `linearSurface`)

```go
// discordPoster is the narrow slice of *switchboard.RESTClient discordSurface
// delivers through: post content to a Discord channel (as the bot).
type discordPoster interface {
	Post(ctx context.Context, channel, content string) error
}

type discordSurface struct {
	poster discordPoster
}

func (s *discordSurface) Service() string { return "discord" }

// Deliver posts BodyPrefix+m.Body to the Discord channel d.Address (the human's
// inbox channel, or a roster discord channel). Discord's create-message returns
// a message object but the RESTClient.Post wrapper doesn't surface an id, so
// foreignID is "" (exactly-once is the EgressMark's job; no idempotency footer).
func (s *discordSurface) Deliver(ctx context.Context, d msgport.Delivery, m msglog.Message) (string, error) {
	if err := s.poster.Post(ctx, d.Address, d.BodyPrefix+m.Body); err != nil {
		return "", fmt.Errorf("discord deliver: post to %q: %w", d.Address, err)
	}
	return "", nil
}

// Poll is an inert stub this slice — Discord ingress is 5c. Returning no events
// (not an error) keeps the engine's ingress loop a harmless no-op.
func (s *discordSurface) Poll(ctx context.Context, project, since string) ([]msgport.Event, string, error) {
	return nil, since, nil
}

func (s *discordSurface) Close() error { return nil }
```

## 2. `directory.Resolve` becomes service-aware (`cmd/at-harbor`, subsumes COV-179)

Rewrite `Resolve` to dispatch on the target and the roster's own service. Structure:

```go
func (d *directory) Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	switch to.Kind {
	case "human":
		return d.resolveHuman(service, project, to, from)
	case "channel":
		return d.resolveChannel(service, project, to, from)
	default: // actor → internal, in-band, never egressed
		return msgport.Delivery{}, false
	}
}
```

**Human** — pick the service the project uses for human DMs; fall back to Linear:
```go
func (d *directory) resolveHuman(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	self, haveSelf := d.instanceOf(from) // ListInstances → ActorID==from.Ref
	r, _ := d.store.GetRoster(project)
	h, hok := findHuman(r, to.Ref)
	proj, _ := d.store.GetProject(project) // for ChatService
	// Discord DM: project uses discord AND the human has a discord profile.
	if proj.ChatService == "discord" && hok {
		if p, ok := h.DeliveryFor("discord"); ok {
			if service != "discord" {
				return msgport.Delivery{}, false // the linear engine doesn't own this target
			}
			return msgport.Delivery{Service: "discord", Address: p.Address, BodyPrefix: from.Ref + ": "}, true
		}
		// discord project but no profile → fall through to Linear @mention.
	}
	// Linear @mention on the SENDER's own ticket (today's behavior).
	if service != "linear" {
		return msgport.Delivery{}, false
	}
	if !haveSelf || !hok {
		return msgport.Delivery{}, false
	}
	return msgport.Delivery{Service: "linear", Address: self.Unit, BodyPrefix: "@" + h.Handle + " "}, true
}
```

**Channel** — route by the roster channel's own `Service` (this is the COV-179 fix); own-ticket falls back to Linear:
```go
func (d *directory) resolveChannel(service, project string, to, from msglog.Target) (msgport.Delivery, bool) {
	if r, ok := d.store.GetRoster(project); ok {
		for _, ch := range r.Channels {
			if ch.Name != to.Ref {
				continue
			}
			chSvc := ch.Service
			if chSvc == "" {
				chSvc = "linear" // back-compat: existing channels had no Service
			}
			if service != chSvc {
				return msgport.Delivery{}, false // another engine owns it
			}
			if chSvc == "discord" {
				return msgport.Delivery{Service: "discord", Address: ch.Ref, BodyPrefix: from.Ref + ": "}, true
			}
			return msgport.Delivery{Service: "linear", Address: ch.Ref}, true // raw, no prefix (parity)
		}
	}
	// Not a roster channel → the cove's own ticket (channel:<Unit>) → Linear.
	if service != "linear" {
		return msgport.Delivery{}, false
	}
	return msgport.Delivery{Service: "linear", Address: to.Ref}, true
}
```

- `instanceOf`/`findHuman` are small local helpers (extract from the current inline loops). `GetProject` is on the store; add it to the narrow `instanceRoster` interface (it already has `ListInstances`/`GetRoster`) → rename it or add `GetProject`. *Confirm `instanceRoster` gains `GetProject(name string) (harbor.Project, bool)`.*
- **Byte-parity for Linear is preserved:** every linear branch returns exactly what today's `Resolve` returns (own-ticket raw, human `@handle ` on own ticket, roster-channel raw on `ch.Ref`). The existing `TestEgressGoldenParity` must still pass unchanged.
- **COV-179 subsumed:** a channel now resolves only for the engine whose `service` matches the channel's `Service` (empty → linear, back-compat). Update `directory.Route` (5c) later; egress `Resolve` is fixed here. (Note in the COV-179 issue that egress is done; ingress `Route` remains.)

## 3. Serve-config: `runtime.discord` (`cmd/at-harbor/config.go`)

```go
// discordConfig enables the resident Discord msgport engine (egress this slice).
type discordConfig struct {
	BotToken credSpec `yaml:"bot-token"` // resolved on the host; never logged/injected
}
```
Add `Discord *discordConfig \`yaml:"discord"\`` to `serveConfig.Runtime`. `validateDiscord()` (mirror `validateDispatcher`): when `Runtime.Discord != nil`, require a non-empty `bot-token` (Command or Value set). Call it from the serve-config validation aggregate.

## 4. cmd wiring (`cmd/at-harbor/main.go`)

Inside the existing `if messageLog != nil { … }` block (right after the linear engine, reusing the same `markers`/`cur`/`dir`):

```go
		if dcfg := cfg.Runtime.Discord; dcfg != nil {
			tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{dcfg.BotToken.toSpec("AT_DISCORD_BOT_TOKEN")})
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: discord bot-token:", err)
				return 1
			}
			dsurf := &discordSurface{poster: switchboard.NewRESTClient(tokEnv["AT_DISCORD_BOT_TOKEN"], nil)}
			if !markers.has("discord") { // seed: don't re-deliver the backlog to Discord
				if err := markers.SetEgress("discord", msgport.EgressMark{LastMsg: logTailID(messageLog)}); err != nil {
					fmt.Fprintln(stderr, "at-harbor: discord egress seed:", err)
					return 1
				}
			}
			deng := msgport.New(dsurf, messageLog, markers, cur, dir, msgport.Config{EgressEnabled: true}, log)
			go deng.Run(context.Background())
			log.Info("harbor msgport (discord): resident, egress ON")
		}
```

- The engine keys `EgressMark` by `Service()` → `markers` segregates `"linear"`/`"discord"` automatically; same file (`msgport-markers.json`).
- `switchboard.NewRESTClient(token, nil)` — channels (ingress) are nil this slice; egress `Post` takes an explicit channel.
- Shares `dir` (Resolve is service-parameterized; discord Resolve ignores `selfIdentity`).
- **The token is resolved on the host and never logged/injected** (same as the tracker token).

## Tests (hermetic)

- **discordSurface** (`cmd/at-harbor`): `Deliver` posts `BodyPrefix+m.Body` to `d.Address` via a fake `discordPoster` (records channel+content); post error propagates; `Poll` returns no events + the incoming `since`.
- **directory.Resolve** (extend `msgport_linear_test.go` or a new `msgport_discord_test.go`):
  - discord human: project `ChatService=discord` + human has `discord` profile → `Delivery{Service:"discord", Address:<inbox>, BodyPrefix:"<from>: "}` for `service=="discord"`; and `ok=false` for `service=="linear"` (linear engine doesn't own it).
  - fallback: discord project, human WITHOUT a discord profile → linear `@handle` (for `service=="linear"`).
  - non-discord project: human → linear `@handle` (discord engine returns ok=false).
  - discord channel (`Channel{Service:"discord", Ref:<id>}`) → `Delivery{Service:"discord", Address:<id>, BodyPrefix:"<from>: "}` for discord; ok=false for linear.
  - linear channel (`Service:""`/`"linear"`) → linear raw; ok=false for discord (COV-179).
  - own-ticket (`channel:<Unit>` not in roster) → linear raw.
  - **`TestEgressGoldenParity` unchanged and passing** (Linear byte-parity intact).
- **seed:** discord markers seeded to the Log tail on first enable; not overwritten on restart (reuse the existing seed test shape).
- **config:** `validateDiscord` — missing `bot-token` errors; present passes; `runtime.discord` unset is a no-op.

## Docs (`docs/usage/harbor/comms-addressing.md` + `messaging.md`)

- Discord egress is live: a discord-project's human DMs go to their inbox channel, a discord roster channel receives channel posts, each prefixed `"<cove>: "`. Requires a message-log + `runtime.discord.bot-token`. Sender-webhook identity is a future polish. Bump `updated`.

## Deferred / boundaries

- **Deferred:** 5c (Discord ingress `Poll` + `Route` + reply-to-message-id → cove reply-routing); webhook per-sender identity; `internal/discord` extraction; unifying `Human.Handle` into `Delivery`.
- **Boundaries:** `internal/msgport` and `internal/harbor` core unchanged (no import added; the discord surface/poster/config live at `cmd/at-harbor`). cmd importing `internal/switchboard` is a wiring-layer reuse behind the `discordPoster` interface. No secret/token/body in any log line, `Delivery`, marker, or cursor. at-cove / connect / oidc untouched. `discord.com` already egress-allow-listed → no kit change.
