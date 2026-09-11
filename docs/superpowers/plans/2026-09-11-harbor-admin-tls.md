# Harbor Admin-API TLS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve harbor's admin API over TLS and refuse to bind it off-loopback without both TLS and OIDC, so it can be reached remotely without exposing bearer tokens in cleartext.

**Architecture:** Pure config helpers (`adminTLS`, `adminUsesTLS`, `validateAdminExposure`, `isLoopbackAddr`) decide cert + exposure; `cmdServe` calls the guard before any listener starts and serves the admin handler via `ListenAndServeTLS` when TLS applies. The broker and the client are unchanged.

**Tech Stack:** Go (stdlib) + `gopkg.in/yaml.v3`. No new dependency.

## Global Constraints

- Module `github.com/aethons-tools/cove`; stdlib + `yaml.v3` + `coreos/go-oidc/v3` only — no new dependency.
- Tests hermetic (pure helpers, table-driven); real TLS admin round-trip behind `//go:build integration` / manual.
- **No regression on the loopback default:** a loopback `admin-listen` with no `admin-tls` stays plain HTTP. Reusing the broker's `tls:` must not upgrade it.
- Before each commit: `go build ./...`, `go vet ./...`, `gofmt -l internal/ cmd/` clean (run `gofmt -w` on your files; leave the pre-existing `internal/switchboard/discord_integration_test.go`).
- Cut-1/cut-2 behavior (OIDC, device-flow login, app profiles, broker) stays green.

---

### Task 1: Config — `admin-tls`, cert/TLS resolution, and the exposure guard

**Files:**
- Modify: `cmd/at-harbor/config.go`
- Modify: `cmd/at-harbor/config_test.go`

**Interfaces:**
- Produces: `serveConfig.AdminTLS{Cert,Key}`; `serveConfig.adminTLS() (cert, key string, ok bool)`; `serveConfig.adminUsesTLS() bool`; `serveConfig.validateAdminExposure() error`; `isLoopbackAddr(addr string) bool`.

- [ ] **Step 1: Write the failing tests**

In `cmd/at-harbor/config_test.go`, add:

```go
func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8081": true, "localhost:8081": true, "[::1]:8081": true,
		":8081": false, "0.0.0.0:8081": false, "10.0.0.5:8081": false, "harbor.example:8081": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestAdminTLSAndUsesTLS(t *testing.T) {
	// only top-level tls → resolves it, but loopback stays plain
	c := serveConfig{AdminListen: "127.0.0.1:8081"}
	c.TLS.Cert, c.TLS.Key = "/t.pem", "/t.key"
	if cert, key, ok := c.adminTLS(); !ok || cert != "/t.pem" || key != "/t.key" {
		t.Fatalf("adminTLS = %q,%q,%v", cert, key, ok)
	}
	if c.adminUsesTLS() {
		t.Fatal("loopback with only top-level tls must stay plain HTTP")
	}
	// explicit admin-tls opts loopback into TLS and overrides the cert
	c.AdminTLS.Cert, c.AdminTLS.Key = "/a.pem", "/a.key"
	if cert, _, _ := c.adminTLS(); cert != "/a.pem" {
		t.Fatalf("admin-tls should override: cert=%q", cert)
	}
	if !c.adminUsesTLS() {
		t.Fatal("explicit admin-tls must enable TLS on loopback")
	}
	// off-loopback always uses TLS
	if !(serveConfig{AdminListen: "0.0.0.0:8081"}).adminUsesTLS() {
		t.Fatal("off-loopback must use TLS")
	}
	// neither cert → not ok
	if _, _, ok := (serveConfig{}).adminTLS(); ok {
		t.Fatal("no cert configured must be ok=false")
	}
}

func TestValidateAdminExposure(t *testing.T) {
	withTLS := func(c *serveConfig) { c.TLS.Cert, c.TLS.Key = "/t.pem", "/t.key" }
	withOIDC := func(c *serveConfig) {
		c.OperatorAuth.OIDC = &struct {
			Issuer         string `yaml:"issuer"`
			Audience       string `yaml:"audience"`
			RequireScope   string `yaml:"require-scope"`
			DeviceClientID string `yaml:"device-client-id"`
			DeviceScope    string `yaml:"device-scope"`
		}{Issuer: "i", Audience: "a"}
	}
	// loopback: always ok, even plain + loopback-auth (today's setup)
	if err := (serveConfig{AdminListen: "127.0.0.1:8081"}).validateAdminExposure(); err != nil {
		t.Fatalf("loopback plain should pass: %v", err)
	}
	// off-loopback, both TLS + OIDC → ok
	ok := serveConfig{AdminListen: "0.0.0.0:8081"}
	withTLS(&ok)
	withOIDC(&ok)
	if err := ok.validateAdminExposure(); err != nil {
		t.Fatalf("off-loopback+TLS+OIDC should pass: %v", err)
	}
	// off-loopback missing TLS → error
	noTLS := serveConfig{AdminListen: "0.0.0.0:8081"}
	withOIDC(&noTLS)
	if err := noTLS.validateAdminExposure(); err == nil {
		t.Fatal("off-loopback without TLS must fail")
	}
	// off-loopback missing OIDC → error
	noOIDC := serveConfig{AdminListen: "0.0.0.0:8081"}
	withTLS(&noOIDC)
	if err := noOIDC.validateAdminExposure(); err == nil {
		t.Fatal("off-loopback without OIDC must fail")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/at-harbor/ -run 'TestIsLoopbackAddr|TestAdminTLS|TestValidateAdminExposure' 2>&1 | head`
