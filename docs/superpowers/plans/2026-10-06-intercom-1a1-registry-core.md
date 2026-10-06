# intercom 1a-1: identity registry core — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Jam's surrogate-id package and the additive registry core — users (with logins and OIDC bindings), connections and accounts — to `jam.Store`, in both `MemStore` and `PostgresStore`, behind one conformance suite.

**Architecture:** A stdlib-only `internal/ident` package mints and parses kind-prefixed, time-sortable ids. The registry follows the store's existing split: `memState` owns maps, reads and lock-free `prepare*` validators; `MemStore` does *lock → prepare → apply*; `PostgresStore` does *lock → prepare → SQL (one transaction) → apply*. Nothing in the product calls the registry yet — later plans (1a-2 projects and memberships, 1a-3 the humans→users cutover, 1a-4 connections in serve config, 1a-5 export v2) build on it, so this PR is dormant and safe to ship.

**Tech Stack:** Go 1.26, stdlib, pgx v5 (existing), Postgres migrations embedded in `internal/jam/migrations`.

**Spec:** `docs/superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md` (§1 ids, §2 schema, §3 domain model). Vocabulary and decisions: `docs/superpowers/specs/2026-10-06-intercom-identity-and-channels-design.md`.

## Plan series for slice 1a

This plan is the first of five; each is a shippable PR, and each later plan is written against the code the previous one actually landed.

1. **1a-1 registry core** (this plan): `internal/ident`; users, connections, accounts; `Directory` reads.
2. **1a-2 project ids + memberships:** `Project.ID`, tombstone/rename for projects, roles/grants/instances by project id, memberships.
3. **1a-3 humans → users cutover:** the data migration (§6), deleting `Human`, admin API/CLI/UI renames, escalation tiers, log refs, ingress accounts, unread cursors, legacy aliases.
4. **1a-4 connections in config:** serve config by connection name, back-compat, relay state files, `ChatService`.
5. **1a-5 export/import v2.**

## Global Constraints

- `internal/ident` is **stdlib-only**.
- Id format: `<prefix>_<26 chars>`; the body is 48-bit Unix milliseconds + 80 random bits from `crypto/rand`, in lowercase Crockford base32 `0123456789abcdefghjkmnpqrstvwxyz`. Prefixes: `prj`, `usr`, `con`, `acc`, `ses`, `chn`.
- Ids are opaque: callers read nothing from an id except `Kind()`. They are never derived from a name and never reused.
- `status ∈ {live, removed}`. Removal is a tombstone: the row stays and `Resolve` still finds it; name lookups, login/OIDC lookups and new links ignore it.
- Live names are unique per entity kind, Jam-wide. A removed entity's name is free for a new entity, which gets a new id.
- Entity names: 1–64 characters, no whitespace, none of `: , * ? [ ] \ /`, and a name must not itself parse as an id.
- Tests are hermetic (`MemStore`). Postgres runs the same suite under `-tags integration` with `JAM_TEST_POSTGRES_DSN`.
- Secrets never appear in the registry: a connection holds a credential **name** (`CredName`), never a value.
- Use the agreed vocabulary (session / studio / episode / turn; *user*, *operator*, *account*, *connection*) in new comments and docs.

## Review Focus

1. **Re-using a removed user's name** must create a new id and leave the old id resolvable as "name (removed)". Pinned in Task 3 (`user_remove_tombstones_and_frees_name`).
2. **Logins and OIDC bindings of a removed user are freed**, so they can be bound to another user. Without this a departed person blocks their login forever. Pinned in Task 3 (`user_remove_frees_logins_and_oidc`).
3. **A name that looks like an id** (e.g. a user named `usr_01j…`) would make "name or id" edge parameters ambiguous. It must be rejected. Pinned in Task 2 (`TestValidateEntityName`).
4. **An account upsert that would merge two different accounts** (handle found on one account, uid already on another) must fail with `ErrAccountTaken` rather than silently re-pointing an identity. Pinned in Task 4 (`account_upsert_conflict`).
5. **Store returns must be copies:** mutating a returned `User.Logins` slice must not change the store. Pinned in Task 3 (`user_returns_copies`).

## File Structure

| File | Responsibility |
|---|---|
| `internal/ident/ident.go` (new) | Mint, parse and classify ids |
| `internal/ident/ident_test.go` (new) | Unit tests |
| `internal/jam/registry.go` (new) | Registry types, `Status`, `Entry`, errors, `ValidateEntityName`, `Directory` and `RegistryStore` interfaces |
| `internal/jam/registry_test.go` (new) | Validation unit tests |
| `internal/jam/registry_state.go` (new) | `memState` registry maps, reads and `prepare*` validators (keeps `memstate.go` from growing) |
| `internal/jam/memstore_registry.go` (new) | `MemStore` registry mutators |
| `internal/jam/pgstore_registry.go` (new) | `PostgresStore` registry mutators and the registry part of `load` |
| `internal/jam/migrations/0006_identity_registry.sql` (new) | Tables and indexes |
| `internal/jam/storetest/registry.go` (new) | `runRegistryConformance`, called from `RunConformance` |
| `internal/jam/memstate.go` (modify) | Initialise the three new maps in `newMemState` |
| `internal/jam/store.go` (modify) | `Store` embeds `RegistryStore` |
| `internal/jam/pgstore.go` (modify) | `load` calls `loadRegistry` |
| `internal/jam/pgstore_testhelpers.go` (modify) | Truncate and reset the registry |
| `internal/jam/storetest/conformance.go` (modify) | Call `runRegistryConformance` |
| `docs/usage/jam/roster.md` (modify) | A short "Identity registry" section |

---

### Task 1: `internal/ident`

**Files:**
- Create: `internal/ident/ident.go`
- Test: `internal/ident/ident_test.go`

**Interfaces:**
- Produces: `type Kind string`; constants `ident.Project`, `ident.User`, `ident.Connection`, `ident.Account`, `ident.Session`, `ident.Channel`; `type ID string`; `func New(k Kind) ID`; `func Parse(s string) (ID, error)`; `func (ID) Kind() Kind`; `func (ID) String() string`.

- [ ] **Step 1: Write the failing tests**

```go
package ident

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewHasKindPrefixAndParses(t *testing.T) {
	for _, k := range []Kind{Project, User, Connection, Account, Session, Channel} {
		id := New(k)
		if !strings.HasPrefix(string(id), string(k)+"_") {
			t.Fatalf("New(%s) = %q, want prefix %s_", k, id, k)
		}
		if len(id) != len(k)+1+26 {
			t.Fatalf("New(%s) = %q, want length %d", k, id, len(k)+27)
		}
		got, err := Parse(string(id))
		if err != nil || got != id {
			t.Fatalf("Parse(%q) = %q, %v", id, got, err)
		}
		if id.Kind() != k {
			t.Fatalf("%q.Kind() = %q, want %q", id, id.Kind(), k)
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := map[ID]bool{}
	for i := 0; i < 10000; i++ {
		id := New(User)
		if seen[id] {
			t.Fatalf("duplicate id %q after %d", id, i)
		}
		seen[id] = true
	}
}

func TestTextOrderFollowsTime(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)
	// Maximal entropy for the earlier id, minimal for the later one: time must dominate.
	a := newAt(User, t0, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	b := newAt(User, t0.Add(time.Millisecond), bytes.NewReader(make([]byte, 10)))
	if !(a < b) {
		t.Fatalf("want %q < %q", a, b)
	}
}

func TestParseRejects(t *testing.T) {
	valid := string(New(User))
	body := valid[len("usr_"):]
	for _, s := range []string{
		"",
		"usr",
		"usr_",
		"xyz_" + body,                // unknown kind
		"USR_" + body,                // kind is lowercase
		"usr_" + body[:25],           // short
		"usr_" + body + "0",          // long
		"usr_" + strings.ToUpper(body), // body is lowercase
		"usr_8" + body[1:],           // first char > 7 overflows 128 bits
		"usr_" + body[:25] + "u",     // 'u' is not in the alphabet
		"usr_" + body[:25] + "i",
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = nil error, want rejection", s)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ident/`
Expected: FAIL — `undefined: New` (the package has no non-test files yet).

- [ ] **Step 3: Write the implementation**

