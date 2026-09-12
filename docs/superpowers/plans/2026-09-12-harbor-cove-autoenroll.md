# Cove Auto-Enrollment with Harbor (COV-141) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a `harbor:` kit omits `identity`, at-cove mints a fresh per-cove identity at session start (shelling `at-harbor enroll --json`) and revokes it at exit — no manual enroll.

**Architecture:** at-cove shells a sibling `at-harbor` (mirroring how it shells `at-mint`), so it reuses the CLI's operator-auth and stays go-oidc-free. `harborPlan` gains an auto-enroll branch returning the auth + a revoke closure; `doChat` defers the revoke.

**Tech Stack:** Go (stdlib) + `gopkg.in/yaml.v3`. No new dependency; at-cove must NOT import `internal/harbor/adminclient` (go-oidc).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **at-cove stays go-oidc-free** — shell `at-harbor`, never import adminclient/internal-harbor.
- The minted token flows via the child's **stdout** (captured in memory) → `HarborAuth` env-only; never on argv, logs, disk, or the kit secret store.
- Tests hermetic (`runner.Fake`); real round-trip behind `//go:build integration` / manual.
- Before each commit: `go build ./...`, `go vet ./...`, `gofmt -l internal/ cmd/` clean (leave the pre-existing `internal/switchboard/discord_integration_test.go`).
- Existing behavior unchanged: `harbor.identity` set → pre-supplied path; no `harbor:` block → nothing shelled.

---

### Task 1: `at-harbor enroll --json`

**Files:**
- Modify: `cmd/at-harbor/main.go` (`cmdEnroll`)
- Modify: `cmd/at-harbor/main_test.go`

**Interfaces:**
- Produces: `at-harbor enroll --json` prints `{"id":"…","token":"…"}` to stdout (no snippet); `--base-url` not required with `--json`.

