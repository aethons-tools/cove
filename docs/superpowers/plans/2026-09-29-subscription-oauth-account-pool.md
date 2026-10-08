# Subscription-OAuth Account Pool — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Credential cove `claude` from a broker-owned pool of Anthropic subscription-OAuth accounts (identity → one account for life, broker-owned refresh) instead of per-cove federated service-account tokens.

**Architecture:** Reuse the broker's authenticate → `Decide` → resolve → inject → proxy pipeline unchanged. The cove is seeded a dummy `claudeAiOauth` credentials file whose `accessToken` **is** its Jam identity token; subscription `claude` sends it as `Authorization: Bearer …`. The `anthropic` destination flips `IdentityIn` to `bearer`, so `presentedToken` reads the identity there. A new `Pool` (file-backed) binds identity → account and returns the account's current access token via an additive `IdentityCredResolver` capability the broker prefers. A background refresher rotates each account's real token ahead of expiry.

**Tech Stack:** Go (stdlib + existing `internal/jam`, `internal/connect`, `internal/jam/snippet`), hermetic tests driving fakes (`runner.Fake`, `httptest`), `just test`.

**Spec:** `docs/superpowers/specs/2026-09-29-subscription-oauth-account-pool-design.md`

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Build/test via `just` (`just test` = hermetic unit tests, `just build`, `just lint`).
- **TDD, in-package, flat layout.** New pool code lives in `internal/jam` (flat, alongside `proxy.go`/`creds.go`), matching the existing package. Write the failing test first.
- **Hermetic tests only.** No Docker, network, or live VM. Drive `runner.Fake` and `httptest`. Real-ssh tests go behind the `integration` build tag (not needed here).
- **Secrets never hit disk (beyond the store), argv, or logs.** Token values (`AccessToken`, `RefreshToken`) are never logged at any level. The dummy seed goes into the cove over SSH stdin, never argv. Persist the store atomically (write-temp + `os.Rename`), file mode `0600`.
- **Additive & flag-gated.** With no `pool:` config block, behavior is unchanged (anthropic destination stays `x-api-key` + `SecretResolver`). Nothing regresses until an operator seeds a pool and flips the destination.
- **Preserve the hardening/egress boundary.** The refresher's OAuth calls are Jam's *own* host egress (add the token endpoint host to Jam's allow-list), never a cove's; coves still reach only `<jam>/anthropic`.
- **Commit message footer** (every commit):
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```
- **Docs in the same change.** Route each task's doc update to the doc that owns the subject (start `docs/OVERVIEW.md`; managed-cove credentialing lives in `docs/usage/jam/coves.md`, connector env in the snippet/serve docs). No task is complete until docs reflect it.

---

## Task 0: Probe the subscription refresh endpoint (spike — blocks Task 3) — ✅ DONE (2026-09-29)

**Result (recorded in the spec, "Refresh endpoint (probed)"):**
- Endpoint: `POST https://platform.claude.com/v1/oauth/token` — a **fixed host**, not `ANTHROPIC_BASE_URL`; must be on Jam's own egress allow-list.
- Encoding: **JSON** (`Content-Type: application/json`).
- Body: `{"grant_type":"refresh_token","refresh_token":"…","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e","scope":"user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"}`.
- Response: standard `{access_token, refresh_token, expires_in}`.

Task 3's code below is already corrected to these values (JSON body, concrete endpoint/client_id/scope defaults). The original spike procedure is retained for reference:

Not a code task. A spike whose deliverable is the recorded refresh-request shape that Task 3 hard-codes. The inference-request probe is already done (recorded in the spec); this one captures the **refresh** request.

**Deliverable:** append a "Refresh endpoint (probed)" note to the spec with: the exact token-endpoint URL, HTTP method, request body (`grant_type=refresh_token`, param names), the public `client_id` value `claude` uses, and the response field names for the new tokens + expiry.

- [ ] **Step 1: Seed a throwaway config dir with a PAST-expiry dummy credential.** In the scratchpad, `mktemp -d`, write `.credentials.json` with `claudeAiOauth.{accessToken, refreshToken}` fabricated and `expiresAt` set to a past epoch-ms so `claude` decides it must refresh.

- [ ] **Step 2: Point `claude` at a local logger and run it.** Start a local HTTP server that logs method + path + headers + body and returns a canned token-ish JSON. Run `CLAUDE_CONFIG_DIR=<tmp> ANTHROPIC_BASE_URL=http://127.0.0.1:<port> claude -p "ping"`. Observe whether the refresh goes to the base URL or a fixed OAuth host (e.g. `console.anthropic.com` / `claude.ai`); capture the request.

Expected: a refresh `POST` (form or JSON) with `grant_type=refresh_token`, the `refresh_token`, and a `client_id`. Note the host — it decides whether the refresher targets a fixed OAuth host (likely) rather than the broker.

- [ ] **Step 3: Record the findings in the spec** under a new "Refresh endpoint (probed)" subsection (URL, method, body params, client_id, response fields). Commit the spec update.

```bash
git add docs/superpowers/specs/2026-09-29-subscription-oauth-account-pool-design.md
git commit -m "spec: record probed subscription refresh endpoint + client_id"
```

> **If the refresh host is NOT `api.anthropic.com`:** it must be added to Jam's own egress allow-list (host-side), and the refresher dials it directly. Note this in the serve wiring task (Task 5).

---

## Task 1: The pool store + `Pool` (bind + select + read tokens)

**Files:**
- Create: `internal/jam/pool.go`
- Test: `internal/jam/pool_test.go`

**Interfaces:**
- Consumes: nothing (leaf).
- Produces:
  - `type PoolAccount struct { Name string; AccessToken string; RefreshToken string; ExpiresAt time.Time }`
  - `type PoolStore interface { Accounts() ([]PoolAccount, error); SetAccount(PoolAccount) error; Bindings() (map[string]string, error); Bind(identityHash, accountName string) error }`
  - `func NewFilePoolStore(path string) (*FilePoolStore, error)` — implements `PoolStore`, atomic + mutexed + `0600`.
  - `type Pool struct { ... }`; `func NewPool(store PoolStore) *Pool`
  - `func (p *Pool) TokenFor(identityHash string) (string, error)` — binds a least-loaded account on first use, returns that account's current `AccessToken`.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/jam/pool_test.go
package jam

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestPool(t *testing.T, accts ...PoolAccount) *Pool {
	t.Helper()
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatalf("NewFilePoolStore: %v", err)
	}
	for _, a := range accts {
		if err := st.SetAccount(a); err != nil {
			t.Fatalf("SetAccount: %v", err)
		}
	}
	return NewPool(st)
}

