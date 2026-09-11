# Route a Hardened Cove Through Harbor (COV-138) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `harbor:` kit block that routes a hardened cove's Anthropic + git through the harbor broker (over 443, via squid) while the cove holds only its identity token — no sealed-egress weakening.

**Architecture:** Config (`internal/kit`) carries the block and folds harbor's host into the baked allow-list; the backend adds a `--add-host …:host-gateway` mapping; `internal/connect` gains a harbor auth mode that supersedes OAuth/Vertex and sources the connector snippet into the session env-script; the composition root wires it and resolves the token host-side. The enroll-snippet renderer is shared via a stdlib-only leaf so at-cove doesn't pull go-oidc.

**Tech Stack:** Go (stdlib) + `gopkg.in/yaml.v3`. No new dependency.

## Global Constraints

- Module `github.com/aethons-tools/cove`; stdlib + `yaml.v3` + `coreos/go-oidc/v3` only — no new dependency. The **at-cove binary must not gain go-oidc**: share the snippet via a stdlib-only leaf (Task 2), do not import `internal/harbor` from at-cove.
- Tests hermetic: `runner.Fake`, no Docker/VM/network; real end-to-end behind `//go:build integration` / manual.
- **Do not weaken the sealed layer:** no edits to `internal/assemble/hardening/image-files/etc/{nftables.conf,squid/squid.conf}`. Harbor is reached on 443 through squid; its host is an additive allow-list entry only.
- Token stays env-only (the tmpfs sourced script) — never argv/logs/disk/kit-secret-store.
- Before each commit: `go build ./...`, `go vet ./...`, `gofmt -l internal/ cmd/` clean (leave the pre-existing `internal/switchboard/discord_integration_test.go`).
- All existing behavior (kits without a `harbor:` block) unchanged.

---

### Task 1: Snippet leaf — `internal/harbor/snippet` (stdlib-only, shared)

Extract the connector-snippet renderer so both harbor and at-cove use one source without at-cove importing the go-oidc-bearing `internal/harbor`.

**Files:**
- Create: `internal/harbor/snippet/snippet.go`
- Create: `internal/harbor/snippet/snippet_test.go`
- Modify: `internal/harbor/enroll.go` (delegate to the leaf)

**Interfaces:**
- Produces: `snippet.Render(baseURL, token string) string` — the env exports + git config the client sources.

- [ ] **Step 1: Move the test.** Create `internal/harbor/snippet/snippet_test.go` mirroring the current `TestRenderEnrollSnippetIncludesEndpointsNotSecrets` from `internal/harbor/enroll_test.go`, but calling `Render(...)` and asserting the same strings (`export AT_HARBOR_IDENTITY_TOKEN=TOK123`, `ANTHROPIC_BASE_URL=…/anthropic`, `ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN`, `url."…/git/".insteadOf`, `credential."…".helper`, `username=x-access-token`, `password=$AT_HARBOR_IDENTITY_TOKEN`, and the raw token appears exactly once).

- [ ] **Step 2: Run to verify it fails.** `go test ./internal/harbor/snippet/ 2>&1 | head` → FAIL (`undefined: Render`).

- [ ] **Step 3: Implement `internal/harbor/snippet/snippet.go`.** Move the body of `RenderEnrollSnippet` here verbatim as `func Render(baseURL, token string) string` (package `snippet`, imports `fmt`/`strings` only).

- [ ] **Step 4: Delegate from harbor.** In `internal/harbor/enroll.go`, replace `RenderEnrollSnippet`'s body with `return snippet.Render(baseURL, token)` (import `github.com/aethons-tools/cove/internal/harbor/snippet`). Keep the exported `RenderEnrollSnippet` (at-harbor's `cmdEnroll` still calls it).

- [ ] **Step 5: Run to verify.** `go test ./internal/harbor/... -count=1` (both the leaf and the existing enroll test pass). vet + gofmt.

- [ ] **Step 6: Commit.**
```bash
git add internal/harbor/snippet/ internal/harbor/enroll.go
git commit -m "refactor(harbor): extract connector snippet into a stdlib-only leaf package"
```

---

### Task 2: Kit config — the `harbor:` block + allow-list union

**Files:**
- Modify: `internal/kit/config.go`
- Modify: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `Config.Harbor *HarborConfig{ Host string; Identity string; ViaHostGateway *bool }`; `(*HarborConfig).HostGateway() bool` (nil→true); `RootDomains` includes `Harbor.Host` when set.

- [ ] **Step 1: Read** the config struct top, `RootDomains` (~line 1150), `unionDomains` (~387), and the validation section (~530) to match style/placement.

