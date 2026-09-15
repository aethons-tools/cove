# harbor Slice 5b — Discord egress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver outbound cove messages to Discord via a second msgport engine — human DMs to a Discord inbox channel, discord roster channels to their channel — reusing the existing `switchboard.RESTClient`.

**Architecture:** A `discordSurface` (`Service()=="discord"`, `Deliver` posts `BodyPrefix+body`, `Poll` inert stub) + a service-aware `directory.Resolve` (routes discord humans/channels to discord, else Linear; preserves Linear byte-parity; subsumes COV-179) + a `runtime.discord` config + a second engine wired egress-on with a `"discord"`-keyed seed.

**Tech Stack:** Go; `cmd/at-harbor` (surface, directory, config, wiring — reusing `internal/switchboard.RESTClient`); hermetic tests via fakes. `internal/msgport`/`internal/harbor` core unchanged.

## Global Constraints

- `internal/msgport` and `internal/harbor` core gain NO new import; the discord surface/poster/config live at `cmd/at-harbor`. cmd importing `internal/switchboard` is a wiring-layer reuse behind the narrow `discordPoster` interface.
- **Linear byte-parity is preserved** — the existing `cmd/at-harbor` `TestEgressGoldenParity` must pass UNCHANGED (own-ticket raw, human `@handle ` on own ticket, roster-channel raw on `ch.Ref`).
- Discord egress uses the `Delivery.BodyPrefix` = `"<from.Ref>: "` prefix (sender identity); NO webhook, NO idempotency footer (EgressMark gives exactly-once).
- No secret/token/message-body in any log line, `Delivery`, marker, or cursor. The Discord bot token is resolved on the host (like the tracker token) and never logged/injected.
- The `"discord"` `EgressMark` is seeded to the Log tail on first enable (reuse `fileMarkers.has`/`logTailID`) so egress-on never re-delivers the backlog to Discord.
- Tests hermetic (no network, no live Discord). Build offline `GOPROXY=off`. `-race` unavailable (no cgo) — reason manually.
- The pre-existing uncommitted `.at-cove/config.yml` / `.at-cove/example-kitconfig.yml` / `.claude/` stay OUT of every commit — stage files explicitly by path.

## File Structure

- `cmd/at-harbor/msgport_discord.go` (new) — `discordPoster`, `discordSurface`.
- `cmd/at-harbor/msgport_linear.go` — `directory.Resolve` rewrite (service-aware); `instanceRoster` += `GetProject`.
- `cmd/at-harbor/config.go` — `discordConfig`, `Runtime.Discord`, `validateDiscord`.
- `cmd/at-harbor/main.go` — second engine wiring + seed.
- `cmd/at-harbor/msgport_discord_test.go` (new) + `msgport_linear_test.go` — surface + Resolve tests.
- `cmd/at-harbor/config_test.go` — `validateDiscord`.
- `docs/usage/harbor/comms-addressing.md` (+ `messaging.md`) — Discord egress.

---

## Task 1: `discordSurface` + service-aware `directory.Resolve`

**Files:**
- Create: `cmd/at-harbor/msgport_discord.go`, `cmd/at-harbor/msgport_discord_test.go`
- Modify: `cmd/at-harbor/msgport_linear.go` (`Resolve`, `instanceRoster`)
- Test: `cmd/at-harbor/msgport_linear_test.go` (Resolve cases), `cmd/at-harbor/msgport_discord_test.go`

**Interfaces:**
- Consumes: `msgport.Surface`/`Delivery`/`Event`, `msglog.Message`/`Target`, `harbor.Instance`/`Roster`/`Human.DeliveryFor`/`Project.ChatService`, `instanceRoster` (+ `GetProject`).
- Produces: `discordPoster interface { Post(ctx, channel, content string) error }`; `discordSurface{poster}`; service-aware `directory.Resolve`. Task 2 supplies the concrete poster (`switchboard.RESTClient`) + engine.

- [ ] **Step 1: Write failing `discordSurface` tests**