func TestTokenForBindsOnceAndIsStable(t *testing.T) {
	far := time.Now().Add(time.Hour)
	p := newTestPool(t, PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: far})
	got1, err := p.TokenFor("id-hash-1")
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	got2, _ := p.TokenFor("id-hash-1")
	if got1 != "tok-a" || got2 != "tok-a" {
		t.Fatalf("want tok-a twice, got %q then %q", got1, got2)
	}
}

func TestTokenForSpreadsAcrossAccounts(t *testing.T) {
	far := time.Now().Add(time.Hour)
	p := newTestPool(t,
		PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: far},
		PoolAccount{Name: "b", AccessToken: "tok-b", ExpiresAt: far},
	)
	seen := map[string]bool{}
	seen[must(t, p.TokenFor("id-1"))] = true
	seen[must(t, p.TokenFor("id-2"))] = true
	if !seen["tok-a"] || !seen["tok-b"] {
		t.Fatalf("two identities should spread across both accounts, saw %v", seen)
	}
}

func TestTokenForReflectsRotatedToken(t *testing.T) {
	far := time.Now().Add(time.Hour)
	st, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", ExpiresAt: far})
	p := NewPool(st)
	_, _ = p.TokenFor("id-1") // bind to "a"
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "new", ExpiresAt: far})
	if got := must(t, p.TokenFor("id-1")); got != "new" {
		t.Fatalf("want rotated token new, got %q", got)
	}
}

func TestTokenForNoAccounts(t *testing.T) {
	p := newTestPool(t)
	if _, err := p.TokenFor("id-1"); err == nil {
		t.Fatal("want error when no accounts configured")
	}
}

func TestFilePoolStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	st, _ := NewFilePoolStore(path)
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: time.Now().Add(time.Hour)})
	_ = st.Bind("id-1", "a")
	st2, err := NewFilePoolStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	b, _ := st2.Bindings()
	if b["id-1"] != "a" {
		t.Fatalf("binding not persisted: %v", b)
	}
}