- [ ] **Step 2: Write the failing tests** in `internal/kit/config_test.go`:
  - parse a kit with `harbor: { host: harbor.local.aethons.tools, identity: harbor-id }` → `Harbor.Host`/`Identity` set, `Harbor.HostGateway() == true` (default).
  - `via-host-gateway: false` → `HostGateway() == false`.
  - `RootDomains(c)` contains `harbor.local.aethons.tools` when the block is set; does not when absent.
  - validation: `harbor:` present with empty `host` → error; `host` with a scheme (`https://…`) or `:port` → error; empty `identity` → error.

- [ ] **Step 3: Run to verify it fails.**

- [ ] **Step 4: Implement.** Add `HarborConfig` + the `Harbor *HarborConfig` field (yaml `harbor`); `HostGateway()` (nil→true); union `c.Harbor.Host` into `RootDomains`; add validation (host non-empty, bare hostname via `net/url`-free check — reject if it contains `/` or `:` or a scheme; identity non-empty). Follow the existing validation idiom.

- [ ] **Step 5: Run to verify + vet + gofmt.**

- [ ] **Step 6: Commit** (`feat(cove): harbor kit config block + allow-list entry`).

---

### Task 3: Backend routability — `--add-host <host>:host-gateway`

**Files:**
- Modify: `internal/backend/backend.go` (add `ExtraHosts []string` to `CreateContext`, and to the ephemeral run context if separate)
- Modify: `internal/backend/colima/colima.go` (Create argv)
- Modify: `internal/backend/colima/dispatch.go` (RunEphemeral argv)
- Modify: the colima argv test(s) under `internal/backend/colima/`

**Interfaces:**
- Consumes: `CreateContext.ExtraHosts`.
- Produces: `--add-host h:host-gateway` in the `docker run` argv for each entry.

- [ ] **Step 1: Read** `backend.go` `CreateContext` (~70), and the colima `Create`/`RunEphemeral` argv builders (`colima.go` ~223, `dispatch.go` ~31) + their existing argv tests.

- [ ] **Step 2: Write the failing test** — extend the colima argv test: given a create context with `ExtraHosts: ["harbor.local.aethons.tools"]`, the built argv contains `--add-host harbor.local.aethons.tools:host-gateway` (as two args or `--add-host=…` matching the existing arg style); empty → no `--add-host`.

- [ ] **Step 3: Run to verify it fails.**