- [ ] **Step 1: Write the failing test.** In `cmd/at-harbor/main_test.go`, add a test that runs `run([]string{"enroll", "--json", "--id", "spider-18", ...}, …)` against the loopback admin handler (mirror the existing enroll test's server harness) and asserts stdout parses as JSON with non-empty `id`/`token` and does **not** contain `export ANTHROPIC_BASE_URL` (no snippet). Also assert `--json` without `--base-url` succeeds.

- [ ] **Step 2: Run to verify it fails** (`--json` unknown flag / snippet still printed).

- [ ] **Step 3: Implement.** In `cmdEnroll`: add `jsonOut := fs.Bool("json", false, "print {\"id\",\"token\"} JSON instead of the shell snippet")`. When `*jsonOut`, skip the `--base-url` requirement, and after a successful `Enroll`, `json.NewEncoder(stdout).Encode(struct{ID, Token string `json:"id"`/`json:"token"`}{res.ID, res.Token})` instead of `RenderEnrollSnippet`. Keep the snippet path (and its `--base-url` requirement) unchanged when `--json` is absent. Add `"encoding/json"` if needed.

- [ ] **Step 4: Run to verify it passes.**

- [ ] **Step 5: vet + gofmt + commit** (`feat(harbor): at-harbor enroll --json (machine-readable id+token)`).

---

### Task 2: at-cove sibling `at-harbor` resolver

**Files:**
- Modify: `cmd/at-cove/main.go` (add `atHarborBinary()`)
- Modify: `cmd/at-cove/main_test.go`

**Interfaces:**
- Produces: `func atHarborBinary() string` — path to an `at-harbor` sibling of the at-cove executable, else the bare name `"at-harbor"` (PATH). Mirrors `mint.atMintBinary`.

- [ ] **Step 1: Write the failing test.** Assert `atHarborBinary()` returns a non-empty string ending in `at-harbor` (bare-name fallback is acceptable in the test env). (A deeper sibling test mirrors any existing `atMintBinary` test; keep it light.)

- [ ] **Step 2: Run to verify it fails** (undefined).

- [ ] **Step 3: Implement.** Mirror `mint.atMintBinary`:
```go
func atHarborBinary() string {
	self, err := os.Executable()
	if err != nil {
		return "at-harbor"
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	sibling := filepath.Join(filepath.Dir(self), "at-harbor")
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return sibling
	}
	return "at-harbor"
}
```

- [ ] **Step 4: Run to verify it passes.**

- [ ] **Step 5: vet + gofmt + commit** (`feat(cove): resolve a sibling at-harbor binary`).

---

### Task 3: `harborPlan` auto-enroll branch + revoke-on-exit

**Files:**
- Modify: `cmd/at-cove/main.go` (`harborPlan`, `doChat`)
- Modify: `cmd/at-cove/main_test.go`

**Interfaces:**
- Changes: `harborPlan(...) (*connect.HarborAuth, func(), error)` — the second return is a revoke closure (nil for the pre-supplied path). `doChat` defers it after `connect.Connect`.

- [ ] **Step 1: Write the failing tests.** In `main_test.go`:
  - **auto path:** `cfg := kit.Config{Name:"k", Harbor:&kit.HarborConfig{Host:"h.example"}}` (no `Identity`), with a source-control project. Use a `runner.Fake{Outputs: []runner.FakeResult{{Stdout: "{\"id\":\"c1\",\"token\":\"TKN\"}\n"}}}`. Call `harborPlan(cfg, usersecret.Store{}, nil, "c1", "/kp", "/s.yml", f)`. Assert: returned `HarborAuth.Token == "TKN"`, `Host == "h.example"`; the recorded `enroll --json` call carries `--id c1 --role guest --destinations anthropic,git --ttl 24h` (and `--repos <project>` when source-control set); the token is **not** on argv (`!calledWith(f.Calls, "TKN")`); the returned revoke closure is non-nil, and invoking it records an `at-harbor revoke --id c1` call.
  - **manual path:** `Identity` set + a store supplying it → resolves the secret, revoke closure is **nil**, no `enroll` shelled.
  - **enroll failure:** a `runner.Fake` returning an error for the enroll `Output` → `harborPlan` returns an error (fail closed).

- [ ] **Step 2: Run to verify it fails.**

- [ ] **Step 3: Implement `harborPlan`.** Signature → `(*connect.HarborAuth, func(), error)`.
  - `cfg.Harbor == nil` → `nil, nil, nil`.
  - `cfg.Harbor.Identity != ""` → resolve the secret as today; return `auth, nil, nil`.
  - else (auto): build args `["enroll", "--json", "--id", kitName, "--role", "guest", "--destinations", "anthropic,git", "--ttl", "24h"]`, appending `"--repos", project` when `cfg.SourceControl.Repo()` yields one; `out, err := r.Output(atHarborBinary(), args...)`; on err wrap "at-harbor enroll"; `json.Unmarshal([]byte(out), &res)` into a local `struct{ID, Token string }`; empty token → error; return `&connect.HarborAuth{Host: cfg.Harbor.Host, Token: res.Token}` and a revoke closure `func() { _ = r.Run(atHarborBinary(), "revoke", "--id", res.ID) }`.
  - Use the cove instance name for `--id`: pass `kitName` = `st.Name`/container as today (doChat already passes `st.Name`).

- [ ] **Step 4: Wire `doChat`.** Capture the revoke closure:
```go
	var harborAuth *connect.HarborAuth
	var harborRevoke func()
	if cfg.Harbor != nil && !noAuth {
		if harborAuth, harborRevoke, err = harborPlan(cfg, store, expand, st.Name, kitPath, secretsPath, r); err != nil {
			return err
		}
		if harborRevoke != nil {
			defer harborRevoke()
		}
	}
```
(Keep passing `harborAuth` to `connect.Options.Harbor`.)

- [ ] **Step 5: Run tests + build.** `go build ./... && go test ./cmd/at-cove/ -count=1`.

- [ ] **Step 6: Confirm the boundary.** `go list -deps ./cmd/at-cove | grep -i oidc` → empty.

- [ ] **Step 7: vet + gofmt + commit** (`feat(cove): auto-enroll a harbor cove — mint on start, revoke on exit`).

---

### Task 4: Docs

**Files:**
- Modify: `docs/usage/at-cove-config.md` (the `harbor:` section)
- Modify: `docs/usage/INDEX.md` or `docs/OVERVIEW.md` (a pointer)

- [ ] **Step 1:** In `at-cove-config.md` `### harbor`, document the two `identity` modes: **set** → pre-supplied token; **omitted** → at-cove **auto-enrolls** (shells `at-harbor enroll` at start, `revoke` at exit) using derived defaults (role guest, destinations anthropic,git, repos = source-control project, 24h TTL). Note the requirement: the launching host needs `at-harbor` reachable to the admin API + an operator credential (`at-harbor login` / `AT_HARBOR_ADMIN_TOKEN`); otherwise set `identity`.
- [ ] **Step 2:** Add a one-line pointer to the COV-141 spec from `OVERVIEW.md`'s harbor cove sentence (or the usage INDEX).
- [ ] **Step 3:** `just test && STRICT=1 ./scripts/lint.sh`; commit (`docs: harbor cove auto-enrollment (COV-141)`).

---

## Manual verification (definition of done)

1. A `harbor:` kit **without** `identity`, launched where `at-harbor` reaches the admin API + an operator is logged in: `at-cove chat` mints an identity (visible in the enrollment list), the cove runs `claude` + `git clone` through harbor, and on exit the identity is **revoked**.
2. A `harbor:` kit **with** `identity`: behaves exactly as COV-138 (no mint/revoke).
3. No `harbor:` block: unchanged.

## Self-review

**Spec coverage:** `enroll --json` → Task 1; sibling resolver → Task 2; auto-enroll mint + revoke closure + mode selection → Task 3; docs → Task 4. Deferred (configurable scope, teammate/worker) correctly absent.

**Placeholder scan:** none — new code specified; each edit exact-at-implementation after a read.

**Type consistency:** `harborPlan` new signature `(*connect.HarborAuth, func(), error)` used in `doChat` (Task 3); `atHarborBinary()` (Task 2) called in `harborPlan` (Task 3); `enroll --json` JSON shape (Task 1) matches the local struct at-cove unmarshals (Task 3).