func must(t *testing.T, s string, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return s
}
```

- [ ] **Step 2: Run to verify failure.**

Run: `go test ./internal/jam/ -run TestTokenFor -v`
Expected: FAIL — `undefined: PoolAccount` / `NewFilePoolStore` / `NewPool`.

- [ ] **Step 3: Implement `pool.go`.**

```go
// internal/jam/pool.go
package jam

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// PoolAccount is one Anthropic subscription-OAuth account the broker draws from.
// AccessToken/RefreshToken are secrets: never log them.
type PoolAccount struct {
	Name         string    `json:"name"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// PoolStore persists account definitions and identity→account bindings. All
// methods are safe for concurrent use.
type PoolStore interface {
	Accounts() ([]PoolAccount, error)
	SetAccount(PoolAccount) error            // upsert by Name (the refresher rotates tokens here)
	Bindings() (map[string]string, error)    // identityHash → accountName
	Bind(identityHash, accountName string) error
}

// Pool binds each identity to one account for life and returns that account's
// current access token. Selection spreads new identities across the least-loaded
// accounts. It never refreshes inline — that is the refresher's job.
type Pool struct {
	store PoolStore
	mu    sync.Mutex // serializes bind-or-select so a first-use never double-binds
}

func NewPool(store PoolStore) *Pool { return &Pool{store: store} }

// TokenFor returns the current access token for the account bound to
// identityHash, binding a least-loaded account on first use.
func (p *Pool) TokenFor(identityHash string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	accts, err := p.store.Accounts()
	if err != nil {
		return "", err
	}
	if len(accts) == 0 {
		return "", fmt.Errorf("pool: no accounts configured")
	}
	byName := make(map[string]PoolAccount, len(accts))
	for _, a := range accts {
		byName[a.Name] = a
	}
	bindings, err := p.store.Bindings()
	if err != nil {
		return "", err
	}
	if name, ok := bindings[identityHash]; ok {
		a, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("pool: bound account %q no longer exists", name)
		}
		return a.AccessToken, nil
	}
	chosen := leastLoaded(accts, bindings)
	if err := p.store.Bind(identityHash, chosen); err != nil {
		return "", err
	}
	return byName[chosen].AccessToken, nil
}

// leastLoaded returns the name of the account with the fewest bindings, breaking
// ties by name for determinism.
func leastLoaded(accts []PoolAccount, bindings map[string]string) string {
	counts := make(map[string]int, len(accts))
	for _, a := range accts {
		counts[a.Name] = 0
	}
	for _, name := range bindings {
		if _, ok := counts[name]; ok {
			counts[name]++
		}
	}
	names := make([]string, 0, len(accts))
	for _, a := range accts {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	best := names[0]
	for _, n := range names {
		if counts[n] < counts[best] {
			best = n
		}
	}
	return best
}

// FilePoolStore is a JSON-file-backed PoolStore: atomic writes, 0600, mutexed.
type FilePoolStore struct {
	path string
	mu   sync.Mutex
}

type poolFile struct {
	Accounts []PoolAccount     `json:"accounts"`
	Bindings map[string]string `json:"bindings"`
}

func NewFilePoolStore(path string) (*FilePoolStore, error) {
	s := &FilePoolStore{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := s.write(poolFile{Bindings: map[string]string{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FilePoolStore) read() (poolFile, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return poolFile{}, err
	}
	var pf poolFile
	if len(b) > 0 {
		if err := json.Unmarshal(b, &pf); err != nil {
			return poolFile{}, err
		}
	}
	if pf.Bindings == nil {
		pf.Bindings = map[string]string{}
	}
	return pf, nil
}

func (s *FilePoolStore) write(pf poolFile) error {
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FilePoolStore) Accounts() ([]PoolAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	return pf.Accounts, err
}

func (s *FilePoolStore) SetAccount(a PoolAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return err
	}
	for i := range pf.Accounts {
		if pf.Accounts[i].Name == a.Name {
			pf.Accounts[i] = a
			return s.write(pf)
		}
	}
	pf.Accounts = append(pf.Accounts, a)
	return s.write(pf)
}

func (s *FilePoolStore) Bindings() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(pf.Bindings))
	for k, v := range pf.Bindings {
		out[k] = v
	}
	return out, nil
}

func (s *FilePoolStore) Bind(identityHash, accountName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return err
	}
	pf.Bindings[identityHash] = accountName
	return s.write(pf)
}
```

- [ ] **Step 4: Run tests + race detector.**

Run: `go test ./internal/jam/ -run 'TestTokenFor|TestFilePoolStore' -race -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/jam/pool.go internal/jam/pool_test.go
git commit -m "feat(jam): subscription account pool store + bind-once selection

<footer>"
```

---

## Task 2: `IdentityCredResolver` + broker capability check + `ChainResolver`

**Files:**
- Modify: `internal/jam/creds.go` (add the interface + `ChainResolver`)
- Modify: `internal/jam/proxy.go:60-66` (prefer the identity-aware resolver)
- Test: `internal/jam/creds_test.go`, `internal/jam/proxy_test.go`

**Interfaces:**
- Consumes: `Pool.TokenFor(identityHash)` (Task 1); `HashToken` (existing); `CredResolver` (existing).
- Produces:
  - `type IdentityCredResolver interface { ResolveFor(name, identityHash string) (string, error) }`
  - `type ChainResolver struct { ... }`; `func NewChainResolver(base CredResolver, pool *Pool, poolCred string) *ChainResolver` — implements both `CredResolver` and `IdentityCredResolver`.

- [ ] **Step 1: Write the failing resolver tests.**

```go
// internal/jam/creds_test.go  (add to the existing file)

type stubBase struct{ vals map[string]string }

func (s stubBase) Resolve(name string) (string, error) {
	if v, ok := s.vals[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("no cred %q", name)
}

func TestChainResolverRoutesPoolCredToPoolByIdentity(t *testing.T) {
	far := time.Now().Add(time.Hour)
	st, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "real-token", ExpiresAt: far})
	cr := NewChainResolver(stubBase{vals: map[string]string{"git": "PAT"}}, NewPool(st), "anthropic-sub")

	got, err := cr.ResolveFor("anthropic-sub", HashToken("cove-identity"))
	if err != nil || got != "real-token" {
		t.Fatalf("pool cred: got %q err %v, want real-token", got, err)
	}
	// Non-pool creds delegate to the base (identity ignored).
	if got, _ := cr.ResolveFor("git", "whatever"); got != "PAT" {
		t.Fatalf("git cred should delegate to base, got %q", got)
	}
}

func TestChainResolverBarePoolResolveFailsClosed(t *testing.T) {
	st, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	cr := NewChainResolver(stubBase{vals: map[string]string{}}, NewPool(st), "anthropic-sub")
	if _, err := cr.Resolve("anthropic-sub"); err == nil {
		t.Fatal("bare Resolve of a pool cred must fail (identity required)")
	}
}
```

- [ ] **Step 2: Run to verify failure.**

Run: `go test ./internal/jam/ -run TestChainResolver -v`
Expected: FAIL — `undefined: NewChainResolver`.

- [ ] **Step 3: Implement in `creds.go`.**

```go
// internal/jam/creds.go  (append)

// IdentityCredResolver is an optional CredResolver that resolves a credential
// scoped to the requesting identity. The broker prefers it when the resolver
// implements it (the subscription pool: the token depends on the identity's
// bound account). Implementations must never log or persist the value.
type IdentityCredResolver interface {
	ResolveFor(name, identityHash string) (string, error)
}

// ChainResolver routes one configured pool credential name to a Pool (by
// identity) and delegates every other name to a base CredResolver. It satisfies
// both CredResolver and IdentityCredResolver.
type ChainResolver struct {
	base     CredResolver
	pool     *Pool
	poolCred string
}

func NewChainResolver(base CredResolver, pool *Pool, poolCred string) *ChainResolver {
	return &ChainResolver{base: base, pool: pool, poolCred: poolCred}
}

func (c *ChainResolver) Resolve(name string) (string, error) {
	if name == c.poolCred {
		return "", fmt.Errorf("credential %q is identity-scoped (pool); no identity presented", name)
	}
	return c.base.Resolve(name)
}

func (c *ChainResolver) ResolveFor(name, identityHash string) (string, error) {
	if name == c.poolCred {
		return c.pool.TokenFor(identityHash)
	}
	return c.base.Resolve(name)
}
```

(Add `"fmt"` to `creds.go` imports if not present — it already imports `fmt`.)

- [ ] **Step 4: Wire the capability check into `proxy.go`.** Replace the credential-resolution block (currently lines ~60-66):

```go
	// current:
	//   var cred string
	//   if dec.NeedCred {
	//       if cred, err = b.creds.Resolve(dec.CredName); err != nil { ... }
	//   }
	var cred string
	if dec.NeedCred {
		if ir, ok := b.creds.(IdentityCredResolver); ok {
			cred, err = ir.ResolveFor(dec.CredName, HashToken(tok))
		} else {
			cred, err = b.creds.Resolve(dec.CredName)
		}
		if err != nil {
			b.log.Error("credential resolve failed", "destination", dest.Name)
			http.Error(w, "credential unavailable", http.StatusBadGateway)
			return
		}
	}
```

- [ ] **Step 5: Write a broker integration test proving bearer-identity → pool token + preserved `anthropic-beta`.** Follow the existing `proxy_test.go` harness (an `httptest.Server` upstream that echoes the received `Authorization` and `anthropic-beta`, a store stub with one actor authorized for the `anthropic` destination, a `ChainResolver` over a one-account pool). Assert the upstream saw `Authorization: Bearer real-token` and the original `anthropic-beta` header survived.

```go
func TestBrokerInjectsPoolTokenForBearerIdentity(t *testing.T) {
	var gotAuth, gotBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	far := time.Now().Add(time.Hour)
	ps, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	_ = ps.SetAccount(PoolAccount{Name: "a", AccessToken: "real-token", ExpiresAt: far})
	creds := NewChainResolver(stubBase{vals: map[string]string{}}, NewPool(ps), "anthropic-sub")

	// A store fixture whose single actor is authorized for a bearer-identity
	// anthropic destination with CredName "anthropic-sub". (Reuse the existing
	// proxy_test store builder; set dest.IdentityIn=ApplyBearer, Apply=ApplyBearer,
	// CredName="anthropic-sub".)
	st := brokerTestStore(t, Destination{
		Name: "anthropic", Route: "/anthropic/", Upstream: upstream.URL,
		IdentityIn: ApplyBearer, Apply: ApplyBearer, CredName: "anthropic-sub",
	}, "cove-identity" /* identity token the fixture actor holds */)

	b := NewBroker(st, creds, testLogger())
	req := httptest.NewRequest("POST", "/anthropic/v1/messages?beta=true", nil)
	req.Header.Set("Authorization", "Bearer cove-identity")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if gotAuth != "Bearer real-token" {
		t.Fatalf("upstream Authorization = %q, want Bearer real-token", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta not preserved: %q", gotBeta)
	}
}
```

> `brokerTestStore`/`testLogger` are the existing proxy_test helpers — if their names differ, adapt to what `proxy_test.go` already provides rather than adding new ones.

- [ ] **Step 6: Run.**

Run: `go test ./internal/jam/ -run 'TestChainResolver|TestBrokerInjectsPool' -race -v`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/jam/creds.go internal/jam/creds_test.go internal/jam/proxy.go internal/jam/proxy_test.go
git commit -m "feat(jam): identity-aware cred resolution for the subscription pool

<footer>"
```

---

## Task 3: The broker-owned refresher

> **Prerequisite:** Task 0's recorded endpoint/client_id. Fill the two constants below from that finding before implementing.

**Files:**
- Create: `internal/jam/refresher.go`
- Test: `internal/jam/refresher_test.go`

**Interfaces:**
- Consumes: `PoolStore` (Task 1). Uses `net/http` against the probed token endpoint.
- Produces:
  - `type Refresher struct { ... }`
  - `func NewRefresher(store PoolStore, opts RefresherOptions) *Refresher` with `RefresherOptions{ HTTPClient *http.Client; TokenURL string; ClientID string; Scope string; Now func() time.Time; Margin time.Duration; Log *slog.Logger }`. Probed defaults: `TokenURL = https://platform.claude.com/v1/oauth/token`, `ClientID = 9d1c250a-e61b-44d9-88ed-5944d1962f5e`, `Scope = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"`.
  - `func (r *Refresher) RefreshDue(ctx context.Context) error` — refresh every account within `Margin` of expiry (the unit of work; the loop just calls this on a ticker).
  - `func (r *Refresher) Run(ctx context.Context, interval time.Duration)` — ticker loop calling `RefreshDue`.

- [ ] **Step 1: Write the failing test** (fake clock + `httptest` token endpoint):

```go
// internal/jam/refresher_test.go
package jam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshDueRotatesTokenWithinMargin(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var gotGrant, gotRefresh, gotClient, gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			ClientID     string `json:"client_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotGrant, gotRefresh, gotClient = body.GrantType, body.RefreshToken, body.ClientID
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600,
		})
	}))
	defer ts.Close()

	st, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	// expires in 5m; margin 15m ⇒ due.
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: now.Add(5 * time.Minute)})

	r := NewRefresher(st, RefresherOptions{
		HTTPClient: ts.Client(), TokenURL: ts.URL, ClientID: "test-client",
		Now: func() time.Time { return now }, Margin: 15 * time.Minute, Log: testLogger(),
	})
	if err := r.RefreshDue(context.Background()); err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	if gotGrant != "refresh_token" || gotRefresh != "old-refresh" || gotClient != "test-client" {
		t.Fatalf("request shape: grant=%q refresh=%q client=%q", gotGrant, gotRefresh, gotClient)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}
	accts, _ := st.Accounts()
	if accts[0].AccessToken != "new-access" || accts[0].RefreshToken != "new-refresh" {
		t.Fatalf("tokens not rotated: %+v", accts[0])
	}
	if !accts[0].ExpiresAt.Equal(now.Add(3600 * time.Second)) {
		t.Fatalf("expiry not advanced: %v", accts[0].ExpiresAt)
	}
}

func TestRefreshDueSkipsFarFromExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer ts.Close()
	st, _ := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	_ = st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", RefreshToken: "r", ExpiresAt: now.Add(2 * time.Hour)})
	r := NewRefresher(st, RefresherOptions{HTTPClient: ts.Client(), TokenURL: ts.URL, ClientID: "c", Now: func() time.Time { return now }, Margin: 15 * time.Minute, Log: testLogger()})
	_ = r.RefreshDue(context.Background())
	if called {
		t.Fatal("account far from expiry must not be refreshed")
	}
}
```

- [ ] **Step 2: Run to verify failure.**

Run: `go test ./internal/jam/ -run TestRefreshDue -v`
Expected: FAIL — `undefined: NewRefresher`.

- [ ] **Step 3: Implement `refresher.go`** (endpoint/client_id/scope + JSON encoding are the Task 0 probed values):

```go
// internal/jam/refresher.go
package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Probed 2026-09-29 (see the spec's "Refresh endpoint" section).
const (
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultScope    = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
)

type RefresherOptions struct {
	HTTPClient *http.Client
	TokenURL   string // default defaultTokenURL
	ClientID   string // default defaultClientID
	Scope      string // default defaultScope
	Now        func() time.Time
	Margin     time.Duration // refresh when ExpiresAt is within this of Now
	Log        *slog.Logger
}

type Refresher struct {
	store PoolStore
	opt   RefresherOptions
}

func NewRefresher(store PoolStore, opt RefresherOptions) *Refresher {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = http.DefaultClient
	}
	if opt.Margin == 0 {
		opt.Margin = 15 * time.Minute
	}
	if opt.TokenURL == "" {
		opt.TokenURL = defaultTokenURL
	}
	if opt.ClientID == "" {
		opt.ClientID = defaultClientID
	}
	if opt.Scope == "" {
		opt.Scope = defaultScope
	}
	return &Refresher{store: store, opt: opt}
}

