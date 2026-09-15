# harbor Slice 5a — delivery-profile foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the roster data model for a second messaging service — a per-service `Human.Delivery` profile and a per-project `ChatService` — with admin/CLI surface, dormant (no routing change).

**Architecture:** Additive struct fields on `Human`/`Project` (JSON, both file + Postgres backends persist `Project` as a JSON doc → no migration); a `SetChatService` store method mirroring `SetEscalationPolicy`; admin/adminclient/CLI mirroring the escalation surface. Nothing consumes the new fields yet (Discord egress/ingress are 5b/5c).

**Tech Stack:** Go; `internal/harbor` (core + FileStore + PostgresStore + storetest conformance), `internal/harbor/adminclient`, `cmd/at-harbor`, hermetic tests.

## Global Constraints

- `internal/harbor` core stays msglog + stdlib (plain struct fields + one store method; no new import).
- Both Store backends (`FileStore`, `PostgresStore`) must implement any new `Store` method; the `internal/harbor/storetest` conformance test covers both.
- No secret in roster data (Discord *channel ids* only; the bot token is 5b serve-config, not here).
- Additive JSON only — old `Project`/`Human` docs must load with empty new fields (no migration, no version bump).
- Dormant: no change to `directory.Resolve`/`Route`, the msgport engine, or any delivery behavior.
- Tests hermetic (no network, no live DB — the conformance suite runs the pg backend against its existing test harness/skip, same as today). Build offline `GOPROXY=off`.
- The pre-existing uncommitted `.at-cove/config.yml` / `.at-cove/example-kitconfig.yml` / `.claude/` stay OUT of every commit — stage files explicitly by path.

## File Structure

- `internal/harbor/identity.go` — `DeliveryProfile`, `Human.Delivery`, `Human.DeliveryFor`, `Project.ChatService`.
- `internal/harbor/memstate.go` — `setChatService` pure helper (beside `setEscalation`).
- `internal/harbor/filestore.go` — `Store` interface += `SetChatService`; `FileStore.SetChatService`.
- `internal/harbor/pgstore.go` — `PostgresStore.SetChatService`.
- `internal/harbor/storetest/conformance.go` — delivery + chat-service round-trip.
- `internal/harbor/identity_test.go` (or existing) — `DeliveryFor`.
- `internal/harbor/admin.go` — `ChatServiceBody`/`ChatServiceView` + `PUT`/`GET /admin/projects/{project}/chat-service`.
- `internal/harbor/adminclient/adminclient.go` — `SetChatService`/`GetChatService`.
- `cmd/at-harbor/main.go` — `add-human --delivery`; `project chat-service` subcommand.
- `docs/usage/harbor/comms-addressing.md` — the model + CLI.

---

## Task 1: types + store (`SetChatService`) + conformance

**Files:**
- Modify: `internal/harbor/identity.go`, `internal/harbor/memstate.go`, `internal/harbor/filestore.go`, `internal/harbor/pgstore.go`
- Test: `internal/harbor/storetest/conformance.go`, `internal/harbor/identity_test.go`

**Interfaces:**
- Consumes: existing `Project`/`Human`/`Roster`; `setEscalation`/`copyProject`/`rawProject`/`applyPutProject`/`putProject` patterns.
- Produces: `DeliveryProfile`, `Human.Delivery`, `Human.DeliveryFor(service) (DeliveryProfile, bool)`, `Project.ChatService`, `Store.SetChatService(project, service string) error`, `setChatService(Project, string) Project`. Task 2 consumes these.

- [ ] **Step 1: Write the failing conformance + DeliveryFor tests**

In `internal/harbor/identity_test.go` (create if absent):
```go
func TestHumanDeliveryFor(t *testing.T) {
	h := Human{Name: "alice", Handle: "@alice", Delivery: []DeliveryProfile{{Service: "discord", Address: "chan-1"}}}
	if d, ok := h.DeliveryFor("discord"); !ok || d.Address != "chan-1" {
		t.Fatalf("DeliveryFor(discord) = %+v,%v", d, ok)
	}
	if _, ok := h.DeliveryFor("slack"); ok {
		t.Fatal("DeliveryFor(slack) should miss")
	}
}
```

