# harbor: Slice 5a — delivery-profile foundation (COV-181)

**Status:** design approved (Discord-decomposition fork + 5a design), pre-plan
**Issue:** COV-181. **Foundation for:** Slice 5b (Discord egress), 5c (Discord ingress). **Builds on:** COV-161 (roster + addressing), COV-192/195 (Postgres control-plane Store + message log — both persist `Project` as a JSON doc).

## Summary

The addressing **data model** for a second messaging service (Discord), so the roster can describe *where* a human receives messages on a non-tracker service and *which* chat service a project uses for human DMs. **Pure, hermetic, and dormant** — no `Resolve`/`Route`/engine change, so no delivery behavior changes. Discord egress (5b) and ingress (5c) consume this foundation.

## Scope (from the Discord-decomposition fork)

Discord is a 3-slice epic: **5a data-model (this) → 5b egress → 5c ingress + reply-routing**. 5a adds only the data model + admin surface; it deliberately changes no routing.

## 1. Types (`internal/harbor/identity.go`)

Additive JSON. Both backends persist `Project` (which owns `Roster`→`Human`) as a JSON doc, so old rosters load with empty new fields — **no migration**.

```go
// DeliveryProfile is how a Human receives messages on one non-tracker Service.
// Address is the service-native delivery target: for "discord", the id of the
// inbox channel harbor posts the human's DMs into.
type DeliveryProfile struct {
	Service string `json:"service"` // e.g. "discord"
	Address string `json:"address"` // service-native target (Discord: inbox channel id)
}

type Human struct {
	Name     string            `json:"name"`
	Handle   string            `json:"handle"`             // Linear @-mention (unchanged)
	Delivery []DeliveryProfile `json:"delivery,omitempty"` // profiles for OTHER (non-tracker) services
}

// DeliveryFor returns the human's profile for service, if present.
func (h Human) DeliveryFor(service string) (DeliveryProfile, bool) {
	for _, d := range h.Delivery {
		if d.Service == service {
			return d, true
		}
	}
	return DeliveryProfile{}, false
}
```

`Project` gains one field:

```go
type Project struct {
	Name                 string                      `json:"name"`
	Roster               Roster                      `json:"roster"`
	Escalation           []EscalationTier            `json:"escalation,omitempty"`
	EscalationByCategory map[string][]EscalationTier `json:"escalation_by_category,omitempty"`
	ChatService          string                      `json:"chat_service,omitempty"` // service backing human DMs; "" = tracker @-mentions only
}
```

**Deliberate asymmetry (documented):** Linear delivery keeps using `Human.Handle`; `Delivery` carries the *other* services. Unifying the Linear handle into a profile would need a migration + a Linear-egress change — out of scope; noted as a future cleanup.

## 2. Store

- **`Human.Delivery`** needs no new store method: the existing `AddHuman(project, Human)` upserts the whole `Human` (upsert-by-name), so `Delivery` flows through once the field exists. The CLI/admin populate it.
- **`Project.ChatService`** gets a setter mirroring `SetEscalationPolicy` exactly:
  - Shared pure helper in `internal/harbor/memstate.go` (beside `setEscalation`/`upsertHuman`):
    ```go
    func setChatService(p Project, service string) Project { p.ChatService = service; return p }
    ```
  - `Store` interface gains `SetChatService(project, service string) error`.
  - `FileStore`: `fs.applyPutProject(setChatService(fs.rawProject(project), service)); return fs.save()`.
  - `PostgresStore`: `return s.putProject(setChatService(copyProject(s.rawProject(project)), service))`.
- **Conformance test** (`internal/harbor/storetest/conformance.go`): extend the existing project block — `AddHuman` with a `Delivery` profile round-trips (via `GetRoster`/`GetProject`, `DeliveryFor`); `SetChatService` set + clear (`""`) round-trips through `GetProject`. Runs against both backends.

## 3. Admin API + adminclient + CLI

- **`Human.Delivery` via add-human:** `POST /admin/projects/{project}/humans` already takes a `harbor.Human` body → `AddHuman`; `Delivery` rides along once the field exists. The **adminclient** add-human path and the **CLI** `project roster add-human` gain a repeatable `--delivery service:address` flag that populates `Human.Delivery` (declarative upsert — re-adding a human replaces it, matching existing add-human semantics). Parse each `--delivery` as `service:address` (split on the first `:`; reject empty service/address).
- **`Project.ChatService`:** a new admin route `PUT /admin/projects/{project}/chat-service` (operator-authenticated, mirroring the escalation route) with body `{"service": "..."}` → `SetChatService`; and a `GET` (or reflect it in the existing `GET /admin/projects/{project}/roster` / `GET /admin/projects` response). New adminclient methods `SetChatService(project, service)` + `GetChatService(project)` (or fold the read into an existing project view). New CLI subcommand group under `cmdProject`: `project chat-service --project P set --service discord | clear | show` (sibling of `roster`/`escalation`).
- The existing roster GET (`ActorSummary`/roster view) reflects `Delivery`; a project view reflects `ChatService`. No token/secret ever appears (a Discord *channel id* is not a secret; the bot token is serve-config for 5b, not roster data).

## 4. Docs (`docs/usage/harbor/comms-addressing.md`)

Extend the doc that OWNS addressing: the delivery-profile model (a human's per-service inbox) + the project chat-service selection. State plainly that this is the **data foundation** — Discord egress/ingress land in following slices; today nothing routes to a non-tracker service. Document the `add-human --delivery` and `project chat-service` CLI. Bump `updated`.

## Tests (hermetic)

- **conformance** (both backends): `Human.Delivery` round-trip + `DeliveryFor`; `SetChatService` set/clear round-trip; JSON back-compat (a project doc without the new fields loads with empty `Delivery`/`ChatService`).
- **identity**: `DeliveryFor` hit/miss.
- **admin**: `PUT /admin/projects/{project}/chat-service` sets it (operator-auth enforced, like escalation); add-human with `Delivery` persists + reflects in the roster GET.
- **adminclient**: `SetChatService`/`GetChatService` round-trip against a test server; add-human carries `Delivery`.
- **cmd**: `--delivery service:address` parsing (valid, malformed → error); `project chat-service set/clear/show`.

## Deferred / boundaries

- **Deferred:** 5b (Discord egress — `discordSurface.Deliver` reusing `internal/switchboard.RESTClient`, `directory.Resolve` routing discord-configured humans/channels, second engine, serve-config for the bot token + channels); 5c (Discord ingress + reply-to-message-id → cove reply-routing). Unifying `Human.Handle` into `Delivery`. Escalation-onto-Log (its own slice).
- **Boundaries:** `internal/harbor` core stays msglog + stdlib (these are plain struct fields + a store method; no new import). Adding a `Store` method touches both backends + the conformance test by design. No secret in roster data (channel ids only). at-cove / connect / oidc untouched.