// RefreshDue refreshes every account whose ExpiresAt is within Margin of Now.
// Per-account failures are logged and do not stop the others. Never logs tokens.
func (r *Refresher) RefreshDue(ctx context.Context) error {
	accts, err := r.store.Accounts()
	if err != nil {
		return err
	}
	deadline := r.opt.Now().Add(r.opt.Margin)
	for _, a := range accts {
		if a.ExpiresAt.After(deadline) {
			continue
		}
		if err := r.refreshOne(ctx, a); err != nil {
			r.opt.Log.Warn("pool token refresh failed", "account", a.Name, "error", err.Error())
		}
	}
	return nil
}

func (r *Refresher) refreshOne(ctx context.Context, a PoolAccount) error {
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": a.RefreshToken,
		"client_id":     r.opt.ClientID,
		"scope":         r.opt.Scope,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opt.TokenURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.opt.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint status %d", resp.StatusCode) // body not logged (may echo secrets)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if body.AccessToken == "" {
		return fmt.Errorf("token endpoint returned no access_token")
	}
	a.AccessToken = body.AccessToken
	if body.RefreshToken != "" { // some providers omit a new refresh token
		a.RefreshToken = body.RefreshToken
	}
	if body.ExpiresIn > 0 {
		a.ExpiresAt = r.opt.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return r.store.SetAccount(a)
}