In `internal/harbor/storetest/conformance.go`, extend the existing project/roster block (find where `AddHuman`/`SetEscalationPolicy` are exercised) with:
```go
		// delivery profile round-trips through AddHuman (upsert by name)
		if err := s.AddHuman("acme", harbor.Human{Name: "dave", Handle: "@dave", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "chan-9"}}}); err != nil {
			t.Fatalf("AddHuman with delivery: %v", err)
		}
		if r, ok := s.GetRoster("acme"); !ok {
			t.Fatal("GetRoster acme")
		} else {
			var dave harbor.Human
			for _, h := range r.Humans {
				if h.Name == "dave" {
					dave = h
				}
			}
			if d, ok := dave.DeliveryFor("discord"); !ok || d.Address != "chan-9" {
				t.Fatalf("dave delivery = %+v,%v", d, ok)
			}
		}
		// chat-service set + clear round-trips through GetProject
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatalf("SetChatService: %v", err)
		}
		if p, ok := s.GetProject("acme"); !ok || p.ChatService != "discord" {
			t.Fatalf("ChatService = %q (ok=%v), want discord", p.ChatService, ok)
		}
		if err := s.SetChatService("acme", ""); err != nil {
			t.Fatalf("SetChatService clear: %v", err)
		}
		if p, ok := s.GetProject("acme"); !ok || p.ChatService != "" {
			t.Fatalf("ChatService after clear = %q, want empty", p.ChatService)
		}
```
(Match the conformance file's actual variable/helper names — it runs each backend through the same closure; find the `acme` project setup it already does and append there. Confirm `GetProject`/`GetRoster` names.)

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/storetest/`
Expected: compile errors (`DeliveryProfile`, `Delivery`, `DeliveryFor`, `ChatService`, `SetChatService` undefined).

- [ ] **Step 3: Add the types (`identity.go`)**

```go
// DeliveryProfile is how a Human receives messages on one non-tracker Service.
// Address is the service-native delivery target: for "discord", the id of the
// inbox channel harbor posts the human's DMs into.
type DeliveryProfile struct {
	Service string `json:"service"`
	Address string `json:"address"`
}
```
Add `Delivery []DeliveryProfile \`json:"delivery,omitempty"\`` to `Human`, and:
```go
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
Add `ChatService string \`json:"chat_service,omitempty"\`` to `Project` (with the doc comment: service backing human DMs; "" = tracker @-mentions only).

- [ ] **Step 4: Add the `setChatService` helper (`memstate.go`)**

Beside `setEscalation`:
```go
// setChatService returns p with its chat service set (or cleared when "").
func setChatService(p Project, service string) Project {
	p.ChatService = service
	return p
}
```

- [ ] **Step 5: Add `SetChatService` to the interface + both backends**

In `filestore.go`, add to the `Store` interface (beside `SetEscalationPolicy`):
```go
	SetChatService(project, service string) error
```
`FileStore` (mirror `SetEscalationPolicy`):
```go
func (fs *FileStore) SetChatService(project, service string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutProject(setChatService(fs.rawProject(project), service))
	return fs.save()
}
```
`pgstore.go` `PostgresStore` (mirror its `SetEscalationPolicy`):
```go
func (s *PostgresStore) SetChatService(project, service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putProject(setChatService(copyProject(s.rawProject(project)), service))
}
```

- [ ] **Step 6: Run — verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/storetest/`
Expected: PASS. Also `GOPROXY=off go build ./...` (any fake `Store` implementations in other packages must now satisfy `SetChatService` — grep `harbor.Store` implementers / test fakes; if a fake breaks the build, add the method to it. Report any such fake touched).

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/identity.go internal/harbor/memstate.go internal/harbor/filestore.go internal/harbor/pgstore.go internal/harbor/storetest/conformance.go internal/harbor/identity_test.go
git commit -m "harbor: roster delivery-profile + project chat-service (data model) (COV-181)"
```
(Stage any test-fake file you had to extend, too — by explicit path.)

---

## Task 2: admin API + adminclient + CLI + docs

**Files:**
- Modify: `internal/harbor/admin.go` (route + body/view types)
- Modify: `internal/harbor/adminclient/adminclient.go` (`SetChatService`/`GetChatService`)
- Modify: `cmd/at-harbor/main.go` (`add-human --delivery`; `project chat-service`)
- Modify: `docs/usage/harbor/comms-addressing.md`
- Test: `internal/harbor/admin_test.go`, `internal/harbor/adminclient/adminclient_test.go`, `cmd/at-harbor/*_test.go` (match existing test files)

**Interfaces:**
- Consumes: `Store.SetChatService`, `GetProject`, `AddHuman`, `Human.Delivery` (Task 1).
- Produces: `ChatServiceBody{Service string}`, `ChatServiceView{Service string}`; `adminclient.SetChatService/GetChatService`; CLI `--delivery` + `project chat-service set|clear|show`.

- [ ] **Step 1: Write failing admin + adminclient tests**

In `internal/harbor/admin_test.go` (mirror the escalation route test):
```go
func TestChatServiceRoute(t *testing.T) {
	// build the admin handler over a test store with project "acme" (mirror the
	// escalation test's setup + operator auth)
	// PUT /admin/projects/acme/chat-service {"service":"discord"} → 200/204
	// GET /admin/projects/acme/chat-service → {"service":"discord"}
	// assert store.GetProject("acme").ChatService == "discord"
	// (also assert operator-auth is enforced, like the escalation route test)
}
```
In `internal/harbor/adminclient/adminclient_test.go` (mirror `SetEscalationPolicy`/`GetEscalationPolicy` tests): round-trip `SetChatService`/`GetChatService` against an `httptest` server that records the request path/body and returns a canned view; and an add-human carrying `Delivery` reaches the server body.

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/`
Expected: compile errors (`ChatServiceBody`/`SetChatService` undefined).

- [ ] **Step 3: Admin API (`admin.go`)**

Add the body/view types (beside `EscalationBody`/`EscalationView`):
```go
// ChatServiceBody is the PUT /admin/projects/{project}/chat-service body.
type ChatServiceBody struct {
	Service string `json:"service"` // "" clears (tracker @-mentions only)
}

// ChatServiceView is the GET /admin/projects/{project}/chat-service response.
type ChatServiceView struct {
	Service string `json:"service"`
}
```
Add the routes (mirror the escalation `GET`/`PUT` handlers, same operator-auth wrapping, `OperatorID(r)` logging — never log a token):
```go
	mux.HandleFunc("GET /admin/projects/{project}/chat-service", func(w http.ResponseWriter, r *http.Request) {
		p, _ := store.GetProject(r.PathValue("project"))
		writeJSON(w, ChatServiceView{Service: p.ChatService}) // use the file's existing JSON-write helper
	})
	mux.HandleFunc("PUT /admin/projects/{project}/chat-service", func(w http.ResponseWriter, r *http.Request) {
		var b ChatServiceBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := store.SetChatService(r.PathValue("project"), b.Service); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError) // match the escalation route's error style
			return
		}
		log.Info("admin chat-service", "operator", OperatorID(r), "project", r.PathValue("project"), "service", b.Service)
		w.WriteHeader(http.StatusNoContent) // match the escalation route's success code
	})