```go
// Package ident mints and parses Jam's surrogate ids: opaque, kind-prefixed,
// time-sortable strings such as "usr_01j9q3…". An id is never derived from a
// name and never reused; its Kind is the only thing a caller may read from it.
package ident

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// Kind is an id's entity kind, written as its prefix.
type Kind string

const (
	Project    Kind = "prj"
	User       Kind = "usr"
	Connection Kind = "con"
	Account    Kind = "acc"
	Session    Kind = "ses"
	Channel    Kind = "chn"
)

var kinds = map[Kind]bool{Project: true, User: true, Connection: true, Account: true, Session: true, Channel: true}

// alphabet is lowercase Crockford base32. Its characters ascend in ASCII, so
// the text order of equal-length bodies is their numeric order — and, because
// the time comes first, their creation order.
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// bodyLen is 128 bits in base32: 26 characters, the first holding 3 bits.
const bodyLen = 26

// ID is a surrogate id.
type ID string

// New mints a fresh id of kind k. It panics on an unknown kind (a programming
// error) or if the system entropy source fails.
func New(k Kind) ID { return newAt(k, time.Now(), rand.Reader) }

func newAt(k Kind, t time.Time, r io.Reader) ID {
	if !kinds[k] {
		panic("ident: unknown kind " + string(k))
	}
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		panic("ident: entropy: " + err.Error())
	}
	return ID(string(k) + "_" + encode(b))
}

func encode(b [16]byte) string {
	n := new(big.Int).SetBytes(b[:])
	mask := big.NewInt(31)
	out := make([]byte, bodyLen)
	for i := bodyLen - 1; i >= 0; i-- {
		out[i] = alphabet[new(big.Int).And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return string(out)
}

// Parse validates s as an id: a known kind prefix, "_", and a well-formed body.
func Parse(s string) (ID, error) {
	k, body, ok := strings.Cut(s, "_")
	if !ok || !kinds[Kind(k)] {
		return "", fmt.Errorf("ident: %q has no known kind prefix", s)
	}
	if len(body) != bodyLen || body[0] < '0' || body[0] > '7' {
		return "", fmt.Errorf("ident: %q has a malformed body", s)
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(alphabet, body[i]) < 0 {
			return "", fmt.Errorf("ident: %q has a malformed body", s)
		}
	}
	return ID(s), nil
}

// Kind returns the id's kind (its prefix). It does not validate the id.
func (id ID) Kind() Kind {
	k, _, _ := strings.Cut(string(id), "_")
	return Kind(k)
}

func (id ID) String() string { return string(id) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ident/ && go vet ./internal/ident/`
Expected: `ok  github.com/aethons-tools/cove/internal/ident`

- [ ] **Step 5: Commit**

```bash
git add internal/ident
git commit -m "feat(ident): kind-prefixed, time-sortable surrogate ids"
```

---

### Task 2: Registry types, errors and name validation

**Files:**
- Create: `internal/jam/registry.go`
- Test: `internal/jam/registry_test.go`

**Interfaces:**
- Consumes: `ident.ID`, `ident.Kind`, `ident.Parse` (Task 1); the existing `jam.OIDCIdentity` and `jam.ValidateIdentity` (`identity.go:180-197`).
- Produces (later tasks rely on these exact names):
  - `type Status string`; `StatusLive`, `StatusRemoved`.
  - `User{ID, Name, Status, Logins []string, OIDC []OIDCIdentity}`.
  - `Connection{ID, Kind, Name, CredName, Status}`; `ConnectionKinds` (`linear`, `discord`).
  - `Account{ID, ConnectionID, ServiceUID, Handle, Label, UserID, Status}`.
  - `Entry{ID, Kind, Name, Status}` with `Label() string`.
  - `ValidateEntityName(name string) error`.
  - Errors: `ErrInvalidName`, `ErrNameTaken`, `ErrLoginTaken`, `ErrIdentityTaken`, `ErrAccountTaken`, `ErrUserNotFound`, `ErrConnectionNotFound`, `ErrAccountNotFound`, `ErrConnectionInUse`, `ErrRemoved`.
  - `Directory` and `RegistryStore` interfaces (signatures below).

- [ ] **Step 1: Write the failing test**

```go
package jam

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestValidateEntityName(t *testing.T) {
	for _, ok := range []string{"alice", "Alice.B", "linear-acme", "a_b", strings.Repeat("x", 64)} {
		if err := ValidateEntityName(ok); err != nil {
			t.Errorf("ValidateEntityName(%q) = %v, want nil", ok, err)
		}
	}
	bad := []string{
		"", strings.Repeat("x", 65), "a b", "a\tb", "a:b", "a,b", "a*", "a?", "a[b]", `a\b`, "a/b",
		string(ident.New(ident.User)), // a name must not look like an id
	}
	for _, name := range bad {
		if err := ValidateEntityName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateEntityName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestEntryLabel(t *testing.T) {
	if got := (Entry{Name: "alice", Status: StatusLive}).Label(); got != "alice" {
		t.Fatalf("live label = %q", got)
	}
	if got := (Entry{Name: "alice", Status: StatusRemoved}).Label(); got != "alice (removed)" {
		t.Fatalf("removed label = %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/jam/ -run 'TestValidateEntityName|TestEntryLabel'`
Expected: FAIL — `undefined: ValidateEntityName`.

- [ ] **Step 3: Write `internal/jam/registry.go`**

```go
package jam

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// Status is a registry entity's lifecycle state. Removal is a tombstone: a
// removed entity keeps its id and still resolves (for history), but no name,
// login or account lookup finds it, and its name is free for a new entity.
type Status string

const (
	StatusLive    Status = "live"
	StatusRemoved Status = "removed"
)

// User is a person registered with Jam who talks to agents (as opposed to an
// operator, who configures Jam). Users are Jam-wide. Logins are admin
// OperatorIDs (OIDC sub, or "local"); OIDC binds browser identities.
type User struct {
	ID     ident.ID       `json:"id"`
	Name   string         `json:"name"`
	Status Status         `json:"status"`
	Logins []string       `json:"logins,omitempty"`
	OIDC   []OIDCIdentity `json:"oidc,omitempty"`
}

// Connection is one configured instance of an external service (a Linear
// workspace, a Discord bot). CredName names the credential Jam uses for it;
// the value never enters the registry.
type Connection struct {
	ID       ident.ID `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	CredName string   `json:"cred_name,omitempty"`
	Status   Status   `json:"status"`
}

// ConnectionKinds are the services a connection may be.
var ConnectionKinds = []string{"linear", "discord"}

// Account is an identity on a connection's service. ServiceUID is the
// service's immutable user id; Handle its mention handle; either may be
// learned later, but one is required. An account with no UserID is an
// external sender not (yet) linked to a Jam user.
type Account struct {
	ID           ident.ID `json:"id"`
	ConnectionID ident.ID `json:"connection_id"`
	ServiceUID   string   `json:"service_uid,omitempty"`
	Handle       string   `json:"handle,omitempty"`
	Label        string   `json:"label,omitempty"`
	UserID       ident.ID `json:"user_id,omitempty"`
	Status       Status   `json:"status"`
}

// Entry is what Resolve knows about any id: enough to render it.
type Entry struct {
	ID     ident.ID
	Kind   ident.Kind
	Name   string
	Status Status
}

// Label renders the entry for people: its name, marked when removed.
func (e Entry) Label() string {
	if e.Status == StatusRemoved {
		return e.Name + " (removed)"
	}
	return e.Name
}

var (
	ErrInvalidName        = errors.New("invalid name")
	ErrNameTaken          = errors.New("name already in use")
	ErrLoginTaken         = errors.New("login already bound to another user")
	ErrIdentityTaken      = errors.New("oidc identity already bound to another user")
	ErrAccountTaken       = errors.New("account identity already in use")
	ErrUserNotFound       = errors.New("user not found")
	ErrConnectionNotFound = errors.New("connection not found")
	ErrAccountNotFound    = errors.New("account not found")
	ErrConnectionInUse    = errors.New("connection is still referenced")
	ErrRemoved            = errors.New("entity has been removed")
)

// ValidateEntityName checks a registry name. Names are labels addressed as
// "<kind>:<name>" (and "chat:<a>,<b>", "ticket:<connection>/<key>"), so they
// exclude whitespace, address and glob metacharacters, and anything that
// parses as an id — every edge accepts "a name or an id", which must not be
// ambiguous.
func ValidateEntityName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("%w: %q must be 1-64 characters", ErrInvalidName, name)
	}
	if strings.ContainsAny(name, ":,*?[]\\/ \t\r\n") {
		return fmt.Errorf("%w: %q must not contain whitespace or any of : , * ? [ ] \\ /", ErrInvalidName, name)
	}
	if _, err := ident.Parse(name); err == nil {
		return fmt.Errorf("%w: %q looks like an id", ErrInvalidName, name)
	}
	return nil
}