`cmd/at-harbor/msgport_discord_test.go`:
```go
type fakeDiscordPoster struct {
	posts   []struct{ channel, content string }
	postErr error
}
func (f *fakeDiscordPoster) Post(_ context.Context, channel, content string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, struct{ channel, content string }{channel, content})
	return nil
}

func TestDiscordDeliver(t *testing.T) {
	p := &fakeDiscordPoster{}
	s := &discordSurface{poster: p}
	if s.Service() != "discord" {
		t.Fatalf("Service = %q", s.Service())
	}
	m := msglog.Message{Body: "hello"}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Service: "discord", Address: "chan-1", BodyPrefix: "cove-1: "}, m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(p.posts) != 1 || p.posts[0].channel != "chan-1" || p.posts[0].content != "cove-1: hello" {
		t.Fatalf("posts = %+v", p.posts)
	}
}

func TestDiscordDeliverPropagatesError(t *testing.T) {
	s := &discordSurface{poster: &fakeDiscordPoster{postErr: fmt.Errorf("boom")}}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{Body: "x"}); err == nil {
		t.Fatal("expected post error")
	}
}

func TestDiscordPollIsInert(t *testing.T) {
	s := &discordSurface{}
	ev, next, err := s.Poll(context.Background(), "acme", "cursor-7")
	if err != nil || len(ev) != 0 || next != "cursor-7" {
		t.Fatalf("Poll = %v,%q,%v; want nil,cursor-7,nil", ev, next, err)
	}
}
```

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestDiscord`
Expected: compile errors (`discordSurface`, `discordPoster` undefined).

- [ ] **Step 3: Implement `msgport_discord.go`**

Create `cmd/at-harbor/msgport_discord.go` with `discordPoster`, `discordSurface{poster}`, `Service`/`Deliver`/`Poll`(inert stub)/`Close` exactly as the spec §1 shows.

- [ ] **Step 4: Run — verify pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestDiscord`
Expected: PASS.

- [ ] **Step 5: Write failing service-aware `Resolve` tests**

In `cmd/at-harbor/msgport_linear_test.go` (or msgport_discord_test.go), extend the test store to carry a `Project` with `ChatService` + a `GetProject`. If the existing test uses a `fakeStore`/narrow store, add `GetProject(name) (harbor.Project, bool)` to it. Add cases (a discord human, a discord channel, the fallback, COV-179):
```go
func TestResolveDiscordRouting(t *testing.T) {
	// store: project "acme" with ChatService="discord",
	//   Human{Name:"alice", Handle:"alice.h", Delivery:[{discord, "inbox-A"}]},
	//   Human{Name:"bob", Handle:"bob.h"},                    // no discord profile
	//   Channel{Name:"eng", Service:"discord", Ref:"disc-eng"},
	//   Channel{Name:"tick", Service:"linear", Ref:"ACME-9"},
	//   Instance{ActorID:"cove-1", Unit:"ACME-7"}
	from := msglog.Target{Kind: "actor", Ref: "cove-1"}
	dir := &directory{store: st, project: "acme"}

	// discord human via discord engine
	if d, ok := dir.Resolve("discord", "acme", tgt("human", "alice"), from); !ok || d.Service != "discord" || d.Address != "inbox-A" || d.BodyPrefix != "cove-1: " {
		t.Fatalf("discord human: %+v %v", d, ok)
	}
	// linear engine does NOT own the discord human
	if _, ok := dir.Resolve("linear", "acme", tgt("human", "alice"), from); ok {
		t.Fatal("linear must not own a discord-routed human")
	}
	// fallback: bob has no discord profile → Linear @mention (linear engine)
	if d, ok := dir.Resolve("linear", "acme", tgt("human", "bob"), from); !ok || d.Service != "linear" || d.Address != "ACME-7" || d.BodyPrefix != "@bob.h " {
		t.Fatalf("fallback human: %+v %v", d, ok)
	}
	if _, ok := dir.Resolve("discord", "acme", tgt("human", "bob"), from); ok {
		t.Fatal("discord must not own a profileless human (linear fallback owns it)")
	}
	// discord channel
	if d, ok := dir.Resolve("discord", "acme", tgt("channel", "eng"), from); !ok || d.Address != "disc-eng" || d.BodyPrefix != "cove-1: " {
		t.Fatalf("discord channel: %+v %v", d, ok)
	}
	// linear channel — discord engine must NOT own it (COV-179)
	if _, ok := dir.Resolve("discord", "acme", tgt("channel", "tick"), from); ok {
		t.Fatal("discord must not own a linear channel")
	}
	if d, ok := dir.Resolve("linear", "acme", tgt("channel", "tick"), from); !ok || d.Address != "ACME-9" || d.BodyPrefix != "" {
		t.Fatalf("linear channel: %+v %v", d, ok)
	}
	// own-ticket (not a roster channel) → linear raw
	if d, ok := dir.Resolve("linear", "acme", tgt("channel", "ACME-7"), from); !ok || d.Address != "ACME-7" || d.BodyPrefix != "" {
		t.Fatalf("own ticket: %+v %v", d, ok)
	}
}
```
(`tgt(kind, ref)` is a tiny helper for `msglog.Target{Kind:kind, Ref:ref}` — reuse if present.) Also add a non-discord-project case: a project with `ChatService==""` → a human resolves to Linear on the linear engine and ok=false on discord.