```
(Match the EXACT helpers/patterns the escalation handlers use — JSON write helper name, success status code, error handling, and how they're registered under operator auth. Read the escalation handlers first and mirror precisely.)

- [ ] **Step 4: adminclient (`adminclient.go`)**

Mirror `SetEscalationPolicy`/`GetEscalationPolicy`:
```go
// SetChatService sets (or clears, with "") the chat service backing project's human DMs.
func (c *Client) SetChatService(project, service string) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/chat-service", harbor.ChatServiceBody{Service: service}, nil)
}

// GetChatService returns project's configured chat service ("" if none).
func (c *Client) GetChatService(project string) (string, error) {
	var v harbor.ChatServiceView
	if err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/chat-service", nil, &v); err != nil {
		return "", err
	}
	return v.Service, nil
}
```

- [ ] **Step 5: CLI — `add-human --delivery` (`cmd/at-harbor/main.go`)**

Find the add-human flagset in the project-roster CLI handler. Add a repeatable string flag:
```go
	var delivery multiFlag // []string; if the repo has no repeatable-flag helper, define a tiny flag.Value
	fs.Var(&delivery, "delivery", "per-service delivery target, `service:address` (repeatable), e.g. discord:123456789")
```
After parsing, build `[]harbor.DeliveryProfile` (split each on the FIRST ":"; error on empty service/address):
```go
	var profiles []harbor.DeliveryProfile
	for _, d := range delivery {
		svc, addr, ok := strings.Cut(d, ":")
		if !ok || svc == "" || addr == "" {
			fmt.Fprintf(stderr, "at-harbor project roster add-human: invalid --delivery %q (want service:address)\n", d)
			return 2
		}
		profiles = append(profiles, harbor.DeliveryProfile{Service: svc, Address: addr})
	}