// Run refreshes on a ticker until ctx is done.
func (r *Refresher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.RefreshDue(ctx); err != nil {
			r.opt.Log.Warn("pool refresh pass failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 4: Run.**

Run: `go test ./internal/jam/ -run TestRefreshDue -race -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/jam/refresher.go internal/jam/refresher_test.go
git commit -m "feat(jam): broker-owned background refresher for pool tokens

<footer>"
```

---

## Task 4: Snippet subscription variant + cove seed

**Files:**
- Modify: `internal/jam/snippet/snippet.go` (add a subscription renderer producing the dummy credentials JSON)
- Modify: `internal/connect/covemaster.go` (write the credentials file into the cove; drop `ANTHROPIC_API_KEY` in subscription mode)
- Test: `internal/jam/snippet/snippet_test.go`, `internal/connect/covemaster_test.go` (if present; else a snippet-level test)

**Interfaces:**
- Consumes: the identity token (already passed to `snippet.Render` / `LaunchCoveMaster`).
- Produces:
  - `func DummyCredentials(identityToken string, farFuture time.Time) (string, error)` — returns the `claudeAiOauth` JSON with `accessToken == identityToken`.
  - `func RenderSubscription(baseURL, token string) string` — like `Render` but **without** `ANTHROPIC_API_KEY` (sets `ANTHROPIC_BASE_URL` + the identity env + gitconfig only).
  - `CoveMasterOptions` gains `Subscription bool`; when set, `LaunchCoveMaster` stages the dummy credentials file at the cove's `CLAUDE_CONFIG_DIR/.credentials.json` and uses `RenderSubscription`.

- [ ] **Step 1: Write the failing snippet tests.**

```go
// internal/jam/snippet/snippet_test.go (add)

func TestDummyCredentialsUsesIdentityAsAccessToken(t *testing.T) {
	far := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := DummyCredentials("TOK123", far)
	if err != nil {
		t.Fatalf("DummyCredentials: %v", err)
	}
	var parsed struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.ClaudeAiOauth.AccessToken != "TOK123" {
		t.Fatalf("accessToken = %q, want the identity token", parsed.ClaudeAiOauth.AccessToken)
	}
	if parsed.ClaudeAiOauth.ExpiresAt != far.UnixMilli() {
		t.Fatalf("expiresAt = %d, want far-future ms %d", parsed.ClaudeAiOauth.ExpiresAt, far.UnixMilli())
	}
}

func TestRenderSubscriptionOmitsAPIKey(t *testing.T) {
	out := RenderSubscription("https://jam.local", "TOK123")
	if strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Fatalf("subscription render must NOT set ANTHROPIC_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://jam.local/anthropic") {
		t.Fatalf("missing base URL:\n%s", out)
	}
	if !strings.Contains(out, "export AT_JAM_IDENTITY_TOKEN=TOK123") {
		t.Fatalf("missing identity token export:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure.**

Run: `go test ./internal/jam/snippet/ -run 'TestDummyCredentials|TestRenderSubscription' -v`
Expected: FAIL — `undefined: DummyCredentials` / `RenderSubscription`.

- [ ] **Step 3: Implement in `snippet.go`.**

```go
// snippet.go (add; snippet stays stdlib-only)
import "encoding/json" // add to imports
import "time"          // add to imports

// DummyCredentials returns the well-known dummy claudeAiOauth credentials file
// seeded into a subscription-mode cove. accessToken IS the identity token (that
// is what reaches the broker as the OAuth bearer). Far-future expiry so claude
// never attempts a refresh (the broker owns refresh).
func DummyCredentials(identityToken string, farFuture time.Time) (string, error) {
	ms := farFuture.UnixMilli()
	doc := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":           identityToken,
			"refreshToken":          "jam-dummy-refresh",
			"expiresAt":             ms,
			"refreshTokenExpiresAt": ms,
			"scopes":                []string{"user:inference", "user:profile"},
			"subscriptionType":      "pro",
			"rateLimitTier":         "default",
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RenderSubscription is Render without ANTHROPIC_API_KEY: subscription-mode
// claude authenticates from the seeded .credentials.json, so setting the API key
// (which forces API-key mode) must be avoided.
func RenderSubscription(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "export %s=%s\n", tokenVar, token)
	fmt.Fprintf(&b, "export %s=\"$%s\"\n", legacyTokenVar, tokenVar)
	fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%s%s\n", baseURL, anthropicPath)
	b.WriteString(GitConfig(baseURL))
	return b.String()
}
```

- [ ] **Step 4: Stage the credentials file in `LaunchCoveMaster`.** Add `Subscription bool` to `CoveMasterOptions`. When set, write the dummy credentials to the cove's config dir over SSH stdin (reuse `writeVM`), and use `RenderSubscription` instead of `Render`. Path: `CLAUDE_CONFIG_DIR/.credentials.json` — the cove's `CLAUDE_CONFIG_DIR` is `/agent-data` (per the probe), so `/agent-data/.credentials.json`. Add a `credsVMPath = "/agent-data/.credentials.json"` const (or reuse `connect.credsVMPath` if exported).

```go
// covemaster.go — inside LaunchCoveMaster, replace the snippet.Render line:
	if o.Subscription {
		far := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		creds, err := snippet.DummyCredentials(o.IdentityToken, far)
		if err != nil {
			return fmt.Errorf("cove-master dummy credentials: %w", err)
		}
		if err := writeVM(r, o.Target, creds, coveMasterCredsVMPath); err != nil {
			return fmt.Errorf("cove-master credentials: %w", err)
		}
		script.WriteString(snippet.RenderSubscription("https://"+o.JamHost, o.IdentityToken))
	} else {
		script.WriteString(snippet.Render("https://"+o.JamHost, o.IdentityToken))
	}
// where const coveMasterCredsVMPath = "/agent-data/.credentials.json"
```

Add a test asserting that with `Subscription: true`, the runner recorded a `writeVM` of the credentials file to `/agent-data/.credentials.json` and the env script contains no `ANTHROPIC_API_KEY` (drive `runner.Fake`, mirror the existing covemaster test if one exists; otherwise assert at the snippet level and leave the SSH staging to Task 5's integration check).

- [ ] **Step 5: Run.**

Run: `go test ./internal/jam/snippet/ ./internal/connect/ -run 'Subscription|DummyCredentials' -v`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/jam/snippet/ internal/connect/covemaster.go internal/connect/covemaster_test.go
git commit -m "feat: subscription-mode cove seed (dummy claudeAiOauth = identity, no API key)

<footer>"
```

---

## Task 5: Serve wiring — `pool:` config, construction, `at-jam pool add`, destination flip, docs

**Files:**
- Modify: `cmd/at-jam/config.go` (a `pool:` serve-config block + validation)
- Modify: `cmd/at-jam/main.go` (construct `FilePoolStore` + `Pool` + `ChainResolver`, start the `Refresher`, wire `Subscription` into the launcher; add the `pool` verb)
- Modify: `internal/jam/launcher/launcher.go` + `internal/connect/covemaster.go` call site (pass `Subscription` through from config)
- Modify: `docs/usage/jam/coves.md` (credentialing section) + `docs/OVERVIEW.md` link if needed
- Test: `cmd/at-jam/config_test.go`

**Interfaces:**
- Consumes: `NewFilePoolStore`, `NewPool`, `NewChainResolver`, `NewRefresher` (Tasks 1-3); `Subscription` option (Task 4).
- Produces: a `poolConfig` YAML block; `at-jam pool add --name <n> [--from-file <f>]` (reads the `claudeAiOauth` token block, never argv).

- [ ] **Step 1: Write the failing config test.**

```go
// cmd/at-jam/config_test.go (add)
func TestServeConfigParsesPoolBlock(t *testing.T) {
	y := `
pool:
  store: /var/lib/jam/pool.json
  cred-name: anthropic-sub
  refresh-interval: 5m
  refresh-margin: 15m
  # token-url / client-id / scope are optional — default to the probed constants
  # (https://platform.claude.com/v1/oauth/token, client 9d1c250a-…, the claude scope set)
`
	cfg, err := parseServeConfig([]byte(y)) // use the real parser name in config.go
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Pool == nil || cfg.Pool.CredName != "anthropic-sub" || cfg.Pool.Store == "" {
		t.Fatalf("pool block not parsed: %+v", cfg.Pool)
	}
}
```

- [ ] **Step 2: Run to verify failure.**

Run: `go test ./cmd/at-jam/ -run TestServeConfigParsesPoolBlock -v`
Expected: FAIL — no `Pool` field.

- [ ] **Step 3: Add the `poolConfig` struct + field** in `config.go`:

```go
type poolConfig struct {
	Store           string `yaml:"store"`
	CredName        string `yaml:"cred-name"`
	RefreshInterval string `yaml:"refresh-interval"`
	RefreshMargin   string `yaml:"refresh-margin"`
	TokenURL        string `yaml:"token-url"`
	ClientID        string `yaml:"client-id"`
}
// add to serveConfig:
//   Pool *poolConfig `yaml:"pool"`
```

Add `validatePool()` (when `Pool != nil`: `Store` and `CredName` required; `TokenURL`/`ClientID`/`Scope` optional — the `Refresher` defaults them to the probed constants; durations parse; default interval 5m / margin 15m).

- [ ] **Step 4: Construct + wire in `main.go`** (near the existing `creds := jam.NewSecretResolver(...)` / `broker := jam.NewBroker(...)`):

```go
	base := jam.NewSecretResolver(runner.OS{}, specs)
	var creds jam.CredResolver = base
	if cfg.Pool != nil {
		poolStore, err := jam.NewFilePoolStore(cfg.Pool.Store)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: pool store:", err)
			return 1
		}
		pool := jam.NewPool(poolStore)
		creds = jam.NewChainResolver(base, pool, cfg.Pool.CredName)
		interval, margin := cfg.poolDurations() // parsed with defaults in config.go
		refresher := jam.NewRefresher(poolStore, jam.RefresherOptions{
			TokenURL: cfg.Pool.TokenURL, ClientID: cfg.Pool.ClientID,
			Margin: margin, Log: log,
		})
		go refresher.Run(ctx, interval) // ctx = the serve lifetime context
		log.Info("Jam subscription pool enabled", "store", cfg.Pool.Store, "cred", cfg.Pool.CredName) // never tokens
	}
	broker := jam.NewBroker(st, creds, log)
```

Pass `Subscription: cfg.Pool != nil` through the launcher config into `connect.CoveMasterOptions` (thread a `Subscription bool` on `launcher.Config` set from `cfg.Pool != nil`, used in `LaunchCoveMaster`).

- [ ] **Step 5: Add the `pool` admin verb** in `main.go` (register alongside `destination`): `at-jam pool add --name <n> --from-file <f>` reads a `claudeAiOauth` JSON block from the file (or stdin), extracts `accessToken`/`refreshToken`/`expiresAt`, and writes a `PoolAccount` via the store (host-side, direct file store — no admin API needed for slice 1). The token block is read from a file/stdin, **never argv**.

- [ ] **Step 6: Docs.** In `docs/usage/jam/coves.md`, add a "Subscription credential pool" subsection under the credentialing/agent-connector story: the `pool:` block, the `at-jam pool add` bootstrap, the destination flip (`identity_in: bearer`, `cred_name: anthropic-sub`, `apply: bearer`), and that the refresh host must be on Jam's egress allow-list. Link it from `docs/OVERVIEW.md`'s security/credential section. State the rollout: no `pool:` block ⇒ unchanged (`x-api-key`).

- [ ] **Step 7: Run the full suite + lint.**

Run: `just test && just lint`
Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add cmd/at-jam/ internal/jam/launcher/launcher.go internal/connect/covemaster.go docs/
git commit -m "feat(at-jam): wire the subscription pool (config, resolver, refresher, pool verb)

<footer>"
```

---

## Self-Review

**Spec coverage:**
- Pool store (file, interface) → Task 1 ✅
- Identity→account bind-once for life + even spread → Task 1 (`Pool`/`leastLoaded`) ✅
- `PoolResolver` beside `SecretResolver`, `IdentityCredResolver` capability, `ChainResolver` → Task 2 ✅ (matches the corrected spec §2)
- Broker path reused; `anthropic-beta` preserved; bearer identity → real bearer → Task 2 broker test ✅
- Broker-owned background refresher, far-future dummy, margin → Task 3 ✅
- Refresh endpoint/client_id unknown → Task 0 spike, gating Task 3 ✅
- Cove seed (dummy `claudeAiOauth`, `accessToken == identity`, no `ANTHROPIC_API_KEY`) → Task 4 ✅
- Destination flip to `bearer`, single path, flag-gated rollout → Task 5 (config + docs) ✅
- Security (no token logs/argv; refresh host on Jam allow-list) → Global Constraints + Tasks 3/5 ✅
- Testing hermetic (fakes/httptest/fake clock) → every task ✅
- Deferred items (rate-limit fairness, dead-account health, Postgres store) → not implemented by design (spec "does not solve"); no task, correct.

**Placeholder scan:** the only deferred concrete values are `TokenURL`/`ClientID`, explicitly gated by Task 0 and filled before Task 3 — flagged, not a silent TODO. Test helper names (`brokerTestStore`, `testLogger`, `parseServeConfig`) are called out as "use the existing name" to avoid inventing APIs.

**Type consistency:** `PoolAccount`, `PoolStore`, `Pool.TokenFor`, `IdentityCredResolver.ResolveFor`, `ChainResolver`, `RefresherOptions`, `DummyCredentials`, `RenderSubscription`, `CoveMasterOptions.Subscription` are used with identical signatures across the tasks that define and consume them.

---

## Board decomposition

Each task above is one PR's worth. Route to Linear via **board-plan** as sub-issues of a tracking epic:
- Task 0 → `class:attended` (a spike; produces a spec note, best done interactively).
- Tasks 1–4 → `class:<implement-worker>` (self-contained, hermetic, dispatchable). Task 3 **blocks on** Task 0; Task 2 blocks on Task 1; Task 4 is independent.
- Task 5 → `class:attended` (touches serve wiring + the live destination flip + docs; wants a human in the loop for the rollout).