- [ ] **Step 4: Implement.** Add `ExtraHosts []string` to `CreateContext` (and the ephemeral context if it's a distinct type). In both argv builders, append `--add-host`, `h+":host-gateway"` for each host, next to the existing `dnsArgs`.

- [ ] **Step 5: Run to verify + vet + gofmt.**

- [ ] **Step 6: Commit** (`feat(cove): backend --add-host host-gateway for harbor routability`).

---

### Task 4: Connect — harbor auth mode (supersede) + snippet injection

**Files:**
- Modify: `internal/connect/connect.go`
- Modify: `internal/connect/connect_test.go` (or the transport/connect test file)

**Interfaces:**
- Produces: `Options.Harbor *HarborAuth{ Host, Token string }`. When set: skip OAuth/Vertex seeding; append `snippet.Render("https://"+Host, Token)` to the sourced env-script.

- [ ] **Step 1: Read** `connect.go` `Options` (~92), the `if !o.SkipAuth { … }` auth branch (~158) incl. Vertex/OAuth seeding, how `ExtraEnv`/env is assembled (~113) and passed to `t.Launch` (~194), and the `StdinScript` env-script seam (`transport.go` ~131-162) to find where to append shell.

- [ ] **Step 2: Write the failing test** — with `Options.Harbor{Host:"harbor.test", Token:"TOK"}` (and a `runner.Fake`), assert: (a) no OAuth/Vertex seeding runs, and (b) the launched env-script/remote input contains `ANTHROPIC_BASE_URL=https://harbor.test/anthropic`, `ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN`, `export AT_HARBOR_IDENTITY_TOKEN=TOK`, and the git `insteadOf`/helper lines; the raw `TOK` appears once. Mirror the existing connect test harness.

- [ ] **Step 3: Run to verify it fails.**

- [ ] **Step 4: Implement.** Add `Options.Harbor *HarborAuth`. In the auth region, when `o.Harbor != nil`: take the harbor branch **instead of** OAuth/Vertex; thread the snippet shell into the same sourced-script path the transport already uses (append to the script the transport sources, or add a dedicated field the transport concatenates). Import `internal/harbor/snippet`.

- [ ] **Step 5: Run to verify + vet + gofmt.**

- [ ] **Step 6: Commit** (`feat(cove): connect harbor auth mode — connector snippet, supersedes OAuth/Vertex`).

---

### Task 5: Composition root — wire the `harbor:` block into a session

**Files:**
- Modify: `cmd/at-cove/main.go` (chat/work/teammate paths that build `connect.Options` + the create context)
- Modify: `cmd/at-cove/*_test.go` (the seams already used by chat/teammate tests)

**Interfaces:**
- Consumes: `Config.Harbor` (Task 2), `connect.Options.Harbor` (Task 4), `CreateContext.ExtraHosts` (Task 3), `secret.Resolve`.

- [ ] **Step 1: Read** where `connect.Options` is built (chat ~1038, and the work/teammate equivalents), where `CreateContext` is built (create/install path), and how a single secret is resolved host-side (`store.Plan`/`secret.Resolve`, ~934).

- [ ] **Step 2: Write the failing test** — a hermetic test (extending the chat/teammate test harness) where a kit with a `harbor:` block + a supplied `identity` produces: `connect.Options.Harbor` set with the resolved token, and the create context carries `ExtraHosts:["<host>"]` (when host-gateway). Assert the token isn't in argv.

- [ ] **Step 3: Run to verify it fails.**

- [ ] **Step 4: Implement.** When `cfg.Harbor != nil`: resolve `cfg.Harbor.Identity` host-side (through the existing supply), set `connect.Options.Harbor = &connect.HarborAuth{Host: cfg.Harbor.Host, Token: <resolved>}`, and set `CreateContext.ExtraHosts = []string{cfg.Harbor.Host}` when `cfg.Harbor.HostGateway()`. Apply on the create path (so `--add-host` is present) and on each session path that builds `connect.Options`.

- [ ] **Step 5: Run to verify + full build/test.**

- [ ] **Step 6: Commit** (`feat(cove): wire the harbor: block into create + session connect`).

---

### Task 6: Docs

**Files:**
- Modify: `docs/OVERVIEW.md`, `docs/usage/at-cove-config.md`, `docs/usage/INDEX.md`

- [ ] **Step 1:** `docs/usage/at-cove-config.md` — document the `harbor:` block (`host`, `identity`, `via-host-gateway`), what enabling it does (allow-list entry, `--add-host`, connector snippet superseding OAuth/Vertex), the 443 requirement, and that it needs `at-cove recreate` (bakes allow-list + add-host).
- [ ] **Step 2:** `docs/OVERVIEW.md` — one line in the auth/egress area noting a cove can route Anthropic+git through harbor via a `harbor:` block; link the spec.
- [ ] **Step 3:** `docs/usage/INDEX.md` — a row pointing at the COV-138 spec (or the config doc section).
- [ ] **Step 4:** `just test && STRICT=1 ./scripts/lint.sh`; commit (`docs: harbor: kit block (cove→harbor networking)`).

---

## Manual verification (definition of done)

1. A kit with `harbor: { host: harbor.local.aethons.tools, identity: <supplied>, via-host-gateway: true }`, `at-cove recreate`d.
2. Inside the cove: `claude` runs a real turn (Anthropic via harbor); `git clone` a private repo works (git via harbor). No real Anthropic key or git PAT is present in the cove.
3. A kit **without** `harbor:` behaves exactly as today (OAuth/Vertex, sealed egress).

## Self-review

**Spec coverage:** `harbor:` block + validation → Task 2; allow-list entry → Task 2 (`RootDomains`); routability `--add-host` → Task 3; auth supersede + snippet injection → Task 4; token resolved host-side + wiring → Task 5; shared stdlib snippet (no go-oidc in at-cove) → Task 1; docs → Task 6. Deferred (auto-enroll, non-443, gitlab, harbor-as-container) correctly absent.

**Placeholder scan:** Tasks 2–5 begin with a **Read** step because they touch less-familiar cross-package code; the *new* code (types, argv, snippet wiring) is specified, and each edit is exact-at-implementation after the read. No "TBD"/vague-requirement steps.

**Type consistency:** `HarborConfig{Host,Identity,ViaHostGateway}` (Task 2) → consumed in Task 5; `connect.HarborAuth{Host,Token}` (Task 4) → built in Task 5; `CreateContext.ExtraHosts` (Task 3) → set in Task 5; `snippet.Render(baseURL, token)` (Task 1) → used in Task 4 and by `harbor.RenderEnrollSnippet`.