- [ ] **Step 6: Run — verify failing**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run 'TestResolve|TestEgressGoldenParity'`
Expected: FAIL/compile — `Resolve` isn't service-aware yet; `GetProject` missing on the store interface/fake.

- [ ] **Step 7: Add `GetProject` to `instanceRoster`; rewrite `Resolve`**

In `msgport_linear.go`: add `GetProject(name string) (harbor.Project, bool)` to the `instanceRoster` interface (the concrete `*FileStore`/`*PostgresStore` already have it; the test fake gets it in Step 5). Rewrite `Resolve` per spec §2 (`resolveHuman`/`resolveChannel` helpers + `instanceOf`/`findHuman`). Preserve every Linear branch's exact output (own-ticket raw / human `@handle ` on own ticket / roster-linear-channel raw on `ch.Ref`).

- [ ] **Step 8: Run — verify pass (incl. unchanged byte-parity)**

Run: `GOPROXY=off go test ./cmd/at-harbor/`
Expected: PASS — the new Resolve tests AND the unchanged `TestEgressGoldenParity` (Linear parity intact) AND the existing `TestResolveUnroutableAndNonLinear`/`TestDirectoryRoute`. If `TestEgressGoldenParity` needs its fake store to gain `GetProject`, add it (returning a zero Project → `ChatService==""` → Linear path, preserving parity).

- [ ] **Step 9: Commit**

```bash
git add cmd/at-harbor/msgport_discord.go cmd/at-harbor/msgport_discord_test.go cmd/at-harbor/msgport_linear.go cmd/at-harbor/msgport_linear_test.go
git commit -m "harbor: discord egress surface + service-aware Resolve (COV-182)"
```

---

## Task 2: config + wiring + docs

**Files:**
- Modify: `cmd/at-harbor/config.go` (`discordConfig`, `Runtime.Discord`, `validateDiscord`), `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go` (second engine + seed)
- Modify: `docs/usage/harbor/comms-addressing.md`, `docs/usage/harbor/messaging.md`

**Interfaces:**
- Consumes: `discordSurface`, `directory` (Task 1); `switchboard.NewRESTClient`, `secret.Resolve`, `credSpec.toSpec`, `fileMarkers`/`logTailID`, `msgport.New`.

- [ ] **Step 1: Write failing `validateDiscord` test**

In `cmd/at-harbor/config_test.go` (mirror a `validateDispatcher` test):
```go
func TestValidateDiscord(t *testing.T) {
	var c serveConfig
	c.Runtime.Discord = &discordConfig{} // no bot-token
	if err := c.validateDiscord(); err == nil {
		t.Fatal("expected error for missing bot-token")
	}
	c.Runtime.Discord = &discordConfig{BotToken: credSpec{Value: "x"}}
	if err := c.validateDiscord(); err != nil {
		t.Fatalf("valid discord: %v", err)
	}
	var empty serveConfig // Discord unset → no-op
	if err := empty.validateDiscord(); err != nil {
		t.Fatalf("unset discord must be a no-op: %v", err)
	}
}
```

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestValidateDiscord`
Expected: compile error (`discordConfig`/`validateDiscord` undefined).