Expected: FAIL — undefined `isLoopbackAddr`/`adminTLS`/`adminUsesTLS`/`validateAdminExposure`/`AdminTLS`.

- [ ] **Step 3: Implement in `cmd/at-harbor/config.go`**

Add `"fmt"` and `"net"` to the imports. Add the `AdminTLS` field to `serveConfig` (after the `TLS` block):

```go
	AdminTLS struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"admin-tls"`
```

Add the helpers:

```go
// adminTLS resolves the admin listener's cert/key: admin-tls if set, else the
// top-level tls. ok is false when neither is configured.
func (c serveConfig) adminTLS() (cert, key string, ok bool) {
	if c.AdminTLS.Cert != "" && c.AdminTLS.Key != "" {
		return c.AdminTLS.Cert, c.AdminTLS.Key, true
	}
	if c.TLS.Cert != "" && c.TLS.Key != "" {
		return c.TLS.Cert, c.TLS.Key, true
	}
	return "", "", false
}

// adminUsesTLS reports whether to serve the admin API over TLS: off-loopback
// (required by validateAdminExposure) or an explicit admin-tls block (opt-in on
// loopback). Reusing the broker's tls: does not upgrade a loopback listener.
func (c serveConfig) adminUsesTLS() bool {
	if !isLoopbackAddr(c.AdminListen) {
		return true
	}
	return c.AdminTLS.Cert != "" && c.AdminTLS.Key != ""
}

// validateAdminExposure refuses an off-loopback admin listener that lacks TLS or
// OIDC — either would expose the admin API to token interception or no real auth.
func (c serveConfig) validateAdminExposure() error {
	if c.AdminListen == "" || isLoopbackAddr(c.AdminListen) {
		return nil
	}
	if _, _, ok := c.adminTLS(); !ok {
		return fmt.Errorf("admin-listen %q is off-loopback but no TLS is configured (set `tls` or `admin-tls`)", c.AdminListen)
	}
	if c.OperatorAuth.OIDC == nil {
		return fmt.Errorf("admin-listen %q is off-loopback but operator auth is loopback-only (configure `operator-auth.oidc`)", c.AdminListen)
	}
	return nil
}

