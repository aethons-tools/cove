# Jam role-mapped destination credentials — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Roles map each allowed destination to the credential the broker injects; repo scoping is removed; `gh` can authenticate to the broker.

**Architecture:** `Scope`/`Override` gain an additive `Credentials map[string]string` (destination → credential name, empty = the destination's default `CredName`). `Decide` returns the credential from the authorizing scope and fails closed when grants disagree. A shared `jam.ParseDestinations`/`FormatDestinations` pair gives CLI and UI one `name=cred,name` syntax. Repo scoping (`Scope.Repos`, `Override.Repos`, `Destination.RepoScoped`, `RepoFromPath`, `repoAllowed`) is deleted end to end.

**Tech Stack:** Go stdlib; existing `internal/jam`, `internal/jam/adminui`, `internal/jam/adminclient`, `cmd/at-jam`.

**Spec:** `docs/superpowers/specs/2026-10-02-jam-role-destination-credentials.md`

## Global Constraints

- Credential **names** only in roles/API/logs; never a credential value (AGENTS.md: secrets never hit disk/argv/logs).
- Fail closed: unknown destination, conflicting credentials across grants, unknown credential name → deny / 400.
- Old stores load: JSON keys `repos` / `repo_scoped` are ignored on decode; a role without `credentials` behaves exactly as before.
- Tests hermetic (`just test`); TDD; docs updated in the same commit as the behavior (AGENTS.md).
- Commit and push after every task (`git push`), branch `feat/jam-role-dest-creds`.

## Review Focus

1. A role listing a destination with **no** `credentials` entry still injects the destination's default `CredName` (existing deployments) — pinned in Task 1.
2. Two grants authorizing the same destination with different credentials → **403**, not first-wins — pinned in Task 1.
3. A grant override that replaces `Destinations` but not `Credentials` keeps the role's mapping for the destinations it retains — pinned in Task 1.
4. `credentials` key naming a destination the scope doesn't allow, or a value naming an unconfigured credential → 400 at role put / grant / enroll — pinned in Task 3.
5. `ParseDestinations` on malformed input (`git=`, `=cred`, `git=a,git=b`) → error, not a silently-empty map — pinned in Task 3.

---

### Task 1: Core — `Credentials` on Scope/Override; `Decide` picks the scope's credential

**Files:**
- Modify: `internal/jam/identity.go` (Scope, Override)
- Modify: `internal/jam/decide.go` (EffectiveScope, Decide, new `Scope.CredentialFor`)
- Test: `internal/jam/decide_test.go`

**Interfaces:**
- Produces: `Scope.Credentials map[string]string` (`json:"credentials,omitempty"`), `Override.Credentials map[string]string` (`json:"credentials,omitempty"`), `func (s Scope) CredentialFor(d Destination) string`.

- [ ] **Step 1: Failing tests** (append to `decide_test.go`):

```go
func TestDecideUsesRoleMappedCredential(t *testing.T) {
	git := testConfig().Destinations[1] // default cred git-pat
	s := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	dec, err := Decide(Actor{ID: "x"}, roleScopes(t, s), git, "acme/api", time.Now())
	if err != nil || dec.CredName != "git-pat-cove" || !dec.NeedCred {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
}

func TestDecideUnmappedFallsBackToDestinationDefault(t *testing.T) {
	git := testConfig().Destinations[1]
	s := Scope{Destinations: []string{"git"}, Repos: []string{"*/*"}}
	dec, err := Decide(Actor{ID: "x"}, roleScopes(t, s), git, "acme/api", time.Now())
	if err != nil || dec.CredName != "git-pat" {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
}

func TestDecideConflictingCredentialsDenied(t *testing.T) {
	git := testConfig().Destinations[1]
	scopes := roleScopes(t,
		Scope{Destinations: []string{"git"}, Repos: []string{"*/*"}, Credentials: map[string]string{"git": "pat-a"}},
		Scope{Destinations: []string{"git"}, Repos: []string{"*/*"}, Credentials: map[string]string{"git": "pat-b"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, "acme/api", time.Now()); err == nil {
		t.Fatal("conflicting credentials across grants must deny")
	}
}

func TestEffectiveScopeCredentialsOverride(t *testing.T) {
	r := Role{Name: "w", Scope: Scope{Destinations: []string{"git", "anthropic"}, Credentials: map[string]string{"git": "pat-a"}}}
	inherit := EffectiveScope(Grant{Overrides: &Override{Destinations: []string{"git"}}}, r)
	if inherit.Credentials["git"] != "pat-a" {
		t.Fatalf("credentials should inherit when override nil: %+v", inherit)
	}
	repl := EffectiveScope(Grant{Overrides: &Override{Credentials: map[string]string{"git": "pat-b"}}}, r)
	if repl.Credentials["git"] != "pat-b" {
		t.Fatalf("credentials should be replaced: %+v", repl)
	}
}
```

(These still pass `Repos`/`ownerRepo` because repo scoping is removed in Task 2, which rewrites them.)

- [ ] **Step 2:** `go test ./internal/jam -run 'Credential|Conflicting|Unmapped'` → FAIL (unknown field `Credentials`).

- [ ] **Step 3: Implement.** `identity.go` — add to `Scope` after `Destinations`:

```go
	// Credentials maps a destination name to the credential the broker injects
	// for it; a destination absent here (or mapped to "") uses its own CredName.
	Credentials map[string]string `json:"credentials,omitempty"`
```

and to `Override`:

```go
	Credentials map[string]string `json:"credentials,omitempty"` // REPLACES Scope.Credentials when non-nil
```

`decide.go` — in `EffectiveScope` add `if g.Overrides.Credentials != nil { s.Credentials = g.Overrides.Credentials }`; add

```go
// CredentialFor is the credential name this scope injects for d: the scope's
// mapping when set, else the destination's default.
func (s Scope) CredentialFor(d Destination) string {
	if c := s.Credentials[d.Name]; c != "" {
		return c
	}
	return d.CredName
}
```

and replace the loop in `Decide`:

```go
	cred, found := "", false
	for _, s := range scopes {
		if !slices.Contains(s.Destinations, dest.Name) {
			continue
		}
		if dest.RepoScoped && !repoAllowed(ownerRepo, s.Repos) {
			continue
		}
		c := s.CredentialFor(dest)
		if found && c != cred {
			return Decision{}, fmt.Errorf("actor %q: grants map destination %q to different credentials", a.ID, dest.Name)
		}
		cred, found = c, true
	}
	if !found {
		return Decision{}, fmt.Errorf("actor %q not authorized for destination %q", a.ID, dest.Name)
	}
	return Decision{Dest: dest, NeedCred: cred != "", CredName: cred, Apply: dest.Apply}, nil
```

Update the `Decide` doc comment: credential comes from the authorizing scope; disagreeing grants deny.

- [ ] **Step 4:** `go test ./internal/jam/...` → PASS.
- [ ] **Step 5:** Commit `feat(jam): role-mapped destination credentials in Scope/Decide` + push.

---

### Task 2: Remove repo scoping end to end

**Files:**
- Modify: `internal/jam/policy.go` (drop `RepoScoped`, `RepoFromPath`, `repoAllowed`, `path` import), `decide.go` (drop `ownerRepo` param + repo checks), `proxy.go` (drop repo extraction), `identity.go` (drop `Scope.Repos`, `Override.Repos`), `filestore.go` (legacy migration: drop `Repos`), `admin.go` (`GrantSummary`/`RoleBody`/`RoleSummary` drop `Repos`), `adminclient/adminclient.go`, `adminui/adminui.go` (`roleRow.Repos`), `adminui/writes.go` (repo form values, destination `repo-scoped`), templates `roster.html`/`roles.html`/`destinations.html`, `cmd/at-jam/main.go` (`--repos`, `--repo-scoped`, `repos=` in listings), `storetest/conformance.go`.
- Tests: every `_test.go` referencing `Repos`/`RepoScoped`/`RepoFromPath`/the `ownerRepo` arg (see `grep -rn -E 'Repos|RepoScoped|RepoFromPath|repo-scoped|"repos"' --include=*_test.go .`).
- Docs: `docs/usage/jam/roster.md`, `serve.md`, `ui.md`, `dispatch-runbook.md`, `comms-addressing.md`, `docs/usage/at-cove-config.md`, `docs/OVERVIEW.md` — remove repos/repo-scoped; add the upgrade note (spec §Upgrade note) to `serve.md` destinations section.

**Interfaces:**
- Produces: `func Decide(a Actor, scopes []Scope, dest Destination, now time.Time) (Decision, error)`.

- [ ] **Step 1: Failing test** in `decide_test.go` — rewrite repo tests into:

```go
func TestDecideNoRepoGate(t *testing.T) {
	git := testConfig().Destinations[1]
	if _, err := Decide(Actor{ID: "x"}, roleScopes(t, Scope{Destinations: []string{"git"}}), git, time.Now()); err != nil {
		t.Fatalf("git with no repo policy should be allowed: %v", err)
	}
}
```

and in `proxy_test.go` a request to `/git/chromedp/chromedp/info/refs` for an actor whose role has `git` → upstream receives `/chromedp/chromedp/info/refs` (adapt the existing git proxy test; drop its repo-deny case). Delete `TestRepoFromPath`, `TestDecideNoCrossGrantRepoBleed`, repo override tests; convert Task 1 tests to the new signature (drop `Repos`, drop the `"acme/api"` arg).

- [ ] **Step 2:** `go build ./... && go vet ./...` → compile errors list = the edit list. Work through it.
- [ ] **Step 3: Implement** the removals above. `proxy.go` becomes `dec, err := Decide(actor, b.resolveScopes(actor), dest, b.now())`. `filestore.go` `legacyIdentity` keeps `Repos` **out** (field deleted; JSON ignored); migration compares only `Destinations`:

```go
		if !sameStrings(existing.Scope.Destinations, li.Destinations) {
			g.Overrides = &Override{Destinations: nonNilStrings(li.Destinations)}
		}
```

CLI listings: `role list` drops `repos=%s`; roster drops `repos=%s`. UI templates: drop the Repos columns/inputs and the repo-scoped checkbox/column.
- [ ] **Step 4:** `just test && just lint` → PASS; `grep -rn -iE 'repo.?scoped|\bRepos\b|--repos' --include=*.go --include=*.html .` → no hits.
- [ ] **Step 5:** Docs per Files list, then commit `refactor(jam)!: remove broker repo scoping` + push.

---

### Task 3: `name=cred` syntax, write-time validation, admin API + client

**Files:**
- Create: `internal/jam/destspec.go`, `internal/jam/destspec_test.go`
- Modify: `internal/jam/admin.go` (RoleBody/RoleSummary/GrantSummary `Credentials`; validate in role put, grant add, enroll), `internal/jam/adminclient/adminclient.go` (PutRole/ListRoles carry `Credentials`)
- Test: `internal/jam/admin_test.go`, `internal/jam/adminclient/adminclient_test.go`
- Docs: `docs/usage/jam/roster.md` (role scope: credentials mapping; API field)

**Interfaces:**
- Produces:
  - `func ParseDestinations(csv string) (dests []string, creds map[string]string, err error)` — `nil` creds when no `=` used.
  - `func FormatDestinations(dests []string, creds map[string]string) string` — inverse, for listings/UI.
  - `func ValidateCredentials(s Scope, credExists func(string) bool) error`.

- [ ] **Step 1: Failing tests** (`destspec_test.go`):

```go
func TestParseDestinations(t *testing.T) {
	d, c, err := ParseDestinations(" git=git-pat-cove , anthropic ")
	if err != nil || !slices.Equal(d, []string{"git", "anthropic"}) || c["git"] != "git-pat-cove" || len(c) != 1 {
		t.Fatalf("got %v %v %v", d, c, err)
	}
	if _, c, _ := ParseDestinations("git,anthropic"); c != nil {
		t.Fatalf("no mappings → nil map, got %v", c)
	}
	for _, bad := range []string{"git=", "=pat", "git=a,git=b", "git,git"} {
		if _, _, err := ParseDestinations(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestFormatDestinationsRoundTrip(t *testing.T) {
	in := "git=git-pat-cove,anthropic"
	d, c, _ := ParseDestinations(in)
	if got := FormatDestinations(d, c); got != in {
		t.Fatalf("FormatDestinations = %q", got)
	}
}

func TestValidateCredentials(t *testing.T) {
	known := func(n string) bool { return n == "git-pat-cove" }
	ok := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	if err := ValidateCredentials(ok, known); err != nil {
		t.Fatal(err)
	}
	notAllowed := Scope{Destinations: []string{"anthropic"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	unknown := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "typo"}}
	for _, s := range []Scope{notAllowed, unknown} {
		if ValidateCredentials(s, known) == nil {
			t.Errorf("%+v: want error", s)
		}
	}
}
```

`admin_test.go`: POST `/admin/roles` with `"credentials":{"git":"nope"}` → 400; with a configured name → 201 and GET `/admin/roles` echoes `credentials`; POST `/admin/actors/{id}/grants` with `overrides.credentials` naming a destination not in the effective scope → 400. `adminclient_test.go`: PutRole/ListRoles round-trip `Credentials`.

- [ ] **Step 2:** `go test ./internal/jam/...` → FAIL (undefined).
- [ ] **Step 3: Implement** `destspec.go`:

```go
package jam

import (
	"fmt"
	"slices"
	"strings"
)

// ParseDestinations parses the operator syntax "git=git-pat-cove,anthropic": a
// comma-separated list of destination names, each optionally mapped to the
// credential the broker injects for it. creds is nil when nothing is mapped.
func ParseDestinations(csv string) (dests []string, creds map[string]string, err error) {
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		name, cred, mapped := strings.Cut(p, "=")
		name, cred = strings.TrimSpace(name), strings.TrimSpace(cred)
		if name == "" || (mapped && cred == "") {
			return nil, nil, fmt.Errorf("bad destination entry %q (want name or name=credential)", p)
		}
		if slices.Contains(dests, name) {
			return nil, nil, fmt.Errorf("destination %q listed twice", name)
		}
		dests = append(dests, name)
		if mapped {
			if creds == nil {
				creds = map[string]string{}
			}
			creds[name] = cred
		}
	}
	return dests, creds, nil
}

// FormatDestinations renders dests/creds back into ParseDestinations syntax.
func FormatDestinations(dests []string, creds map[string]string) string {
	parts := make([]string, 0, len(dests))
	for _, d := range dests {
		if c := creds[d]; c != "" {
			d += "=" + c
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, ",")
}

// ValidateCredentials checks a scope's destination→credential map at write
// time: every key must be an allowed destination and every non-empty value a
// configured credential, so a typo fails at the admin API, not mid-request.
func ValidateCredentials(s Scope, credExists func(string) bool) error {
	for d, c := range s.Credentials {
		if !slices.Contains(s.Destinations, d) {
			return fmt.Errorf("credentials maps %q, which is not one of the scope's destinations", d)
		}
		if c != "" && !credExists(c) {
			return fmt.Errorf("credential %q (for %q) does not resolve to a configured credential", c, d)
		}
	}
	return nil
}
```

`admin.go`: add `Credentials map[string]string \`json:"credentials,omitempty"\`` to `RoleBody`, `RoleSummary`, `GrantSummary`; role put builds `Scope{…, Credentials: b.Credentials}` and calls `ValidateCredentials(role.Scope, credExists)` → 400; GET roles and `RosterSummaries` fill `Credentials`. Grant add and enroll: when `b.Overrides != nil`, look up the role (`store.GetRole(orDefaultProject(b.Project), b.Role)`; 400 if missing) and `ValidateCredentials(EffectiveScope(Grant{Overrides: b.Overrides}, role), credExists)` → 400. `adminclient`: PutRole sends `Credentials: r.Scope.Credentials`; ListRoles reads `rs.Credentials`.
- [ ] **Step 4:** `just test` → PASS.
- [ ] **Step 5:** Docs (roster.md), commit `feat(jam): name=cred destination syntax + write-time credential validation` + push.

---

### Task 4: CLI and admin UI speak `name=cred`

**Files:**
- Modify: `cmd/at-jam/main.go` (`role add --destinations` via `jam.ParseDestinations`; `role list` and roster print `jam.FormatDestinations`), `internal/jam/adminui/writes.go` (role form + `overridesFrom` parse via `jam.ParseDestinations`, validate with `jam.ValidateCredentials`), `internal/jam/adminui/adminui.go` (`roleRow.Destinations` → display string), templates `roles.html`/`roster.html` (render the formatted string; placeholder `destinations (e.g. git=git-pat,anthropic)`)
- Test: `cmd/at-jam/main_test.go`, `internal/jam/adminui/writes_test.go`
- Docs: `docs/usage/jam/roster.md` (CLI example), `docs/usage/jam/ui.md`

**Interfaces:**
- Consumes: `jam.ParseDestinations`, `jam.FormatDestinations`, `jam.ValidateCredentials` (Task 3).

- [ ] **Step 1: Failing tests:** `main_test.go` — `role add --name w --destinations git=git-pat-cove,anthropic` against the fake admin server sends body `destinations:["git","anthropic"], credentials:{"git":"git-pat-cove"}`; `--destinations git=` exits 2 with an error. `writes_test.go` — POST `/ui/roles` with `destinations=git=git-pat` stores `Credentials["git"]=="git-pat"` (credExists stub true); with an unknown credential → 400; roles table renders `git=git-pat`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.** CLI:

```go
		ds, creds, err := jam.ParseDestinations(*dests)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam role add:", err)
			return 2
		}
		r := jam.Role{
			Name: *name, Kit: *kitName,
			Scope: jam.Scope{Destinations: ds, Credentials: creds, Addressing: splitCSV(*addressing), TTL: *ttl},
```

flag help: `"comma-separated destination names, each optionally name=credential"`. Listings print `dests=%s` with `jam.FormatDestinations(...)`. UI: `overridesFrom(dests string) (*jam.Override, error)` returns `&jam.Override{Destinations: d, Credentials: c}` or nil when empty; role form likewise; both run `jam.ValidateCredentials` (for grants/enroll on the effective scope) and `renderError(…400…)` on failure.
- [ ] **Step 4:** `just test && just lint` → PASS.
- [ ] **Step 5:** Docs, commit `feat(at-jam): name=cred destinations in CLI and admin UI` + push.

---

### Task 5: `gh` through the broker

**Files:**
- Modify: `internal/jam/proxy.go` (`presentedToken`: bearer also accepts `token <x>`)
- Test: `internal/jam/proxy_test.go`
- Docs: `docs/usage/jam/serve.md` (Destinations: a "GitHub API for `gh`" example — two destinations `github-api` route `/api/v3/` and `github-graphql` route `/api/` → `https://api.github.com`, `--identity-in bearer --apply bearer`; studio env `GH_HOST=<jam host>`, `GH_ENTERPRISE_TOKEN=$AT_JAM_IDENTITY_TOKEN`; the role maps both to the project's PAT)

- [ ] **Step 1: Failing test:**

```go
func TestPresentedTokenAcceptsGHTokenScheme(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v3/user", nil)
	r.Header.Set("Authorization", "token abc")
	if tok, ok := presentedToken(r, ApplyBearer); !ok || tok != "abc" {
		t.Fatalf("got %q %v", tok, ok)
	}
}
```

plus a proxy test: route `/api/` upstream test server; request `/api/graphql` → upstream path `/graphql` with `Authorization: Bearer <cred>`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement:**

```go
	case ApplyBearer:
		// "token <x>" is what gh sends to a GitHub Enterprise host (GH_HOST=jam).
		auth := r.Header.Get("Authorization")
		for _, scheme := range []string{"Bearer ", "token "} {
			if s, ok := strings.CutPrefix(auth, scheme); ok && s != "" {
				return s, true
			}
		}
```
- [ ] **Step 4:** `just test` → PASS.
- [ ] **Step 5:** Docs, commit `feat(jam): accept gh's token auth scheme; document gh via the broker` + push.

---

### Task 6: Docs audit and whole-branch check

- [ ] Run the docs-audit skill; fix findings.
- [ ] `just test && just lint`; `grep -rn -iE 'repo.?scoped|--repos' docs --include=*.md | grep -v superpowers` → no hits.
- [ ] Commit any fixes + push; report the branch for PR.