- [ ] **Step 3: Config (`config.go`)**

Add `discordConfig{ BotToken credSpec \`yaml:"bot-token"\` }`, `Discord *discordConfig \`yaml:"discord"\`` to `serveConfig.Runtime`, and `validateDiscord()` (nil → nil; else require `BotToken.Command` or `BotToken.Value` non-empty). Wire `validateDiscord()` into wherever `validateDispatcher()`/`validateLauncher()` are aggregated (find the serve-config validation caller and add it).

- [ ] **Step 4: Run — verify pass**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run TestValidateDiscord`
Expected: PASS.

- [ ] **Step 5: Wire the second engine (`main.go`)**

Inside the existing `if messageLog != nil { … }` block (right after the linear engine, reusing `markers`/`cur`/`dir`), add the discord block from spec §4 (`secret.Resolve` the bot-token → `switchboard.NewRESTClient(token, nil)` → `discordSurface` → seed `"discord"` if `!markers.has("discord")` → `msgport.New(..., EgressEnabled:true)` → `go deng.Run(...)` + a log line). Add the `internal/switchboard` import.

- [ ] **Step 6: Build + full test**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...`
Expected: PASS across the module.

- [ ] **Step 7: Docs**

`docs/usage/harbor/comms-addressing.md` (+ a line in `messaging.md` if it owns the egress description): Discord egress is live — a discord-project's human DMs go to the human's inbox channel, discord roster channels receive posts, each prefixed `"<cove>: "`; requires a message-log + `runtime.discord.bot-token`; sender-webhook identity is a future polish. Bump `updated:` to 2026-09-15.

- [ ] **Step 8: docs-audit + boundary**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` (no NEW findings vs baseline).
Run: `GOPROXY=off go list -deps ./internal/harbor | grep -iE 'grpc|dispatch|/kit|backend|connect'` (empty).
Run: `GOPROXY=off go list -deps ./internal/msgport | grep -iE 'harbor|switchboard|dispatch'` (empty — msgport stays msglog+stdlib).

- [ ] **Step 9: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go docs/usage/harbor/comms-addressing.md docs/usage/harbor/messaging.md
git commit -m "harbor: wire discord egress engine + config + docs (COV-182)"
```

---

## Self-Review

**Spec coverage:** §1 surface → Task 1 Steps 3. §2 Resolve → Task 1 Step 7. §3 config → Task 2 Step 3. §4 wiring → Task 2 Step 5. Docs → Task 2 Step 7. COV-179 subsumed in Resolve (Task 1). All covered.

**Placeholder scan:** the test store's `GetProject` addition is flagged (Task 1 Steps 5/7/8). The serve-config validation aggregate caller is "find and add `validateDiscord`" — concrete (grep `validateDispatcher(`).

**Type consistency:** `discordPoster.Post(ctx, channel, content string) error` matches `*switchboard.RESTClient.Post`. `discordSurface` satisfies `msgport.Surface` (Service/Deliver/Poll/Close). `Resolve(service, project string, to, from msglog.Target) (msgport.Delivery, bool)` unchanged signature; `instanceRoster` gains `GetProject`. `discordConfig.BotToken credSpec` + `toSpec` matches the tracker-token pattern. Seed reuses `fileMarkers.has`/`SetEgress` + `logTailID` (Slice 3).

**Risks flagged for review:** (1) Linear byte-parity — `TestEgressGoldenParity` must pass unchanged; the reviewer verifies the Linear branches are output-identical. (2) the discord engine's ingress loop runs a stub `Poll` on a timer — confirm it's a harmless no-op (returns empty, not error). (3) the seed prevents backlog re-delivery to Discord — confirm `markers.has("discord")` guards it and it's keyed separately from `"linear"`. (4) adding `GetProject` to `instanceRoster` — confirm every implementer (concrete stores + fakes) has it and the build is clean.