```
Pass `Delivery: profiles` into the `harbor.Human{...}` already built + sent via `adminclient.AddHuman`. (If a `multiFlag`/repeatable-string helper already exists in cmd, reuse it; otherwise add a minimal `type multiFlag []string` with `String()`/`Set()`.)

- [ ] **Step 6: CLI — `project chat-service` subcommand (`cmd/at-harbor/main.go`)**

In `cmdProject`, add a third subcommand group beside `roster`/`escalation` (mirror `cmdProjectEscalation`'s structure):
```go
	if len(args) >= 1 && args[0] == "chat-service" {
		return cmdProjectChatService(args[1:], g, stdout, stderr)
	}
```
Add `cmdProjectChatService` handling `set --project P --service discord`, `clear --project P`, `show --project P` — building an adminclient (same construction the escalation subcommand uses, honoring `--app`/`--admin-url`/token) and calling `SetChatService`/`GetChatService`. Update the `project` command Brief string to mention `chat-service set|clear|show`. `clear` = `SetChatService(project, "")`. `show` prints the service or "(none)".

- [ ] **Step 7: Run — verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/`
Expected: PASS. Add a cmd test for `--delivery` parsing (valid → profiles; `discord:` and `:x` and `x` → error) if the cmd test file has a pattern for exercising the roster CLI; otherwise assert the parsing helper directly.

- [ ] **Step 8: Docs (`docs/usage/harbor/comms-addressing.md`)**

Add a "Delivery profiles & per-project chat service" section to the doc that OWNS addressing: a human's per-service delivery profile (Discord = inbox channel id), the project `ChatService` selection, and the `add-human --delivery service:address` + `project chat-service set|clear|show` CLI. State this is the **data foundation** — Discord egress/ingress arrive in later slices; nothing routes to a non-tracker service today. Bump `updated:` to 2026-09-15.

- [ ] **Step 9: docs-audit + full build/test**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` (expect no NEW findings vs baseline — an existing doc edited).
Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` (expect all PASS).
Run: `GOPROXY=off go list -deps ./internal/harbor | grep -iE 'grpc|dispatch|/kit|backend|connect'` (expect empty).

- [ ] **Step 10: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go internal/harbor/adminclient/adminclient.go internal/harbor/adminclient/adminclient_test.go cmd/at-harbor/main.go cmd/at-harbor/*_test.go docs/usage/harbor/comms-addressing.md
git commit -m "harbor: admin/CLI for delivery-profile + chat-service + docs (COV-181)"
```
(Stage only the files you changed, by path.)

---

## Self-Review

**Spec coverage:** §1 types → Task 1 Steps 3-4. §2 store → Task 1 Steps 4-5 (+ conformance Step 1). §3 admin/adminclient/CLI → Task 2 Steps 3-6. §4 docs → Task 2 Step 8. All covered.

**Placeholder scan:** admin-route helper names (JSON-write helper, success code, auth wrapping) are flagged "mirror the escalation handlers exactly" — the implementer must read those first (concrete, in `admin.go`). The repeatable-flag helper (`multiFlag`) is "reuse if present, else add minimal" with the code given.

**Type consistency:** `DeliveryProfile{Service,Address}`, `Human.Delivery`, `Project.ChatService`, `SetChatService(project, service string) error`, `setChatService(Project,string) Project`, `ChatServiceBody{Service}`/`ChatServiceView{Service}`, `adminclient.SetChatService/GetChatService` — consistent across tasks. `AddHuman(project, harbor.Human)` unchanged (Delivery rides along).

**Risks flagged for review:** (1) adding `SetChatService` to the `Store` interface breaks any test fake implementing `harbor.Store` — Task 1 Step 6 catches it via the build; the reviewer should confirm every implementer was updated. (2) the pg conformance path — confirm the conformance suite's pg backend runs or skips as it does today (don't newly require a live DB). (3) confirm the fields are truly dormant — no `Resolve`/`Route`/engine reads `Delivery`/`ChatService` in this slice.