// Directory is the registry's read side: resolve any id for display, and map
// names, logins and service identities to live entities.
type Directory interface {
	// Resolve finds any registry id, removed ones included.
	Resolve(id ident.ID) (Entry, bool)
	// LookupName finds the live entity of kind k named name (User and
	// Connection in this slice).
	LookupName(k ident.Kind, name string) (ident.ID, bool)
	UserByLogin(login string) (User, bool)
	UserByOIDC(issuer, subject string) (User, bool)
	AccountByUID(conn ident.ID, uid string) (Account, bool)
	AccountByHandle(conn ident.ID, handle string) (Account, bool)
}

// RegistryStore is the Store's registry: users, connections and accounts.
// Create* mint the id when it is empty. Get* include removed entities; List*
// return live ones sorted by name (accounts: by id). Returned values are copies.
type RegistryStore interface {
	Directory

	CreateUser(u User) (User, error)
	RenameUser(id ident.ID, name string) error
	SetUserLogins(id ident.ID, logins []string) error
	SetUserOIDC(id ident.ID, ids []OIDCIdentity) error
	// RemoveUser tombstones the user, frees their logins and OIDC bindings,
	// and unlinks their accounts.
	RemoveUser(id ident.ID) error
	GetUser(id ident.ID) (User, bool)
	ListUsers() []User

	CreateConnection(c Connection) (Connection, error)
	RenameConnection(id ident.ID, name string) error
	// RemoveConnection tombstones the connection; ErrConnectionInUse while any
	// account belongs to it.
	RemoveConnection(id ident.ID) error
	GetConnection(id ident.ID) (Connection, bool)
	ListConnections() []Connection

	// UpsertAccount finds the account by (ConnectionID, ServiceUID), else by
	// (ConnectionID, Handle), and fills in what it learned (a missing uid or
	// handle, a new label); otherwise it creates one. ErrAccountTaken when the
	// handle and the uid belong to two different accounts.
	UpsertAccount(a Account) (Account, error)
	// LinkAccount links the account to a live user; userID "" unlinks.
	LinkAccount(id, userID ident.ID) error
	GetAccount(id ident.ID) (Account, bool)
	ListAccounts(conn ident.ID) []Account
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/jam/ -run 'TestValidateEntityName|TestEntryLabel'`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/jam/registry.go internal/jam/registry_test.go
git commit -m "feat(jam): registry types, errors and entity-name validation"
```

---

### Task 3: Users in the store (conformance + memState + MemStore)

**Files:**
- Create: `internal/jam/storetest/registry.go`, `internal/jam/registry_state.go`, `internal/jam/memstore_registry.go`
- Modify: `internal/jam/memstate.go` (`memState` fields and `newMemState`), `internal/jam/store.go` (embed `RegistryStore`), `internal/jam/storetest/conformance.go` (call the new suite)

**Interfaces:**
- Consumes: Task 2's types, errors and `RegistryStore`.
- Produces: the `memState` maps `users`, `connections`, `accounts` (keyed by `ident.ID`); `idExists(id) bool`; `newEntityID(id, kind) (ident.ID, error)`; `liveUser(id) (User, error)`; `accountName(Account) string`; lock-free `prepare*` helpers that `PostgresStore` reuses in Task 5:
  - `prepareCreateUser(u User) (User, error)`
  - `prepareRenameUser(id ident.ID, name string) (User, error)`
  - `prepareSetUserLogins(id ident.ID, logins []string) (User, error)`
  - `prepareSetUserOIDC(id ident.ID, ids []OIDCIdentity) (User, error)`
  - `prepareRemoveUser(id ident.ID) (User, []Account, error)` (the tombstoned user and the accounts it unlinks)
  - `applyPutUser(u User)`, `applyPutAccount(a Account)`
- **`PostgresStore` must keep compiling:** embedding `RegistryStore` in `Store` means `PostgresStore` needs the new methods. This task adds them to `PostgresStore` as `panic("pgstore: registry not implemented")` stubs in `pgstore_registry.go`; Task 5 replaces them. The stubs are never reached by hermetic tests.

- [ ] **Step 1: Write the failing conformance tests**

Create `internal/jam/storetest/registry.go`:

```go
package storetest

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runRegistryConformance exercises the RegistryStore contract.
func runRegistryConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	mustUser := func(t *testing.T, s jam.Store, name string) jam.User {
		t.Helper()
		u, err := s.CreateUser(jam.User{Name: name})
		if err != nil {
			t.Fatalf("CreateUser %s: %v", name, err)
		}
		return u
	}

	t.Run("user_create_mints_id_and_resolves", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "alice")
		if u.ID.Kind() != ident.User || u.Status != jam.StatusLive {
			t.Fatalf("created = %+v", u)
		}
		if got, ok := s.GetUser(u.ID); !ok || got.Name != "alice" {
			t.Fatalf("GetUser = %+v, %v", got, ok)
		}
		if id, ok := s.LookupName(ident.User, "alice"); !ok || id != u.ID {
			t.Fatalf("LookupName = %q, %v", id, ok)
		}
		e, ok := s.Resolve(u.ID)
		if !ok || e.Kind != ident.User || e.Label() != "alice" {
			t.Fatalf("Resolve = %+v, %v", e, ok)
		}
		if _, err := s.CreateUser(jam.User{Name: "alice"}); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("duplicate live name: %v, want ErrNameTaken", err)
		}
		if _, err := s.CreateUser(jam.User{Name: "a b"}); !errors.Is(err, jam.ErrInvalidName) {
			t.Fatalf("bad name: %v, want ErrInvalidName", err)
		}
	})

	t.Run("user_create_with_given_id", func(t *testing.T) {
		s := newStore(t)
		id := ident.New(ident.User)
		u, err := s.CreateUser(jam.User{ID: id, Name: "alice"})
		if err != nil || u.ID != id {
			t.Fatalf("CreateUser with id = %+v, %v", u, err)
		}
		if _, err := s.CreateUser(jam.User{ID: id, Name: "bob"}); err == nil {
			t.Fatal("re-using an id must fail")
		}
		if _, err := s.CreateUser(jam.User{ID: ident.New(ident.Project), Name: "carol"}); err == nil {
			t.Fatal("a user id of the wrong kind must fail")
		}
	})

	t.Run("user_rename", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		mustUser(t, s, "bob")
		if err := s.RenameUser(a.ID, "bob"); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("rename onto a live name: %v, want ErrNameTaken", err)
		}
		if err := s.RenameUser(a.ID, "alice"); err != nil {
			t.Fatalf("rename to own name must be a no-op: %v", err)
		}
		if err := s.RenameUser(a.ID, "alicia"); err != nil {
			t.Fatalf("RenameUser: %v", err)
		}
		if _, ok := s.LookupName(ident.User, "alice"); ok {
			t.Fatal("old name still resolves")
		}
		if id, _ := s.LookupName(ident.User, "alicia"); id != a.ID {
			t.Fatalf("new name resolves to %q, want %q", id, a.ID)
		}
		if err := s.RenameUser(ident.New(ident.User), "x"); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("rename unknown: %v, want ErrUserNotFound", err)
		}
	})

	t.Run("user_logins_and_oidc_unique", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		b := mustUser(t, s, "bob")
		if err := s.SetUserLogins(a.ID, []string{"auth0|a", "local"}); err != nil {
			t.Fatalf("SetUserLogins: %v", err)
		}
		if err := s.SetUserLogins(b.ID, []string{"local"}); !errors.Is(err, jam.ErrLoginTaken) {
			t.Fatalf("shared login: %v, want ErrLoginTaken", err)
		}
		if u, ok := s.UserByLogin("local"); !ok || u.ID != a.ID {
			t.Fatalf("UserByLogin = %+v, %v", u, ok)
		}
		oidc := []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "s1"}}
		if err := s.SetUserOIDC(a.ID, oidc); err != nil {
			t.Fatalf("SetUserOIDC: %v", err)
		}
		if err := s.SetUserOIDC(b.ID, oidc); !errors.Is(err, jam.ErrIdentityTaken) {
			t.Fatalf("shared oidc: %v, want ErrIdentityTaken", err)
		}
		if err := s.SetUserOIDC(b.ID, []jam.OIDCIdentity{{Issuer: "", Subject: "x"}}); err == nil {
			t.Fatal("an empty issuer must be rejected")
		}
		if u, ok := s.UserByOIDC("https://idp", "s1"); !ok || u.ID != a.ID {
			t.Fatalf("UserByOIDC = %+v, %v", u, ok)
		}
		// Replacing the set releases what was dropped.
		if err := s.SetUserLogins(a.ID, []string{"auth0|a"}); err != nil {
			t.Fatalf("SetUserLogins shrink: %v", err)
		}
		if err := s.SetUserLogins(b.ID, []string{"local"}); err != nil {
			t.Fatalf("a released login must be bindable: %v", err)
		}
	})

	t.Run("user_remove_tombstones_and_frees_name", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatalf("RemoveUser: %v", err)
		}
		if err := s.RemoveUser(a.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("second remove: %v, want ErrRemoved", err)
		}
		if err := s.RenameUser(a.ID, "x"); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("rename removed: %v, want ErrRemoved", err)
		}
		e, ok := s.Resolve(a.ID)
		if !ok || e.Status != jam.StatusRemoved || e.Label() != "alice (removed)" {
			t.Fatalf("Resolve removed = %+v, %v", e, ok)
		}
		if _, ok := s.LookupName(ident.User, "alice"); ok {
			t.Fatal("a removed user must not be found by name")
		}
		if len(s.ListUsers()) != 0 {
			t.Fatalf("ListUsers = %+v, want none live", s.ListUsers())
		}
		a2 := mustUser(t, s, "alice")
		if a2.ID == a.ID {
			t.Fatal("re-using a removed name must mint a new id")
		}
	})

	t.Run("user_remove_frees_logins_and_oidc", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		oidc := []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "s1"}}
		if err := s.SetUserLogins(a.ID, []string{"local"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetUserOIDC(a.ID, oidc); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.UserByLogin("local"); ok {
			t.Fatal("a removed user's login must not resolve")
		}
		b := mustUser(t, s, "bob")
		if err := s.SetUserLogins(b.ID, []string{"local"}); err != nil {
			t.Fatalf("login freed by removal: %v", err)
		}
		if err := s.SetUserOIDC(b.ID, oidc); err != nil {
			t.Fatalf("oidc freed by removal: %v", err)
		}
	})

	t.Run("user_returns_copies", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		if err := s.SetUserLogins(a.ID, []string{"local"}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetUser(a.ID)
		got.Logins[0] = "mutated"
		if again, _ := s.GetUser(a.ID); again.Logins[0] != "local" {
			t.Fatalf("store mutated through a returned value: %+v", again)
		}
		list := s.ListUsers()
		list[0].Logins[0] = "mutated"
		if u, _ := s.UserByLogin("local"); u.ID != a.ID {
			t.Fatal("store mutated through ListUsers")
		}
	})

	t.Run("users_list_sorted_live", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "carol")
		b := mustUser(t, s, "bob")
		mustUser(t, s, "alice")
		if err := s.RemoveUser(b.ID); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, u := range s.ListUsers() {
			names = append(names, u.Name)
		}
		if len(names) != 2 || names[0] != "alice" || names[1] != "carol" {
			t.Fatalf("ListUsers names = %v", names)
		}
	})
}
```

In `internal/jam/storetest/conformance.go`, add as the first statement of `RunConformance`'s body:

```go
	runRegistryConformance(t, newStore)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/jam/...`
Expected: build failure — `s.CreateUser undefined (type jam.Store has no field or method CreateUser)`.

- [ ] **Step 3: Embed `RegistryStore` in `Store` and add the memState maps**

In `internal/jam/store.go`, add as the first line inside `type Store interface {`:

```go
	RegistryStore
```

In `internal/jam/memstate.go`, add to the `memState` struct, after `projects`:

```go
	// users, connections and accounts are the identity registry, keyed by id;
	// removed entities stay (tombstones). See registry_state.go.
	users       map[ident.ID]User
	connections map[ident.ID]Connection
	accounts    map[ident.ID]Account
```

and to `newMemState`'s literal:

```go
		users:       map[ident.ID]User{},
		connections: map[ident.ID]Connection{},
		accounts:    map[ident.ID]Account{},
```

with `"github.com/aethons-tools/cove/internal/ident"` added to its imports.

- [ ] **Step 4: Write `internal/jam/registry_state.go` (reads + user prepares)**

```go
package jam

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// ---- registry reads (RLock; promoted to the embedding Store) ----

func (m *memState) Resolve(id ident.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch id.Kind() {
	case ident.User:
		if u, ok := m.users[id]; ok {
			return Entry{ID: id, Kind: ident.User, Name: u.Name, Status: u.Status}, true
		}
	case ident.Connection:
		if c, ok := m.connections[id]; ok {
			return Entry{ID: id, Kind: ident.Connection, Name: c.Name, Status: c.Status}, true
		}
	case ident.Account:
		if a, ok := m.accounts[id]; ok {
			return Entry{ID: id, Kind: ident.Account, Name: accountName(a), Status: a.Status}, true
		}
	}
	return Entry{}, false
}

// accountName is an account's display name: its label, else its handle, else
// its service uid.
func accountName(a Account) string {
	return cmp.Or(a.Label, a.Handle, a.ServiceUID)
}

func (m *memState) LookupName(k ident.Kind, name string) (ident.ID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch k {
	case ident.User:
		if u, ok := m.liveUserNamed(name); ok {
			return u.ID, true
		}
	}
	return "", false
}

func (m *memState) UserByLogin(login string) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.Logins, login) {
			return copyUser(u), true
		}
	}
	return User{}, false
}

func (m *memState) UserByOIDC(issuer, subject string) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := OIDCIdentity{Issuer: issuer, Subject: subject}
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.OIDC, want) {
			return copyUser(u), true
		}
	}
	return User{}, false
}

func (m *memState) GetUser(id ident.ID) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	return copyUser(u), ok
}

func (m *memState) ListUsers() []User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []User
	for _, u := range m.users {
		if u.Status == StatusLive {
			out = append(out, copyUser(u))
		}
	}
	slices.SortFunc(out, func(a, b User) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// ---- lock-free helpers (caller holds mu) ----

func (m *memState) liveUserNamed(name string) (User, bool) {
	for _, u := range m.users {
		if u.Status == StatusLive && u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// liveUser returns the user for a mutation: ErrUserNotFound when absent,
// ErrRemoved when tombstoned.
func (m *memState) liveUser(id ident.ID) (User, error) {
	u, ok := m.users[id]
	if !ok {
		return User{}, fmt.Errorf("%w: %s", ErrUserNotFound, id)
	}
	if u.Status != StatusLive {
		return User{}, fmt.Errorf("%w: user %s", ErrRemoved, id)
	}
	return u, nil
}

// newEntityID returns id when set (validated as kind k and unused), else a
// fresh id of kind k.
func (m *memState) newEntityID(id ident.ID, k ident.Kind) (ident.ID, error) {
	if id == "" {
		return ident.New(k), nil
	}
	if _, err := ident.Parse(string(id)); err != nil || id.Kind() != k {
		return "", fmt.Errorf("id %q is not a valid %s id", id, k)
	}
	if m.idExists(id) {
		return "", fmt.Errorf("id %q already exists", id)
	}
	return id, nil
}

// idExists reports whether any registry entity (removed included) has id.
func (m *memState) idExists(id ident.ID) bool {
	_, u := m.users[id]
	_, c := m.connections[id]
	_, a := m.accounts[id]
	return u || c || a
}

func (m *memState) checkUserName(name string, self ident.ID) error {
	if err := ValidateEntityName(name); err != nil {
		return err
	}
	if other, ok := m.liveUserNamed(name); ok && other.ID != self {
		return fmt.Errorf("%w: user %q", ErrNameTaken, name)
	}
	return nil
}

func (m *memState) checkLogins(logins []string, self ident.ID) error {
	for _, l := range logins {
		if l == "" {
			return fmt.Errorf("login must be non-empty")
		}
		for _, u := range m.users {
			if u.ID != self && u.Status == StatusLive && slices.Contains(u.Logins, l) {
				return fmt.Errorf("%w: %q", ErrLoginTaken, l)
			}
		}
	}
	return nil
}

func (m *memState) checkOIDC(ids []OIDCIdentity, self ident.ID) error {
	if err := ValidateIdentity(ids); err != nil {
		return err
	}
	for _, id := range ids {
		for _, u := range m.users {
			if u.ID != self && u.Status == StatusLive && slices.Contains(u.OIDC, id) {
				return fmt.Errorf("%w: %s:%s", ErrIdentityTaken, id.Issuer, id.Subject)
			}
		}
	}
	return nil
}

// ---- user prepares: validate and compute the value to persist ----

func (m *memState) prepareCreateUser(u User) (User, error) {
	id, err := m.newEntityID(u.ID, ident.User)
	if err != nil {
		return User{}, err
	}
	if err := m.checkUserName(u.Name, id); err != nil {
		return User{}, err
	}
	if err := m.checkLogins(u.Logins, id); err != nil {
		return User{}, err
	}
	if err := m.checkOIDC(u.OIDC, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.ID, u.Status = id, StatusLive
	return u, nil
}

func (m *memState) prepareRenameUser(id ident.ID, name string) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkUserName(name, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.Name = name
	return u, nil
}

func (m *memState) prepareSetUserLogins(id ident.ID, logins []string) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkLogins(logins, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.Logins = slices.Clone(logins)
	return u, nil
}

func (m *memState) prepareSetUserOIDC(id ident.ID, ids []OIDCIdentity) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkOIDC(ids, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.OIDC = slices.Clone(ids)
	return u, nil
}

// prepareRemoveUser returns the tombstoned user (logins and OIDC cleared) and
// the accounts it unlinks.
func (m *memState) prepareRemoveUser(id ident.ID) (User, []Account, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, nil, err
	}
	u = copyUser(u)
	u.Status, u.Logins, u.OIDC = StatusRemoved, nil, nil
	var unlinked []Account
	for _, a := range m.accounts {
		if a.UserID == id {
			a.UserID = ""
			unlinked = append(unlinked, a)
		}
	}
	return u, unlinked, nil
}

// ---- apply (caller holds mu.Lock) ----

func (m *memState) applyPutUser(u User)       { m.users[u.ID] = copyUser(u) }
func (m *memState) applyPutAccount(a Account) { m.accounts[a.ID] = a }

func copyUser(u User) User {
	u.Logins = slices.Clone(u.Logins)
	u.OIDC = slices.Clone(u.OIDC)
	return u
}
```

- [ ] **Step 5: Write `internal/jam/memstore_registry.go` (user mutators) and the Postgres stubs**

```go
package jam

import "github.com/aethons-tools/cove/internal/ident"

// ---- registry mutators: Lock; prepare via memState; apply ----

func (fs *MemStore) CreateUser(u User) (User, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, err := fs.prepareCreateUser(u)
	if err != nil {
		return User{}, err
	}
	fs.applyPutUser(u)
	return copyUser(u), nil
}

func (fs *MemStore) RenameUser(id ident.ID, name string) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareRenameUser(id, name) })
}

func (fs *MemStore) SetUserLogins(id ident.ID, logins []string) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareSetUserLogins(id, logins) })
}

func (fs *MemStore) SetUserOIDC(id ident.ID, ids []OIDCIdentity) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareSetUserOIDC(id, ids) })
}

func (fs *MemStore) RemoveUser(id ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, unlinked, err := fs.prepareRemoveUser(id)
	if err != nil {
		return err
	}
	fs.applyPutUser(u)
	for _, a := range unlinked {
		fs.applyPutAccount(a)
	}
	return nil
}

func (fs *MemStore) putUserWith(prepare func() (User, error)) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, err := prepare()
	if err != nil {
		return err
	}
	fs.applyPutUser(u)
	return nil
}
```

Create `internal/jam/pgstore_registry.go` with stubs for every `RegistryStore` mutator so the package compiles (Task 5 replaces each body):

```go
package jam

import "github.com/aethons-tools/cove/internal/ident"

func (s *PostgresStore) CreateUser(User) (User, error)          { panic("pgstore: registry not implemented") }
func (s *PostgresStore) RenameUser(ident.ID, string) error      { panic("pgstore: registry not implemented") }
func (s *PostgresStore) SetUserLogins(ident.ID, []string) error { panic("pgstore: registry not implemented") }
func (s *PostgresStore) SetUserOIDC(ident.ID, []OIDCIdentity) error {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) RemoveUser(ident.ID) error { panic("pgstore: registry not implemented") }
func (s *PostgresStore) CreateConnection(Connection) (Connection, error) {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) RenameConnection(ident.ID, string) error { panic("pgstore: registry not implemented") }
func (s *PostgresStore) RemoveConnection(ident.ID) error         { panic("pgstore: registry not implemented") }
func (s *PostgresStore) UpsertAccount(Account) (Account, error)  { panic("pgstore: registry not implemented") }
func (s *PostgresStore) LinkAccount(ident.ID, ident.ID) error    { panic("pgstore: registry not implemented") }
```

Connection and account **reads** and **MemStore mutators** come in Task 4; until then `go build` fails on `MemStore`/`PostgresStore` not implementing `GetConnection` etc. So in this step also add, to `registry_state.go`, these temporary reads (Task 4 replaces them with real ones and they are listed there in full):

```go
func (m *memState) GetConnection(id ident.ID) (Connection, bool) { return Connection{}, false }
func (m *memState) ListConnections() []Connection                  { return nil }
func (m *memState) GetAccount(id ident.ID) (Account, bool)        { return Account{}, false }
func (m *memState) ListAccounts(conn ident.ID) []Account           { return nil }
func (m *memState) AccountByUID(ident.ID, string) (Account, bool)  { return Account{}, false }
func (m *memState) AccountByHandle(ident.ID, string) (Account, bool) {
	return Account{}, false
}
```

and to `memstore_registry.go`:

```go
func (fs *MemStore) CreateConnection(Connection) (Connection, error) { panic("task 4") }
func (fs *MemStore) RenameConnection(ident.ID, string) error         { panic("task 4") }
func (fs *MemStore) RemoveConnection(ident.ID) error                 { panic("task 4") }
func (fs *MemStore) UpsertAccount(Account) (Account, error)          { panic("task 4") }
func (fs *MemStore) LinkAccount(ident.ID, ident.ID) error            { panic("task 4") }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/jam/... ./internal/ident/`
Expected: `ok` for every package; the new `user_*` subtests run under `TestMemStoreConformance` (in `store_conformance_test.go`) and pass.

- [ ] **Step 7: Commit**

```bash
git add internal/jam internal/ident
git commit -m "feat(jam): registry users — tombstones, rename, unique logins and OIDC (MemStore)"
```

---

### Task 4: Connections and accounts (conformance + memState + MemStore)

**Files:**
- Modify: `internal/jam/storetest/registry.go`, `internal/jam/registry_state.go`, `internal/jam/memstore_registry.go`

**Interfaces:**
- Consumes: Task 3's `memState` maps, `newEntityID`, `idExists`, `liveUser`, `applyPutAccount`, `accountName`, `LookupName`.
- Produces (reused by Task 5):
  - `prepareCreateConnection(c Connection) (Connection, error)`
  - `prepareRenameConnection(id ident.ID, name string) (Connection, error)`
  - `prepareRemoveConnection(id ident.ID) (Connection, error)`
  - `prepareUpsertAccount(a Account) (Account, error)`
  - `prepareLinkAccount(id, userID ident.ID) (Account, error)`
  - `applyPutConnection(c Connection)`

- [ ] **Step 1: Write the failing conformance tests**

Append inside `runRegistryConformance` in `internal/jam/storetest/registry.go`:

```go
	mustConn := func(t *testing.T, s jam.Store, kind, name string) jam.Connection {
		t.Helper()
		c, err := s.CreateConnection(jam.Connection{Kind: kind, Name: name, CredName: name + "-cred"})
		if err != nil {
			t.Fatalf("CreateConnection %s: %v", name, err)
		}
		return c
	}

	t.Run("connection_lifecycle", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		if c.ID.Kind() != ident.Connection || c.Status != jam.StatusLive {
			t.Fatalf("created = %+v", c)
		}
		if _, err := s.CreateConnection(jam.Connection{Kind: "linear", Name: "linear-acme"}); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("duplicate: %v, want ErrNameTaken", err)
		}
		if _, err := s.CreateConnection(jam.Connection{Kind: "slack", Name: "s"}); err == nil {
			t.Fatal("an unknown kind must be rejected")
		}
		if err := s.RenameConnection(c.ID, "linear-main"); err != nil {
			t.Fatalf("RenameConnection: %v", err)
		}
		if id, _ := s.LookupName(ident.Connection, "linear-main"); id != c.ID {
			t.Fatalf("renamed lookup = %q", id)
		}
		if err := s.RemoveConnection(c.ID); err != nil {
			t.Fatalf("RemoveConnection: %v", err)
		}
		if e, _ := s.Resolve(c.ID); e.Label() != "linear-main (removed)" {
			t.Fatalf("Resolve removed = %+v", e)
		}
		if len(s.ListConnections()) != 0 {
			t.Fatalf("ListConnections = %+v", s.ListConnections())
		}
	})

	t.Run("connection_remove_refused_while_accounts", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "discord", "discord-main")
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "123"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveConnection(c.ID); !errors.Is(err, jam.ErrConnectionInUse) {
			t.Fatalf("remove with accounts: %v, want ErrConnectionInUse", err)
		}
	})

	t.Run("account_upsert_finds_and_learns", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		a, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, Handle: "alice.h"})
		if err != nil || a.ID.Kind() != ident.Account {
			t.Fatalf("create by handle = %+v, %v", a, err)
		}
		// Ingress later learns the uid for the same handle: same account.
		b, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-1", Handle: "alice.h", Label: "Alice H"})
		if err != nil || b.ID != a.ID || b.ServiceUID != "u-1" || b.Label != "Alice H" {
			t.Fatalf("learn uid = %+v, %v", b, err)
		}
		if got, ok := s.AccountByUID(c.ID, "u-1"); !ok || got.ID != a.ID {
			t.Fatalf("AccountByUID = %+v, %v", got, ok)
		}
		// The handle changed on the service: found by uid, handle updated.
		d, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-1", Handle: "alice.new"})
		if err != nil || d.ID != a.ID || d.Handle != "alice.new" {
			t.Fatalf("handle change = %+v, %v", d, err)
		}
		if _, ok := s.AccountByHandle(c.ID, "alice.h"); ok {
			t.Fatal("the old handle must no longer resolve")
		}
		if e, _ := s.Resolve(a.ID); e.Label() != "Alice H" {
			t.Fatalf("account label = %q", e.Label())
		}
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID}); err == nil {
			t.Fatal("an account needs a uid or a handle")
		}
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: ident.New(ident.Connection), Handle: "x"}); !errors.Is(err, jam.ErrConnectionNotFound) {
			t.Fatalf("unknown connection: %v, want ErrConnectionNotFound", err)
		}
	})

	t.Run("account_upsert_conflict", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, Handle: "alice.h"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-2", Handle: "bob.h"}); err != nil {
			t.Fatal(err)
		}
		// uid u-2 is bob's; handle alice.h is alice's: never merge them.
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-2", Handle: "alice.h"}); !errors.Is(err, jam.ErrAccountTaken) {
			t.Fatalf("merge two accounts: %v, want ErrAccountTaken", err)
		}
	})

	t.Run("account_link_and_unlink_on_user_removal", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "discord", "discord-main")
		u := mustUser(t, s, "alice")
		a, _ := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "123"})
		if err := s.LinkAccount(a.ID, u.ID); err != nil {
			t.Fatalf("LinkAccount: %v", err)
		}
		if got, _ := s.GetAccount(a.ID); got.UserID != u.ID {
			t.Fatalf("linked = %+v", got)
		}
		if err := s.LinkAccount(a.ID, ident.New(ident.User)); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("link to unknown user: %v, want ErrUserNotFound", err)
		}
		if err := s.RemoveUser(u.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAccount(a.ID); got.UserID != "" {
			t.Fatalf("removing the user must unlink: %+v", got)
		}
		if err := s.LinkAccount(a.ID, u.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("link to removed user: %v, want ErrRemoved", err)
		}
		if err := s.LinkAccount(ident.New(ident.Account), ""); !errors.Is(err, jam.ErrAccountNotFound) {
			t.Fatalf("unknown account: %v, want ErrAccountNotFound", err)
		}
	})

	t.Run("accounts_listed_per_connection", func(t *testing.T) {
		s := newStore(t)
		c1 := mustConn(t, s, "discord", "d1")
		c2 := mustConn(t, s, "discord", "d2")
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c1.ID, ServiceUID: "1"}); err != nil {
			t.Fatal(err)
		}
		// The same service uid on another connection is a different account.
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c2.ID, ServiceUID: "1"}); err != nil {
			t.Fatal(err)
		}
		if n1, n2 := len(s.ListAccounts(c1.ID)), len(s.ListAccounts(c2.ID)); n1 != 1 || n2 != 1 {
			t.Fatalf("ListAccounts = %d, %d; want 1, 1", n1, n2)
		}
	})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/jam/ -run TestMemStoreConformance`
Expected: FAIL — `panic: task 4` from `CreateConnection`.

- [ ] **Step 3: Replace the temporary reads in `registry_state.go` and add connection/account prepares**

Delete the six temporary read stubs added in Task 3 Step 5. In `LookupName`, add the connection case after the user case:

```go
	case ident.Connection:
		if c, ok := m.liveConnectionNamed(name); ok {
			return c.ID, true
		}
```

Then add:

```go
func (m *memState) GetConnection(id ident.ID) (Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.connections[id]
	return c, ok
}

func (m *memState) ListConnections() []Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Connection
	for _, c := range m.connections {
		if c.Status == StatusLive {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Connection) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (m *memState) GetAccount(id ident.ID) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.accounts[id]
	return a, ok
}

func (m *memState) ListAccounts(conn ident.ID) []Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Account
	for _, a := range m.accounts {
		if a.ConnectionID == conn && a.Status == StatusLive {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b Account) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (m *memState) AccountByUID(conn ident.ID, uid string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountBy(conn, func(a Account) bool { return uid != "" && a.ServiceUID == uid })
}

func (m *memState) AccountByHandle(conn ident.ID, handle string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountBy(conn, func(a Account) bool { return handle != "" && a.Handle == handle })
}

// accountBy is the lock-free live-account finder on one connection.
func (m *memState) accountBy(conn ident.ID, match func(Account) bool) (Account, bool) {
	for _, a := range m.accounts {
		if a.ConnectionID == conn && a.Status == StatusLive && match(a) {
			return a, true
		}
	}
	return Account{}, false
}

func (m *memState) liveConnectionNamed(name string) (Connection, bool) {
	for _, c := range m.connections {
		if c.Status == StatusLive && c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

func (m *memState) liveConnection(id ident.ID) (Connection, error) {
	c, ok := m.connections[id]
	if !ok {
		return Connection{}, fmt.Errorf("%w: %s", ErrConnectionNotFound, id)
	}
	if c.Status != StatusLive {
		return Connection{}, fmt.Errorf("%w: connection %s", ErrRemoved, id)
	}
	return c, nil
}

func (m *memState) checkConnectionName(name string, self ident.ID) error {
	if err := ValidateEntityName(name); err != nil {
		return err
	}
	if other, ok := m.liveConnectionNamed(name); ok && other.ID != self {
		return fmt.Errorf("%w: connection %q", ErrNameTaken, name)
	}
	return nil
}

func (m *memState) prepareCreateConnection(c Connection) (Connection, error) {
	if !slices.Contains(ConnectionKinds, c.Kind) {
		return Connection{}, fmt.Errorf("connection kind %q is not one of %v", c.Kind, ConnectionKinds)
	}
	id, err := m.newEntityID(c.ID, ident.Connection)
	if err != nil {
		return Connection{}, err
	}
	if err := m.checkConnectionName(c.Name, id); err != nil {
		return Connection{}, err
	}
	c.ID, c.Status = id, StatusLive
	return c, nil
}

func (m *memState) prepareRenameConnection(id ident.ID, name string) (Connection, error) {
	c, err := m.liveConnection(id)
	if err != nil {
		return Connection{}, err
	}
	if err := m.checkConnectionName(name, id); err != nil {
		return Connection{}, err
	}
	c.Name = name
	return c, nil
}

func (m *memState) prepareRemoveConnection(id ident.ID) (Connection, error) {
	c, err := m.liveConnection(id)
	if err != nil {
		return Connection{}, err
	}
	if _, used := m.accountBy(id, func(Account) bool { return true }); used {
		return Connection{}, fmt.Errorf("%w: connection %q has accounts", ErrConnectionInUse, c.Name)
	}
	c.Status = StatusRemoved
	return c, nil
}

// prepareUpsertAccount implements UpsertAccount's find-or-create (see
// RegistryStore). A found account takes the incoming uid/handle/label when set.
func (m *memState) prepareUpsertAccount(in Account) (Account, error) {
	if _, err := m.liveConnection(in.ConnectionID); err != nil {
		return Account{}, err
	}
	if in.ServiceUID == "" && in.Handle == "" {
		return Account{}, fmt.Errorf("an account needs a service uid or a handle")
	}
	byUID, hasUID := m.accountBy(in.ConnectionID, func(a Account) bool { return in.ServiceUID != "" && a.ServiceUID == in.ServiceUID })
	byHandle, hasHandle := m.accountBy(in.ConnectionID, func(a Account) bool { return in.Handle != "" && a.Handle == in.Handle })
	switch {
	case hasUID && hasHandle && byUID.ID != byHandle.ID:
		return Account{}, fmt.Errorf("%w: uid %q and handle %q belong to different accounts", ErrAccountTaken, in.ServiceUID, in.Handle)
	case hasUID || hasHandle:
		a := byUID
		if !hasUID {
			a = byHandle
		}
		if in.ServiceUID != "" {
			if a.ServiceUID != "" && a.ServiceUID != in.ServiceUID {
				return Account{}, fmt.Errorf("%w: handle %q is bound to uid %q", ErrAccountTaken, in.Handle, a.ServiceUID)
			}
			a.ServiceUID = in.ServiceUID
		}
		if in.Handle != "" {
			a.Handle = in.Handle
		}
		if in.Label != "" {
			a.Label = in.Label
		}
		return a, nil
	}
	id, err := m.newEntityID(in.ID, ident.Account)
	if err != nil {
		return Account{}, err
	}
	if in.UserID != "" {
		if _, err := m.liveUser(in.UserID); err != nil {
			return Account{}, err
		}
	}
	in.ID, in.Status = id, StatusLive
	return in, nil
}

func (m *memState) prepareLinkAccount(id, userID ident.ID) (Account, error) {
	a, ok := m.accounts[id]
	if !ok || a.Status != StatusLive {
		return Account{}, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	if userID != "" {
		if _, err := m.liveUser(userID); err != nil {
			return Account{}, err
		}
	}
	a.UserID = userID
	return a, nil
}

func (m *memState) applyPutConnection(c Connection) { m.connections[c.ID] = c }
```

- [ ] **Step 4: Replace the MemStore stubs in `memstore_registry.go`**

```go
func (fs *MemStore) CreateConnection(c Connection) (Connection, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareCreateConnection(c)
	if err != nil {
		return Connection{}, err
	}
	fs.applyPutConnection(c)
	return c, nil
}

func (fs *MemStore) RenameConnection(id ident.ID, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareRenameConnection(id, name)
	if err != nil {
		return err
	}
	fs.applyPutConnection(c)
	return nil
}

func (fs *MemStore) RemoveConnection(id ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareRemoveConnection(id)
	if err != nil {
		return err
	}
	fs.applyPutConnection(c)
	return nil
}

func (fs *MemStore) UpsertAccount(a Account) (Account, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, err := fs.prepareUpsertAccount(a)
	if err != nil {
		return Account{}, err
	}
	fs.applyPutAccount(a)
	return a, nil
}

func (fs *MemStore) LinkAccount(id, userID ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, err := fs.prepareLinkAccount(id, userID)
	if err != nil {
		return err
	}
	fs.applyPutAccount(a)
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/jam/... ./internal/ident/ && go vet ./internal/jam/...`
Expected: `ok` everywhere.

- [ ] **Step 6: Commit**

```bash
git add internal/jam
git commit -m "feat(jam): registry connections and accounts (MemStore)"
```

---

### Task 5: Postgres — migration 0006 and `PostgresStore` registry

**Files:**
- Create: `internal/jam/migrations/0006_identity_registry.sql`
- Modify: `internal/jam/pgstore_registry.go` (replace the stubs), `internal/jam/pgstore.go` (`load`), `internal/jam/pgstore_testhelpers.go`

**Interfaces:**
- Consumes: every `prepare*` and `apply*` helper from Tasks 3–4. `s.pool` (pgx pool), the `pgx.BeginFunc` pattern (`pgstore.go:312`), `s.loadDocs` (`pgstore.go:283`).
- Produces: `func (s *PostgresStore) loadRegistry(ctx context.Context) error`.

- [ ] **Step 1: Write the migration**

`internal/jam/migrations/0006_identity_registry.sql`:

```sql
-- Identity registry (intercom slice 1a-1). Surrogate ids (internal/ident)
-- for users, connections and accounts. participants is the supertable every
-- id-bearing entity inserts into, so later tables can foreign-key any kind.
-- Removal is a tombstone (status = 'removed'); partial unique indexes keep
-- live names unique while letting a removed name be reused. Each table's doc
-- is the source of truth for the cache; the other columns exist to index and
-- constrain.
CREATE TABLE participants (
    id   text PRIMARY KEY,
    kind text NOT NULL
);

CREATE TABLE users (
    id         text PRIMARY KEY REFERENCES participants(id),
    name       text NOT NULL,
    status     text NOT NULL,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_live_name ON users (name) WHERE status = 'live';

-- Login and OIDC uniqueness across live users. A removed user's rows are
-- deleted, freeing them.
CREATE TABLE user_logins (
    login   text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id)
);
CREATE TABLE user_oidc (
    issuer  text NOT NULL,
    subject text NOT NULL,
    user_id text NOT NULL REFERENCES users(id),
    PRIMARY KEY (issuer, subject)
);

CREATE TABLE connections (
    id         text PRIMARY KEY REFERENCES participants(id),
    kind       text NOT NULL,
    name       text NOT NULL,
    status     text NOT NULL,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX connections_live_name ON connections (name) WHERE status = 'live';

CREATE TABLE accounts (
    id            text PRIMARY KEY REFERENCES participants(id),
    connection_id text NOT NULL REFERENCES connections(id),
    service_uid   text,
    handle        text,
    user_id       text REFERENCES users(id),
    status        text NOT NULL,
    doc           jsonb NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX accounts_uid ON accounts (connection_id, service_uid) WHERE service_uid IS NOT NULL;
CREATE UNIQUE INDEX accounts_live_handle ON accounts (connection_id, handle) WHERE handle IS NOT NULL AND status = 'live';
```

- [ ] **Step 2: Replace the stubs in `internal/jam/pgstore_registry.go`**

```go
package jam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/jackc/pgx/v5"
)

// ---- registry mutators: Lock; prepare via memState; SQL (one tx); apply ----

func (s *PostgresStore) CreateUser(u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.prepareCreateUser(u)
	if err != nil {
		return User{}, err
	}
	if err := s.registryTx("CreateUser", func(ctx context.Context, tx pgx.Tx) error {
		return putUserTx(ctx, tx, u, nil)
	}); err != nil {
		return User{}, err
	}
	s.applyPutUser(u)
	return copyUser(u), nil
}

func (s *PostgresStore) RenameUser(id ident.ID, name string) error {
	return s.putUserWith("RenameUser", func() (User, error) { return s.prepareRenameUser(id, name) })
}

func (s *PostgresStore) SetUserLogins(id ident.ID, logins []string) error {
	return s.putUserWith("SetUserLogins", func() (User, error) { return s.prepareSetUserLogins(id, logins) })
}

func (s *PostgresStore) SetUserOIDC(id ident.ID, ids []OIDCIdentity) error {
	return s.putUserWith("SetUserOIDC", func() (User, error) { return s.prepareSetUserOIDC(id, ids) })
}

func (s *PostgresStore) RemoveUser(id ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, unlinked, err := s.prepareRemoveUser(id)
	if err != nil {
		return err
	}
	if err := s.registryTx("RemoveUser", func(ctx context.Context, tx pgx.Tx) error {
		return putUserTx(ctx, tx, u, unlinked)
	}); err != nil {
		return err
	}
	s.applyPutUser(u)
	for _, a := range unlinked {
		s.applyPutAccount(a)
	}
	return nil
}

func (s *PostgresStore) putUserWith(op string, prepare func() (User, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := prepare()
	if err != nil {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error {
		return putUserTx(ctx, tx, u, nil)
	}); err != nil {
		return err
	}
	s.applyPutUser(u)
	return nil
}

func (s *PostgresStore) CreateConnection(c Connection) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.prepareCreateConnection(c)
	if err != nil {
		return Connection{}, err
	}
	if err := s.registryTx("CreateConnection", func(ctx context.Context, tx pgx.Tx) error {
		return putConnectionTx(ctx, tx, c)
	}); err != nil {
		return Connection{}, err
	}
	s.applyPutConnection(c)
	return c, nil
}

func (s *PostgresStore) RenameConnection(id ident.ID, name string) error {
	return s.putConnectionWith("RenameConnection", func() (Connection, error) { return s.prepareRenameConnection(id, name) })
}

func (s *PostgresStore) RemoveConnection(id ident.ID) error {
	return s.putConnectionWith("RemoveConnection", func() (Connection, error) { return s.prepareRemoveConnection(id) })
}

func (s *PostgresStore) putConnectionWith(op string, prepare func() (Connection, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := prepare()
	if err != nil {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error {
		return putConnectionTx(ctx, tx, c)
	}); err != nil {
		return err
	}
	s.applyPutConnection(c)
	return nil
}

func (s *PostgresStore) UpsertAccount(a Account) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.prepareUpsertAccount(a)
	if err != nil {
		return Account{}, err
	}
	if err := s.registryTx("UpsertAccount", func(ctx context.Context, tx pgx.Tx) error {
		return putAccountTx(ctx, tx, a)
	}); err != nil {
		return Account{}, err
	}
	s.applyPutAccount(a)
	return a, nil
}

func (s *PostgresStore) LinkAccount(id, userID ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.prepareLinkAccount(id, userID)
	if err != nil {
		return err
	}
	if err := s.registryTx("LinkAccount", func(ctx context.Context, tx pgx.Tx) error {
		return putAccountTx(ctx, tx, a)
	}); err != nil {
		return err
	}
	s.applyPutAccount(a)
	return nil
}

// ---- SQL ----

func (s *PostgresStore) registryTx(op string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
		return fmt.Errorf("pgstore: %s: %w", op, err)
	}
	return nil
}

func insertParticipantTx(ctx context.Context, tx pgx.Tx, id ident.ID) error {
	_, err := tx.Exec(ctx, `INSERT INTO participants (id, kind) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`, id, string(id.Kind()))
	return err
}

// putUserTx upserts u, replaces its login and OIDC rows (a removed user has
// none), and writes the accounts the change unlinked.
func putUserTx(ctx context.Context, tx pgx.Tx, u User, unlinked []Account) error {
	if err := insertParticipantTx(ctx, tx, u.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, name, status, doc) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		u.ID, u.Name, string(u.Status), doc); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_logins WHERE user_id = $1`, u.ID); err != nil {
		return err
	}
	for _, l := range u.Logins {
		if _, err := tx.Exec(ctx, `INSERT INTO user_logins (login, user_id) VALUES ($1,$2)`, l, u.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_oidc WHERE user_id = $1`, u.ID); err != nil {
		return err
	}
	for _, o := range u.OIDC {
		if _, err := tx.Exec(ctx, `INSERT INTO user_oidc (issuer, subject, user_id) VALUES ($1,$2,$3)`, o.Issuer, o.Subject, u.ID); err != nil {
			return err
		}
	}
	for _, a := range unlinked {
		if err := putAccountTx(ctx, tx, a); err != nil {
			return err
		}
	}
	return nil
}

func putConnectionTx(ctx context.Context, tx pgx.Tx, c Connection) error {
	if err := insertParticipantTx(ctx, tx, c.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO connections (id, kind, name, status, doc) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		c.ID, c.Kind, c.Name, string(c.Status), doc)
	return err
}

func putAccountTx(ctx context.Context, tx pgx.Tx, a Account) error {
	if err := insertParticipantTx(ctx, tx, a.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO accounts (id, connection_id, service_uid, handle, user_id, status, doc)
		 VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),$6,$7)
		 ON CONFLICT (id) DO UPDATE SET service_uid = EXCLUDED.service_uid, handle = EXCLUDED.handle,
		   user_id = EXCLUDED.user_id, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		a.ID, a.ConnectionID, a.ServiceUID, a.Handle, string(a.UserID), string(a.Status), doc)
	return err
}

// loadRegistry fills the registry maps from their docs (load's registry part).
func (s *PostgresStore) loadRegistry(ctx context.Context) error {
	if err := s.loadDocs(ctx, "users", func(doc []byte) error {
		var u User
		if err := json.Unmarshal(doc, &u); err != nil {
			return err
		}
		s.users[u.ID] = u
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "connections", func(doc []byte) error {
		var c Connection
		if err := json.Unmarshal(doc, &c); err != nil {
			return err
		}
		s.connections[c.ID] = c
		return nil
	}); err != nil {
		return err
	}
	return s.loadDocs(ctx, "accounts", func(doc []byte) error {
		var a Account
		if err := json.Unmarshal(doc, &a); err != nil {
			return err
		}
		s.accounts[a.ID] = a
		return nil
	})
}
```

- [ ] **Step 3: Wire `load` and the test helper**

In `pgstore.go`'s `load`, immediately after the `projects` `loadDocs` block, add:

```go
	if err := s.loadRegistry(ctx); err != nil {
		return err
	}
```

In `pgstore_testhelpers.go`, extend the `TRUNCATE` list with `accounts, connections, user_oidc, user_logins, users, participants` (children before parents is not required by `TRUNCATE` of the full set, but list them all in one statement so FKs are satisfied), and after the existing map resets add:

```go
	s.users = map[ident.ID]User{}
	s.connections = map[ident.ID]Connection{}
	s.accounts = map[ident.ID]Account{}
```

with the `ident` import.

- [ ] **Step 4: Run the hermetic tests and the Postgres conformance**

Run: `go test ./internal/jam/... && go vet -tags integration ./internal/jam/...`
Expected: `ok`; vet clean under the integration tag.

Then, where a Postgres is available:
Run: `JAM_TEST_POSTGRES_DSN='host=localhost port=5432 dbname=jam user=jam password=jam sslmode=disable' go test -tags integration ./internal/jam/ -run TestPostgresStoreConformance -v`
Expected: every `user_*`, `connection_*` and `account_*` subtest PASSes. If no Postgres is reachable from the studio, say so in the PR description and ask the reviewer to run this command; do not mark the task verified without it.

- [ ] **Step 5: Commit**

```bash
git add internal/jam
git commit -m "feat(jam): Postgres identity registry (migration 0006)"
```

---

### Task 6: Docs

**Files:**
- Modify: `docs/usage/jam/roster.md`

- [ ] **Step 1: Add an "Identity registry" section** at the end of `docs/usage/jam/roster.md`, and bump its front-matter `updated:` to the commit date:

```markdown
## Identity registry (in progress)

Jam is moving every stored reference from names to **surrogate ids** — opaque,
kind-prefixed, time-sortable strings (`usr_…`, `con_…`, `acc_…`; see
`internal/ident`). The store now holds a registry of:

- **Users** — people registered with Jam who talk to agents (Jam-wide), with
  their admin logins and OIDC bindings (each unique across live users).
- **Connections** — configured instances of an external service (`linear`,
  `discord`), naming the credential Jam uses for each.
- **Accounts** — identities on a connection's service (a uid and/or a handle),
  optionally linked to a user.

Removal is a **tombstone**: the id keeps resolving (shown as "name (removed)"),
but names, logins and links no longer find it, and the name is free for a new
entity with a new id. Renaming touches nothing else, because nothing refers to
names.

Nothing uses the registry yet. The per-project roster humans described above
stay authoritative until the humans → users cutover. Design:
[`superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md`](../../superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md).
```

- [ ] **Step 2: Check links, then commit**

Run: `grep -n "intercom-slice1-identity-registry-design" docs/usage/jam/roster.md && test -f docs/superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md && echo ok`
Expected: the line, then `ok`.

```bash
git add docs/usage/jam/roster.md
git commit -m "docs(jam): identity registry section in roster.md"
```

---

## Final verification

- [ ] `go build ./... && go test ./... && go vet ./...` — all green.
- [ ] `just lint` — clean.
- [ ] The Postgres conformance run from Task 5 Step 4 — green, or the PR says plainly that it is pending a reviewer's run.