// isLoopbackAddr reports whether a host:port listen address binds only loopback.
// Empty host, 0.0.0.0 and :: are all-interfaces (non-loopback).
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd/at-harbor/ -run 'TestIsLoopbackAddr|TestAdminTLS|TestValidateAdminExposure' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./cmd/at-harbor/ && gofmt -l cmd/at-harbor/
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go
git commit -m "feat(harbor): admin-tls config + cert resolution + off-loopback exposure guard"
```

---

### Task 2: `cmdServe` — enforce the guard + serve admin over TLS; docs

**Files:**
- Modify: `cmd/at-harbor/main.go` (`cmdServe`)
- Modify: `docs/OVERVIEW.md`, `docs/usage/INDEX.md`

**Interfaces:**
- Consumes: `serveConfig.validateAdminExposure`, `adminUsesTLS`, `adminTLS` (Task 1).

- [ ] **Step 1: Enforce the guard early**

In `cmd/at-harbor/main.go` `cmdServe`, right after the `parseServeConfig` error check, add:

```go
	if err := cfg.validateAdminExposure(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
```

- [ ] **Step 2: Serve the admin API over TLS when applicable**

Replace the admin listener goroutine (the `go func() { … http.ListenAndServe(cfg.AdminListen, admin) … }()`) with:

```go
		admin := harbor.NewAdminHandler(st, auth, credExists, cfg.operatorLoginConfig(), log)
		go func() {
			if cfg.adminUsesTLS() {
				cert, key, _ := cfg.adminTLS()
				log.Info("harbor admin API listening (TLS)", "addr", cfg.AdminListen)
				if err := (&http.Server{Addr: cfg.AdminListen, Handler: admin}).ListenAndServeTLS(cert, key); err != nil {
					log.Error("admin API stopped", "err", err.Error())
				}
				return
			}
			log.Info("harbor admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, admin); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
```

(Keep the `credExists`/`admin` lines that already precede the goroutine; only the goroutine body changes.)

- [ ] **Step 3: Build + full tests**

Run: `go build ./... && go test ./... -count=1`
Expected: builds; all pass (no regression — loopback admin still plain HTTP).

- [ ] **Step 4: Docs**

`docs/OVERVIEW.md` — in the at-harbor paragraph, update the safety note: the admin API now serves **TLS** and refuses to bind off-loopback without TLS + OIDC; loopback stays plain HTTP; optional `admin-tls` overrides the broker's cert. Drop the "off-loopback exposure awaits TLS" caveat.

`docs/usage/INDEX.md` — add a row after the operator-login row:

```markdown
| [harbor admin-API TLS](../superpowers/specs/2026-09-11-harbor-admin-tls-design.md) | The admin API serves TLS (cert from `admin-tls`, else the broker's `tls:`) and **refuses to bind off-loopback** unless both TLS and `operator-auth.oidc` are set; loopback stays plain HTTP. Clients use `https://` admin-urls (system trust store). | You are exposing harbor's admin API beyond loopback, or configuring `admin-tls`. |
```

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./... && gofmt -l internal/ cmd/    # (ignore the pre-existing discord_integration_test.go)
git add cmd/at-harbor/main.go docs/OVERVIEW.md docs/usage/INDEX.md
git commit -m "feat(harbor): serve admin API over TLS + enforce off-loopback guard in serve"
```

---

## Manual verification (definition of done)

1. Loopback config (today's) starts and works over plain HTTP — no regression.
2. `admin-listen` off-loopback with `tls:` + `operator-auth.oidc` → starts, logs "listening (TLS)".
3. `admin-listen` off-loopback without TLS → refuses to start (clear error); likewise without `operator-auth.oidc`.
4. `curl https://<host>:8081/admin/login-config` works (system-trusted cert); `at-harbor destination list --admin-url https://<host>:8081` (logged in) works.

## Self-review

**Spec coverage:** `admin-tls` + cert resolution → Task 1 (`adminTLS`); exposure guard → Task 1 (`validateAdminExposure`, `isLoopbackAddr`); loopback-plain default preserved → Task 1 (`adminUsesTLS`) + Task 2; TLS serving → Task 2; guard enforced before listeners → Task 2 Step 1; client unchanged (system roots) → no task needed; docs → Task 2. Deferred (custom CA/`--insecure`, mTLS, ACME, at-harborctl) correctly absent.

**Placeholder scan:** none — exact code/edits throughout.

**Type consistency:** `adminTLS`/`adminUsesTLS`/`validateAdminExposure`/`isLoopbackAddr` signatures match between Task 1 definitions and Task 2 calls; the `OperatorAuth.OIDC` anonymous-struct literal in the test mirrors `config.go` exactly.
