# Kit build drift + per-turn connector refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild studio-kit images when Jam's baked-in payload (or JamHost / launcher key / default base) changes, and make every agent turn in a running studio use the role's *current* connector, with Jam showing which studios are stale.

**Architecture:** Part 1 is launcher-only: the image tag gains an assembly fingerprint computed once at `launcher.New` from `install.AtCoveIdentity()` + `basedigest.DefaultRef()` + `JamHost` + `PublicKey`. Part 2 is "B-pull": cove-master's `agentrun` fetches `GET /connector` before each `claude` spawn, sets that spawn's env (and git routing), and reports the applied connector's fingerprint up the Attach stream; Jam records it on the Instance and derives `ok | stale | unknown | error` on read.

**Tech Stack:** Go, grpc + protobuf (buf-generated `attachpb`), `just`.

**Spec:** `docs/superpowers/specs/2026-10-02-kit-build-drift-design.md`

## Global Constraints

- Tests are hermetic (no Docker/network/VM); TDD — failing test first.
- Secrets never hit disk, argv, or logs. The identity token never appears in `AT_JAM_CONNECTOR`, in a log line, or in a fingerprint input. Connector *values* are never logged — keys/fingerprints/counts only.
- Image tag format: `cove-kit:<kitDigest[:32]>-<asmDigest[:32]>`; a digest shorter than 32 chars is used whole. Tag ≤ 128 chars.
- New cove-master env names: `AT_JAM_CONNECTOR` (raise-time connector JSON, no token) and `AT_JAM_BASE_URL` (`https://<JamHost>`).
- Proto package stays `harbor.attach.v1`; new oneof field `ConnectorApplied connector = 4` on `StatusUp`, message `ConnectorApplied { string fingerprint = 1; }`.
- `agentrun` and `covemaster` never import `internal/jam` (subpackages `internal/jam/snippet` and `internal/jam/attach/attachpb` are fine).
- Connector status strings: `ok`, `stale`, `unknown`, `error`.
- No task is complete until the docs it changes are updated in the same branch (AGENTS.md).
- Toolchain: in this sandbox run go with `GOPROXY=https://proxy.golang.org,direct` (the session default `direct` cannot fetch `google.golang.org/*`).

## Review Focus

1. **A destination env key removed between turns** must be *absent* from the next spawn's env (not left over from cove-master's inherited raise-time env). → Task 4, `TestConnectorDropsRemovedKeys`.
2. **`/connector` fails (401 after revoke, 409 conflict, network)** must not stop the turn: spawn with the last-applied connector, no new report. → Task 4, `TestConnectorFetchFailureKeepsLast`.
3. **A kit digest shorter than 32 chars** (legacy/test refs like `cafef00d`) must not panic tag building. → Task 1, `TestImageTagShortDigest`.
4. **A `ConnectorApplied` reported while the Attach stream is down** must reach Jam after reconnect. → Task 3, `TestConnectorAppliedResentOnReconnect`.
5. **A studio raised before this change** (`Instance.Connector == ""`) must show `unknown`, never `stale`; a role whose destinations conflict must show `error`, not crash the list. → Task 6, `TestCoveSummariesConnectorStatus`.

---

### Task 1: Assembly fingerprint in the studio-kit image tag

**Files:**
- Modify: `internal/jam/launcher/launcher.go` (Launcher struct, `New`, `Raise` tag use)
- Modify: `internal/jam/launcher/inventory.go` (`imageTag` → method-backed; `backendInventory` takes a tag func)
- Modify: `internal/jam/launcher/prepare.go` (use `l.imageTag`, log `asm`)
- Create: `internal/jam/launcher/asm.go`
- Test: `internal/jam/launcher/asm_test.go`; update `launcher_test.go`, `prepare_test.go`, `inventory_test.go` expectations
- Docs: `docs/usage/jam/kits.md`, `docs/usage/jam/coves.md` (kit-prepare paragraph)

**Interfaces:**
- Consumes: `install.AtCoveIdentity() (string, error)`, `basedigest.DefaultRef() string`
- Produces: `func asmDigest(identity, defaultRef, jamHost string, pub []byte) string`; `func (l *Launcher) imageTag(r KitRef) string`; `func tagFor(kitDigest, asm string) string`

- [ ] **Step 1: Write the failing tests** — `internal/jam/launcher/asm_test.go`:

```go
package launcher

import (
	"strings"
	"testing"
)

func TestTagForFormat(t *testing.T) {
	kit := strings.Repeat("a", 64)
	asm := strings.Repeat("b", 64)
	got := tagFor(kit, asm)
	want := "cove-kit:" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 32)
	if got != want {
		t.Fatalf("tagFor = %q, want %q", got, want)
	}
	if len(got) > 128 {
		t.Fatalf("tag %d chars > docker's 128", len(got))
	}
}

func TestImageTagShortDigest(t *testing.T) {
	if got := tagFor("cafef00d", "beef"); got != "cove-kit:cafef00d-beef" {
		t.Fatalf("short digests must be used whole, got %q", got)
	}
}

func TestAsmDigestCoversEveryInput(t *testing.T) {
	base := asmDigest("id", "ref", "jam.a", []byte("key"))
	if base != asmDigest("id", "ref", "jam.a", []byte("key")) {
		t.Fatal("asmDigest not deterministic")
	}
	for name, d := range map[string]string{
		"identity": asmDigest("id2", "ref", "jam.a", []byte("key")),
		"base":     asmDigest("id", "ref2", "jam.a", []byte("key")),
		"jamhost":  asmDigest("id", "ref", "jam.b", []byte("key")),
		"key":      asmDigest("id", "ref", "jam.a", []byte("key2")),
	} {
		if d == base {
			t.Fatalf("changing %s did not change the digest", name)
		}
	}
	// Length-prefixed: shifting a boundary must not collide.
	if asmDigest("ab", "c", "h", nil) == asmDigest("a", "bc", "h", nil) {
		t.Fatal("field boundary shift collided")
	}
}

func TestLauncherTagDependsOnJamHostAndKey(t *testing.T) {
	ref := KitRef{ID: "web", Version: 1, Digest: strings.Repeat("c", 64)}
	a := New(Config{JamHost: "jam.a", PublicKey: []byte("k1")})
	b := New(Config{JamHost: "jam.b", PublicKey: []byte("k1")})
	c := New(Config{JamHost: "jam.a", PublicKey: []byte("k2")})
	if a.imageTag(ref) == b.imageTag(ref) || a.imageTag(ref) == c.imageTag(ref) {
		t.Fatalf("tag ignores JamHost/key: %s %s %s", a.imageTag(ref), b.imageTag(ref), c.imageTag(ref))
	}
	if !strings.HasPrefix(a.imageTag(ref), "cove-kit:"+strings.Repeat("c", 32)+"-") {
		t.Fatalf("tag %q lacks the kit digest prefix", a.imageTag(ref))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ -run 'TagFor|ImageTagShort|AsmDigest|LauncherTag' -v`
Expected: FAIL — `undefined: tagFor`, `undefined: asmDigest`, `a.imageTag undefined`.

- [ ] **Step 3: Implement** — `internal/jam/launcher/asm.go`:

```go
package launcher

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/install"
)

// tagHalf is how many hex chars of each digest the image tag keeps: docker caps
// a tag at 128 chars, and 2×32 (+ "-") fits with 128 bits of each digest.
const tagHalf = 32

// asmDigest is the launcher's assembly fingerprint: everything a studio-kit
// build bakes in that is NOT in the kit's own BuildDigest — at-jam's embedded
// payload (the sealed hardening layer + at-task/at-switchboard/cove-master,
// hashed by install.AtCoveIdentity, the same identity the repo-kit currency check
// uses), the blessed default base (an omitted base builds FROM it; a context base
// descends from it), the Jam host (baked into the infra egress list) and the
// launcher's public key (baked into authorized_keys). Length-prefixed fields.
func asmDigest(identity, defaultRef, jamHost string, pub []byte) string {
	h := sha256.New()
	for _, f := range [][]byte{[]byte(identity), []byte(defaultRef), []byte(jamHost), pub} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// currentAsmDigest computes asmDigest for this binary and cfg. An identity error
// (an unreadable embed — never expected) degrades to an empty identity: the tag
// still varies with the other inputs, and the error is logged by the caller.
func currentAsmDigest(cfg Config) (string, error) {
	id, err := install.AtCoveIdentity()
	return asmDigest(id, basedigest.DefaultRef(), cfg.JamHost, cfg.PublicKey), err
}

// tagFor names a studio-kit image by its kit build-digest AND the launcher's
// assembly fingerprint, so a change to either rebuilds.
func tagFor(kitDigest, asm string) string {
	return "cove-kit:" + trim(kitDigest) + "-" + trim(asm)
}

func trim(d string) string {
	if len(d) > tagHalf {
		return d[:tagHalf]
	}
	return d
}

// imageTag is the tag this launcher builds, inventories and runs a kit under.
func (l *Launcher) imageTag(r KitRef) string { return tagFor(r.Digest, l.asm) }
```

In `launcher.go`: add field `asm string` to `Launcher`; in `New`, after the defaults and before inventory wiring:

```go
	l := &Launcher{cfg: cfg, inflight: map[string]*sync.Mutex{}}
	asm, err := currentAsmDigest(cfg)
	if err != nil {
		cfg.Log.Warn("launcher: at-jam build identity unreadable; image tags key on the other inputs only", "error", err.Error())
	}
	l.asm = asm
	l.inv = cfg.Inventory
	if l.inv == nil {
		l.inv = backendInventory{ops: cfg.Ops, tag: l.imageTag}
	}
```

In `Raise` replace `image, digest := imageTag(spec.Kit), ""` with `image, digest := l.imageTag(spec.Kit), ""` and update the two comments that say `cove-kit:<build-digest>` to `cove-kit:<build-digest>-<asm>`.

In `inventory.go` delete the package-level `imageTag` and change:

```go
// backendInventory is the default Inventory: it asks the substrate backend
// whether the kit's tagged image exists. ... (keep the existing COV-217 text)
type backendInventory struct {
	ops imageChecker
	tag func(KitRef) string // the launcher's imageTag, so inventory and build agree
}

// Has reports whether the kit's image (under this launcher's tag) exists.
func (b backendInventory) Has(ref KitRef) (bool, error) { return b.ops.HasKitImage(b.tag(ref)) }
```

In `prepare.go` replace both `imageTag(ref)` with `l.imageTag(ref)`, and add `"asm", shortAsm(l.asm)` to the "prepared studio kit" log line, with (in `asm.go`):

```go
// shortAsm is the fingerprint prefix logged when a kit is prepared, so an
// operator can see that a rebuild came from a Jam-side change.
func shortAsm(a string) string { return a[:min(12, len(a))] }
```

Update the `PrepareKit` doc comment's `cove-kit:<build-digest>` to `cove-kit:<build-digest>-<asm>`.

- [ ] **Step 4: Fix existing test expectations**

`inventory_test.go`: construct `backendInventory{ops: c, tag: func(r KitRef) string { return tagFor(r.Digest, "") }}` and expect `"cove-kit:deadbeef-"`.
`prepare_test.go` lines ~129/181: compare `ops.builtTag != l.imageTag(ref)` (the test's launcher variable) instead of `"cove-kit:"+ref.Digest`; fix the comment at ~109.
`launcher_test.go` ~230: `ops.runImage != l.imageTag(spec.Kit)`; fix comments at ~22/195/213.

- [ ] **Step 5: Run the package**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ ./internal/install/ -v 2>&1 | tail -30`
Expected: PASS (also check no import cycle: `go build ./...`).

- [ ] **Step 6: Docs** — `docs/usage/jam/kits.md`: in the paragraph ending "a prompt- or secrets-only edit reuses the cached image." append: "The tag also carries the launcher's **assembly fingerprint** — at-jam's embedded payload (hardening layer, at-task / at-switchboard / cove-master), the blessed default base, the Jam host and the launcher key — so upgrading Jam (or moving it, or rotating its key) rebuilds each kit lazily on its next raise; running studios keep their image until re-raised. Superseded `cove-kit:*` images are not yet garbage-collected." In `docs/usage/jam/coves.md` (kit-prepare section) change "The image is tagged by the kit's build-digest, so a prompt-only edit reuses the cached image." to "The image is tagged by the kit's build-digest plus the launcher's assembly fingerprint ([kits.md](kits.md#the-studiokit)), so a prompt-only edit reuses the cached image while a Jam upgrade rebuilds it." Bump `updated:` frontmatter to 2026-10-02 where needed.

- [ ] **Step 7: Commit**

```bash
git add internal/jam/launcher docs/usage/jam/kits.md docs/usage/jam/coves.md
git commit -m "feat(jam): key studio-kit images on the launcher's assembly fingerprint"
```

---

### Task 2: snippet — connector fingerprint + shared git helper value

**Files:**
- Modify: `internal/jam/snippet/connector.go`, `internal/jam/snippet/snippet.go`
- Test: `internal/jam/snippet/connector_test.go` (append)

**Interfaces:**
- Produces: `func Fingerprint(c Connector) string` (64 hex); `func GitHelper() string` (the credential-helper value, unquoted, for argv use); `const TokenVar = "AT_JAM_IDENTITY_TOKEN"` is NOT exported — keep internal; agentrun gets the token from config.

- [ ] **Step 1: Failing tests** (append to `connector_test.go`):

```go
func TestFingerprintStableAndTokenFree(t *testing.T) {
	a := Connector{Env: map[string]string{"B": "{base}/x", "A": "{token}"}, GitRoute: "/git/"}
	b := Connector{Env: map[string]string{"A": "{token}", "B": "{base}/x"}, GitRoute: "/git/"}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint depends on map order")
	}
	if Fingerprint(Connector{}) != Fingerprint(Connector{Env: map[string]string{}}) {
		t.Fatal("nil and empty env must fingerprint alike")
	}
	if Fingerprint(a) == Fingerprint(Connector{Env: a.Env}) {
		t.Fatal("git route not covered")
	}
	if len(Fingerprint(a)) != 64 {
		t.Fatalf("want 64 hex, got %q", Fingerprint(a))
	}
}

func TestGitHelperMatchesRenderedConfig(t *testing.T) {
	// GitConfig shell-quotes the helper; GitHelper is the same value unquoted.
	if !strings.Contains(Connector{GitRoute: "/git/"}.GitConfig("https://j"), "'"+GitHelper()+"'") {
		t.Fatalf("GitHelper %q is not the value GitConfig renders", GitHelper())
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/jam/snippet/ -run 'Fingerprint|GitHelper' -v` → FAIL (undefined).

- [ ] **Step 3: Implement.** In `snippet.go` split the helper constant:

```go
	// gitHelperValue is a `!`-prefixed shell helper git runs with the operation
	// as $1; it emits the identity as username + the env-only token as password,
	// only for `get`. It reads the new name, falling back to the deprecated one.
	gitHelperValue = `!f() { test "$1" = get && echo username=x-access-token && echo password=${` + tokenVar + `:-$` + legacyTokenVar + `}; }; f`
	// gitHelper is gitHelperValue single-quoted for a shell snippet, so nothing
	// expands until git invokes it in the cove.
	gitHelper = `'` + gitHelperValue + `'`
```

In `connector.go`:

```go
// Fingerprint identifies a connector's content (templates + git route, never a
// token — a Connector holds none): sha256 of its JSON, whose map keys
// encoding/json sorts, so it is order-independent; nil and empty Env coincide
// (omitempty). cove-master reports it after applying a connector and Jam compares
// it with the role's current one, so both sides must use this function.
func Fingerprint(c Connector) string {
	b, _ := json.Marshal(c) // map[string]string + string: never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// GitHelper is the git credential-helper value GitConfig installs, unquoted —
// for callers that set it via argv (cove-master's per-turn git refresh).
func GitHelper() string { return gitHelperValue }
```

(add `crypto/sha256`, `encoding/hex` imports).

- [ ] **Step 4: Run** `go test ./internal/jam/snippet/ -v` → PASS (all, incl. existing render tests).

- [ ] **Step 5: Commit** `git commit -am "feat(snippet): connector Fingerprint and unquoted GitHelper"`

---

### Task 3: Attach wire + covemaster `ConnectorApplied`

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto`; regenerate `internal/jam/attach/attachpb/*.pb.go` via `just buf-gen`
- Modify: `internal/covemaster/covemaster.go` (Handle, msg helper), `internal/covemaster/client.go` (record + send + resend)
- Modify: `internal/agentrun/workload_test.go` (`recordHandle` gains the method so the package compiles)
- Test: `internal/covemaster/connector_test.go`

**Interfaces:**
- Produces: `Handle.ConnectorApplied(fingerprint string)` (never blocks; latest wins); `attachpb.StatusUp_Connector{Connector: &attachpb.ConnectorApplied{Fingerprint: fp}}`.

- [ ] **Step 1: Proto** — in `attach.proto`:

```proto
message StatusUp {
  oneof msg {
    Activity         status    = 1;
    Heartbeat        heartbeat = 2;
    SessionEvent     event     = 3;
    ConnectorApplied connector = 4;
  }
}

// ConnectorApplied reports the fingerprint (snippet.Fingerprint) of the
// connector cove-master applied to the agent's most recent spawn. Jam compares it
// with the role's current connector to flag a stale studio.
message ConnectorApplied { string fingerprint = 1; }
```

Run: `GOPROXY=https://proxy.golang.org GOTOOLCHAIN=local just buf-gen` → regenerated files; `go build ./...` passes.

- [ ] **Step 2: Failing test** — `internal/covemaster/connector_test.go`. Extend `fakeRuntime` as shown after the tests, then:

```go
package covemaster

import (
	"context"
	"testing"
	"time"
)

// connWorkload reports a connector fingerprint, then blocks until released.
type connWorkload struct {
	fp      string
	release chan struct{}
}

func (w *connWorkload) Run(ctx context.Context, h Handle) error {
	h.Report(Running)
	h.ConnectorApplied(w.fp)
	select {
	case <-w.release:
	case <-ctx.Done():
	}
	return nil
}
func (w *connWorkload) Control(Control) {}

func (f *fakeRuntime) connectorList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.connectors...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConnectorAppliedSent(t *testing.T) {
	f := &fakeRuntime{autoAck: true}
	c := fakeClient(t, f, nil)
	w := &connWorkload{fp: "fp-1", release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), w) }()
	waitFor(t, func() bool { l := f.connectorList(); return len(l) > 0 && l[len(l)-1] == "fp-1" })
	close(w.release)
	<-done
}

func TestConnectorAppliedResentOnReconnect(t *testing.T) {
	// The server ends the RPC on the first ConnectorApplied it sees; the client
	// must reconnect and re-send the latest fingerprint without the workload
	// reporting it again.
	f := &fakeRuntime{autoAck: true, dropOnConnector: true}
	c := fakeClient(t, f, nil)
	w := &connWorkload{fp: "fp-1", release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), w) }()
	waitFor(t, func() bool { return len(f.connectorList()) >= 2 })
	close(w.release)
	<-done
	for _, fp := range f.connectorList() {
		if fp != "fp-1" {
			t.Fatalf("unexpected fingerprint %q", fp)
		}
	}
}
```

The `fakeRuntime` additions in `events_test.go`: fields `connectors []string` and `dropOnConnector bool`, and the branch

```go
		case *attachpb.StatusUp_Connector:
			f.mu.Lock()
			f.connectors = append(f.connectors, x.Connector.GetFingerprint())
			drop := f.dropOnConnector
			f.dropOnConnector = false
			f.mu.Unlock()
			if drop {
				return nil // server ends the RPC → client reconnects
			}
```

Also add to `agentrun/workload_test.go`'s `recordHandle`:

```go
	connectors []string // in the struct

func (h *recordHandle) ConnectorApplied(fp string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connectors = append(h.connectors, fp)
}
```

- [ ] **Step 3: Run** `go test ./internal/covemaster/ -run Connector -v` → FAIL (`c.ConnectorApplied undefined`).

- [ ] **Step 4: Implement.** `covemaster.go` — add to `Handle`:

```go
	// ConnectorApplied records the fingerprint of the connector the workload
	// applied to its latest agent spawn. It never blocks; the newest value wins
	// and is re-sent on every (re)connect.
	ConnectorApplied(fingerprint string)
```

and

```go
func connectorMsg(fp string) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Connector{Connector: &attachpb.ConnectorApplied{Fingerprint: fp}}}
}
```

`client.go` — fields `connector string` (guarded by `mu`) and `connCh chan string` (buffer 1, made in `New`). Method:

```go
// ConnectorApplied implements Handle: remember the latest fingerprint (for
// re-send on reconnect) and coalesce it onto connCh for the live session.
func (c *Client) ConnectorApplied(fp string) {
	c.mu.Lock()
	c.connector = fp
	c.mu.Unlock()
	select {
	case c.connCh <- fp:
	default:
		select { // drop the stale pending value, then retry once
		case <-c.connCh:
		default:
		}
		select {
		case c.connCh <- fp:
		default:
		}
	}
}
```

In `session`, right after the latest-activity re-send block:

```go
	c.mu.Lock()
	conn := c.connector
	c.mu.Unlock()
	if conn != "" {
		if err := stream.Send(connectorMsg(conn)); err != nil {
			return classify(ctx, err, recvErr)
		}
	}
```

and in the main `select`:

```go
		case fp := <-c.connCh:
			if err := stream.Send(connectorMsg(fp)); err != nil {
				return classify(ctx, err, recvErr)
			}
```

- [ ] **Step 5: Run** `go test ./internal/covemaster/ ./internal/agentrun/ ./internal/jam/attach/... -v 2>&1 | tail -20` → PASS.

- [ ] **Step 6: Commit** `git add -A internal/jam/attach internal/covemaster internal/agentrun && git commit -m "feat(attach): ConnectorApplied status from cove-master"`

---

### Task 4: agentrun — per-turn connector refresh

**Files:**
- Create: `internal/agentrun/connector.go`
- Modify: `internal/agentrun/spawner.go` (`env []string` param), `internal/agentrun/workload.go` (Config + Run)
- Modify: `internal/agentrun/spawner_test.go`, `internal/agentrun/workload_test.go` (fakeSpawner signature; records env)
- Test: `internal/agentrun/connector_test.go`

**Interfaces:**
- Consumes: `snippet.Connector`, `snippet.Fingerprint`, `snippet.GitHelper`, `snippet.Fetch(hc *http.Client, baseURL, token string) (Connector, error)`; `covemaster.Handle.ConnectorApplied`
- Produces:

```go
type ConnectorSource interface {
	Fetch(ctx context.Context) (snippet.Connector, error)
}
type GitRouter interface {
	// Route points https://github.com/ at baseURL+newRoute (none when ""),
	// removing oldRoute's rewrite; the credential helper is kept iff newRoute != "".
	Route(baseURL, oldRoute, newRoute string) error
}
type ConnectorConfig struct {
	Source  ConnectorSource
	Git     GitRouter            // nil → execGit
	BaseURL string               // https://<jam host>
	Token   string               // the identity token (expanded in memory only)
	Initial snippet.Connector    // the raise-time connector (AT_JAM_CONNECTOR)
	Environ func() []string      // nil → os.Environ
}
// Spawner.Spawn(ctx, bin, args, dir, env []string, stdout io.Writer) — env nil inherits cove-master's.
```

- [ ] **Step 1: Failing tests** — `internal/agentrun/connector_test.go`:

```go
package agentrun

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

type fakeSource struct {
	c   snippet.Connector
	err error
}

func (f *fakeSource) Fetch(context.Context) (snippet.Connector, error) { return f.c, f.err }

type fakeGit struct{ calls [][3]string }

func (g *fakeGit) Route(base, oldR, newR string) error {
	g.calls = append(g.calls, [3]string{base, oldR, newR})
	return nil
}

func newRefresher(src *fakeSource, git *fakeGit, initial snippet.Connector, environ []string) *connectorRefresher {
	return newConnectorRefresher(ConnectorConfig{
		Source: src, Git: git, BaseURL: "https://jam.example", Token: "tok-XYZ",
		Initial: initial, Environ: func() []string { return environ },
	}, nil)
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestConnectorAppliesFetched(t *testing.T) {
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "ANTHROPIC_API_KEY": "{token}"}}}
	r := newRefresher(src, &fakeGit{}, snippet.Connector{}, []string{"PATH=/bin"})
	env, fp, changed := r.prepare(context.Background())
	m := envMap(env)
	if m["GH_HOST"] != "jam.example" || m["ANTHROPIC_API_KEY"] != "tok-XYZ" || m["PATH"] != "/bin" {
		t.Fatalf("env = %v", m)
	}
	if fp != snippet.Fingerprint(src.c) || !changed {
		t.Fatalf("fp=%q changed=%v", fp, changed)
	}
	if _, fp2, changed2 := r.prepare(context.Background()); fp2 != fp || changed2 {
		t.Fatal("unchanged connector must not report again")
	}
}

func TestConnectorDropsRemovedKeys(t *testing.T) {
	initial := snippet.Connector{Env: map[string]string{"OLD_VAR": "x", "GH_HOST": "{host}"}}
	// cove-master's own env still carries the raise-time values.
	environ := []string{"PATH=/bin", "OLD_VAR=x", "GH_HOST=jam.example"}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	r := newRefresher(src, &fakeGit{}, initial, environ)
	env, _, _ := r.prepare(context.Background())
	if _, ok := envMap(env)["OLD_VAR"]; ok {
		t.Fatalf("removed key leaked into the spawn env: %v", env)
	}
	if n := slices.IndexFunc(env, func(s string) bool { return strings.HasPrefix(s, "GH_HOST=") }); n < 0 {
		t.Fatal("kept key missing")
	}
	// No duplicates of a connector key.
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_HOST=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("GH_HOST appears %d times", count)
	}
}

func TestConnectorFetchFailureKeepsLast(t *testing.T) {
	initial := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}
	src := &fakeSource{err: errors.New("401 unknown identity")}
	git := &fakeGit{}
	r := newRefresher(src, git, initial, []string{"PATH=/bin"})
	env, fp, changed := r.prepare(context.Background())
	if envMap(env)["GH_HOST"] != "jam.example" {
		t.Fatalf("fallback env = %v", env)
	}
	// First turn reports what it actually applied (the initial connector).
	if fp != snippet.Fingerprint(initial) || !changed {
		t.Fatalf("fp=%q changed=%v", fp, changed)
	}
	if _, _, changed := r.prepare(context.Background()); changed {
		t.Fatal("a repeated failure must not re-report")
	}
	if len(git.calls) != 0 {
		t.Fatalf("git rewritten on failure: %v", git.calls)
	}
}

func TestConnectorGitOnlyOnRouteChange(t *testing.T) {
	initial := snippet.Connector{GitRoute: "/git/"}
	src := &fakeSource{c: snippet.Connector{GitRoute: "/git/"}}
	git := &fakeGit{}
	r := newRefresher(src, git, initial, nil)
	r.prepare(context.Background())
	if len(git.calls) != 0 {
		t.Fatalf("same route rewrote git: %v", git.calls)
	}
	src.c = snippet.Connector{GitRoute: "/git2/"}
	r.prepare(context.Background())
	if len(git.calls) != 1 || git.calls[0] != [3]string{"https://jam.example", "/git/", "/git2/"} {
		t.Fatalf("git calls = %v", git.calls)
	}
}

func TestConnectorNeverLogsToken(t *testing.T) {
	var buf strings.Builder
	src := &fakeSource{err: errors.New("boom")}
	r := newConnectorRefresher(ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://j", Token: "tok-XYZ",
		Environ: func() []string { return nil }}, newTestLogger(&buf))
	r.prepare(context.Background())
	if strings.Contains(buf.String(), "tok-XYZ") {
		t.Fatalf("token logged: %s", buf.String())
	}
}
```

with, in the same file:

```go
func newTestLogger(w *strings.Builder) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
```

(import `log/slog`).

And in `workload_test.go`, change `fakeSpawner` to record env:

```go
type fakeSpawner struct {
	bin, dir string
	args     []string
	env      []string
	proc     Process
	err      error
	stdout   io.Writer
}

func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	f.bin, f.args, f.dir, f.env, f.stdout = bin, args, dir, env, stdout
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}
```

plus a workload test:

```go
func TestRunAppliesConnectorPerSpawn(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	src := &fakeSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f,
		Connector: &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return []string{"PATH=/bin"} }}}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if envMap(f.env)["GH_HOST"] != "jam.example" {
		t.Fatalf("spawn env = %v", f.env)
	}
	if len(h.connectors) != 1 || h.connectors[0] != snippet.Fingerprint(src.c) {
		t.Fatalf("reported = %v", h.connectors)
	}
}

func TestRunWithoutConnectorInherits(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if f.env != nil || len(h.connectors) != 0 {
		t.Fatalf("no connector config must inherit env and report nothing: env=%v reports=%v", f.env, h.connectors)
	}
}
```

(Check the worker-result `ok` JSON shape against an existing `writeResult(t, dir, …)` call in this file and copy it exactly.) Update `spawner_test.go`'s two `Spawn(...)` calls to pass `nil` env before `nil` stdout.

- [ ] **Step 2: Run** `go test ./internal/agentrun/ -v 2>&1 | tail -20` → FAIL (undefined `newConnectorRefresher`, `ConnectorConfig`, Spawn arity).

- [ ] **Step 3: Implement** — `internal/agentrun/connector.go`:

```go
package agentrun

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// ConnectorSource fetches the studio's current connector (GET /connector).
type ConnectorSource interface {
	Fetch(ctx context.Context) (snippet.Connector, error)
}

// GitRouter rewrites the global git config when the connector's git route
// changes: https://github.com/ → baseURL+newRoute (no rewrite when newRoute is
// ""), dropping oldRoute's rewrite; the credential helper is kept iff newRoute != "".
type GitRouter interface {
	Route(baseURL, oldRoute, newRoute string) error
}

// ConnectorConfig turns on the per-turn connector refresh. Nil Config.Connector
// (an older launcher that sets no AT_JAM_BASE_URL) keeps today's behavior: every
// spawn inherits cove-master's env.
type ConnectorConfig struct {
	Source  ConnectorSource
	Git     GitRouter // nil → execGit
	BaseURL string    // https://<jam host>
	Token   string    // the identity token; expanded in memory, never logged
	Initial snippet.Connector
	Environ func() []string // nil → os.Environ
}

// HTTPConnectorSource is the production ConnectorSource: snippet.Fetch through
// the cove's proxy (http.ProxyFromEnvironment via the default transport) with
// the system trust store.
func HTTPConnectorSource(baseURL, token string) ConnectorSource {
	return httpSource{hc: &http.Client{Timeout: 10 * time.Second}, base: baseURL, token: token}
}

type httpSource struct {
	hc          *http.Client
	base, token string
}

func (s httpSource) Fetch(ctx context.Context) (snippet.Connector, error) {
	return snippet.Fetch(s.hc, s.base, s.token)
}

// connectorRefresher applies the current connector before each agent spawn. It
// is single-goroutine (Run's turn loop) — no locking.
type connectorRefresher struct {
	cfg      ConnectorConfig
	log      *slog.Logger
	last     snippet.Connector // last applied (initially the raise-time one)
	route    string            // git route currently configured
	reported string            // last fingerprint reported up
}

func newConnectorRefresher(cfg ConnectorConfig, log *slog.Logger) *connectorRefresher {
	if cfg.Git == nil {
		cfg.Git = execGit{}
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &connectorRefresher{cfg: cfg, log: log, last: cfg.Initial, route: cfg.Initial.GitRoute}
}

// prepare fetches the current connector (falling back to the last applied one
// on any error), applies its git route if changed, and returns the spawn env,
// the applied connector's fingerprint, and whether that fingerprint is new
// since the last report. Logs carry keys counts and fingerprints, never values.
func (r *connectorRefresher) prepare(ctx context.Context) (env []string, fp string, changed bool) {
	cur, err := r.cfg.Source.Fetch(ctx)
	if err != nil {
		r.log.Warn("agentrun: connector refresh failed; using the last applied connector", "err", err.Error())
		cur = r.last
	} else if cur.GitRoute != r.route {
		if gerr := r.cfg.Git.Route(r.cfg.BaseURL, r.route, cur.GitRoute); gerr != nil {
			// Env still applies; the route is retried next turn (r.route unchanged).
			r.log.Warn("agentrun: git route refresh failed", "err", gerr.Error())
		} else {
			r.route = cur.GitRoute
		}
	}
	env = r.spawnEnv(cur)
	r.last = cur
	fp = snippet.Fingerprint(cur)
	if fp != r.reported {
		r.log.Info("agentrun: connector applied", "fingerprint", fp[:12], "env_keys", len(cur.Env))
		r.reported, changed = fp, true
	}
	return env, fp, changed
}

// spawnEnv is cove-master's env minus every key the previous connector owned
// or the current one sets (incl. the identity-token vars Expand re-emits with
// the same value), plus the current connector expanded in memory — so a key a
// destination dropped is gone, and no key appears twice.
func (r *connectorRefresher) spawnEnv(cur snippet.Connector) []string {
	exp := cur.Expand(r.cfg.BaseURL, r.cfg.Token)
	owned := map[string]bool{}
	for k := range r.last.Env {
		owned[k] = true
	}
	for k := range exp {
		owned[k] = true
	}
	var env []string
	for _, kv := range r.cfg.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !owned[k] {
			env = append(env, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(exp)) {
		env = append(env, k+"="+exp[k])
	}
	return env
}

// execGit is the production GitRouter: `git config --global` via argv (no shell),
// mirroring what snippet.Connector.GitConfig renders at raise.
type execGit struct{}

func (execGit) Route(base, oldRoute, newRoute string) error {
	base = strings.TrimRight(base, "/")
	if oldRoute != "" {
		// Exit 5 = key absent: fine, nothing to remove.
		if err := exec.Command("git", "config", "--global", "--unset-all", "url."+base+oldRoute+".insteadOf").Run(); err != nil && !isExit(err, 5) {
			return err
		}
	}
	if newRoute == "" {
		if err := exec.Command("git", "config", "--global", "--unset-all", "credential."+base+".helper").Run(); err != nil && !isExit(err, 5) {
			return err
		}
		return nil
	}
	if err := exec.Command("git", "config", "--global", "url."+base+newRoute+".insteadOf", "https://github.com/").Run(); err != nil {
		return err
	}
	return exec.Command("git", "config", "--global", "credential."+base+".helper", snippet.GitHelper()).Run()
}

func isExit(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}
```

(add the `errors` import).

`spawner.go`: interface + impl gain `env []string` before `stdout`:

```go
	Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error)
```

and in `execSpawner.Spawn` after `cmd.Dir = dir`: `cmd.Env = env // nil inherits cove-master's env`.

`workload.go`: `Config` gains

```go
	// Connector, when set, refreshes the agent's connector before every spawn
	// (GET /connector) and reports the applied fingerprint; nil inherits
	// cove-master's env unchanged (an older launcher).
	Connector *ConnectorConfig
```

`Workload` gains `conn *connectorRefresher`, set in `New` when `cfg.Connector != nil` (`newConnectorRefresher(*cfg.Connector, log)`). In `Run`'s loop, just before `w.spawner.Spawn(...)`:

```go
		var env []string
		if w.conn != nil {
			var fp string
			var changed bool
			env, fp, changed = w.conn.prepare(ctx)
			if changed {
				h.ConnectorApplied(fp)
			}
		}
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir, env, sink)
```

- [ ] **Step 4: Run** `go test ./internal/agentrun/ -v 2>&1 | tail -30` → PASS.

- [ ] **Step 5: Commit** `git add internal/agentrun && git commit -m "feat(agentrun): refresh the connector before every agent spawn"`

---

### Task 5: Raise handoff + cove-master wiring

**Files:**
- Modify: `internal/connect/covemaster.go` (export `AT_JAM_CONNECTOR`, `AT_JAM_BASE_URL`)
- Modify: `cmd/cove-master/main.go` (`buildAgentConfig` builds `ConnectorConfig`; package doc env list)
- Test: `internal/connect/covemaster_test.go`, `cmd/cove-master/main_test.go`
- Docs: `docs/usage/jam/coves.md` (cove-master env block + a "connector refresh" paragraph), `docs/usage/jam/connector.md` ("Delivery")

**Interfaces:**
- Consumes: `agentrun.ConnectorConfig`, `agentrun.HTTPConnectorSource(baseURL, token string) ConnectorSource`
- Produces: env contract `AT_JAM_CONNECTOR` = `json.Marshal(snippet.Connector)`, `AT_JAM_BASE_URL` = `https://<JamHost>`.

- [ ] **Step 1: Failing tests.** In `internal/connect/covemaster_test.go`:

```go
func TestLaunchCoveMasterHandsOffConnector(t *testing.T) {
	fake := &runner.Fake{}
	c := snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "ANTHROPIC_API_KEY": "{token}"}, GitRoute: "/git/"}
	if err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target: sshargs.Target{Host: "h"}, JamHost: "jam.example.com", IdentityToken: "tok-123", Connector: &c,
	}); err != nil {
		t.Fatal(err)
	}
	var env string
	for _, call := range fake.Calls {
		if strings.Contains(strings.Join(call.Args, " "), "cat > "+coveMasterEnvVMPath) {
			env = call.Stdin
		}
	}
	if !strings.Contains(env, "export AT_JAM_BASE_URL='https://jam.example.com'") {
		t.Fatalf("no base url:\n%s", env)
	}
	if !strings.Contains(env, "export AT_JAM_CONNECTOR=") || !strings.Contains(env, `"git_route":"/git/"`) {
		t.Fatalf("no connector handoff:\n%s", env)
	}
	if n := strings.Count(env, "tok-123"); n != 1 {
		t.Fatalf("raw token appears %d times (must only be the single export)", n)
	}
}
```

(Check `shellQuote`'s output form in `covemaster.go` and match the expected quoting in the assertion exactly.)

In `cmd/cove-master/main_test.go` (create if absent; follow existing tests' style there):

```go
func TestBuildAgentConfigConnector(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt")
	os.WriteFile(pf, []byte("p"), 0o600)
	env := map[string]string{
		"AT_COVE_AGENT_PROMPT_FILE": pf,
		"AT_JAM_IDENTITY_TOKEN":     "tok",
		"AT_JAM_BASE_URL":           "https://jam.example",
		"AT_JAM_CONNECTOR":          `{"env":{"GH_HOST":"{host}"},"git_route":"/git/"}`,
	}
	cfg, err := buildAgentConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connector == nil || cfg.Connector.BaseURL != "https://jam.example" || cfg.Connector.Token != "tok" ||
		cfg.Connector.Initial.GitRoute != "/git/" || cfg.Connector.Initial.Env["GH_HOST"] != "{host}" {
		t.Fatalf("connector cfg = %+v", cfg.Connector)
	}
	delete(env, "AT_JAM_BASE_URL")
	if cfg, _ := buildAgentConfig(func(k string) string { return env[k] }); cfg.Connector != nil {
		t.Fatal("no AT_JAM_BASE_URL (older launcher) must disable the refresh")
	}
	env["AT_JAM_BASE_URL"], env["AT_JAM_CONNECTOR"] = "https://jam.example", "{not json"
	if cfg, err := buildAgentConfig(func(k string) string { return env[k] }); err != nil || cfg.Connector == nil || len(cfg.Connector.Initial.Env) != 0 {
		t.Fatalf("malformed AT_JAM_CONNECTOR must start empty, not fail: %+v %v", cfg.Connector, err)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/connect/ ./cmd/cove-master/ -run 'HandsOff|AgentConfigConnector' -v` → FAIL.

- [ ] **Step 3: Implement.** `connect/covemaster.go`, after the `AT_COVE_AGENT_PROMPT_FILE` export:

```go
	// Per-turn connector refresh handoff (cove-master re-fetches GET /connector
	// before every agent spawn): the Jam base, and the raise-time connector as
	// token-free JSON — cove-master's fallback and the env keys it owns.
	fmt.Fprintf(&script, "export AT_JAM_BASE_URL=%s\n", shellQuote("https://"+o.JamHost))
	if o.Connector != nil {
		cj, err := json.Marshal(o.Connector)
		if err != nil {
			return fmt.Errorf("cove-master env: connector: %w", err)
		}
		fmt.Fprintf(&script, "export AT_JAM_CONNECTOR=%s\n", shellQuote(string(cj)))
	}
```

`cmd/cove-master/main.go` `buildAgentConfig`, before the return:

```go
	cfg := agentrun.Config{WorkDir: workdir, Prompt: string(prompt), Resident: resident == "1" || resident == "true"}
	if base := getenv("AT_JAM_BASE_URL"); base != "" {
		token := jamEnv(getenv, "IDENTITY_TOKEN")
		var initial snippet.Connector
		if raw := getenv("AT_JAM_CONNECTOR"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &initial); err != nil {
				initial = snippet.Connector{} // the first successful fetch fills it
			}
		}
		cfg.Connector = &agentrun.ConnectorConfig{
			Source: agentrun.HTTPConnectorSource(base, token), BaseURL: base, Token: token, Initial: initial,
		}
	}
	return cfg, nil
```

Add to the package doc env list:

```
//	AT_JAM_BASE_URL          https://<jam host>; when set, the agent's connector is re-fetched
//	                         (GET /connector) before every spawn
//	AT_JAM_CONNECTOR         the raise-time connector (JSON, no token): fallback + owned env keys
```

- [ ] **Step 4: Run** `go test ./internal/connect/ ./cmd/cove-master/ -v 2>&1 | tail -20` → PASS.

- [ ] **Step 5: Docs.** `docs/usage/jam/coves.md`: add the two variables to the env block (same column style), and after the "cove-master runs the agent as a **headless one-shot**" paragraph add: "**Connector refresh.** Before every agent spawn — the first turn, a resume, a wake — cove-master re-fetches its connector (`GET /connector`, [connector.md](connector.md)) and starts that turn with the current env and git routing, so a destination or grant edit reaches a running studio at its next turn (never mid-turn). If the fetch fails it keeps the last connector it applied and logs a warning. It reports the applied connector's fingerprint up the Attach stream; see the `connector` column below." `docs/usage/jam/connector.md` "Delivery" first bullet becomes: "**Jam-raised studios** get their connector at raise, and cove-master re-fetches it before every agent turn, so edits reach a running studio at its next turn ([coves.md](coves.md#cove-master-the-in-cove-client))."

- [ ] **Step 6: Commit** `git add internal/connect cmd/cove-master docs/usage/jam && git commit -m "feat(cove-master): hand off the connector and refresh it per turn"`

---

### Task 6: Jam records the applied connector and shows staleness

**Files:**
- Modify: `internal/jam/instance.go` (`Connector` field), `internal/jam/supervisor.go` (`RecordConnector`)
- Modify: `internal/jam/attach/server.go` (handle `StatusUp_Connector`)
- Modify: `internal/jam/admin.go` (`CoveSummary.Connector`, `CoveSummaries`)
- Modify: `cmd/at-jam/main.go` (`studio list` column), `internal/jam/adminui/templates/coves.html` (column)
- Test: `internal/jam/attach/server_test.go`, `internal/jam/admin_test.go` (or a new `internal/jam/connector_status_test.go`)
- Docs: `docs/usage/jam/coves.md` (`studio list` columns), `docs/usage/jam/ui-pages.md` if it lists studio columns

**Interfaces:**
- Consumes: `snippet.Fingerprint`, `ConnectorFor(store, actor)`
- Produces: `func (s *Supervisor) RecordConnector(actorID, fp string) error`; `CoveSummary.Connector string` (`json:"connector"`): `ok|stale|unknown|error`.

- [ ] **Step 1: Failing tests.** `server_test.go`:

```go
func TestAttachRecordsConnector(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Connector{Connector: &attachpb.ConnectorApplied{Fingerprint: "fp-1"}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { i, ok := store.GetInstance("w1"); return ok && i.Connector == "fp-1" }) {
		i, _ := store.GetInstance("w1")
		t.Fatalf("connector not recorded: %+v", i)
	}
}
```

`internal/jam/connector_status_test.go`:

```go
package jam

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func TestCoveSummariesConnectorStatus(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	gh := Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"GH_HOST": "{host}"}}
	if err := store.AddDestination(gh); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"gh"}})
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "w1", Project: "acme", Role: "w"})
	actor := actorByID(t, store, "w1")
	want, err := ConnectorFor(store, actor)
	if err != nil {
		t.Fatal(err)
	}
	put := func(fp string) {
		if err := store.PutInstance(Instance{ActorID: "w1", Project: "acme", Role: "w", Phase: PhaseLive, Connector: fp}); err != nil {
			t.Fatal(err)
		}
	}
	status := func() string {
		for _, c := range CoveSummaries(store) {
			if c.ID == "w1" {
				return c.Connector
			}
		}
		t.Fatal("w1 missing")
		return ""
	}

	put("")
	if s := status(); s != "unknown" {
		t.Fatalf("never reported = %q, want unknown", s)
	}
	put(snippet.Fingerprint(want))
	if s := status(); s != "ok" {
		t.Fatalf("current = %q, want ok", s)
	}

	// Edit the destination's env: the reported fingerprint is now stale.
	if err := store.RemoveDestination("gh"); err != nil {
		t.Fatal(err)
	}
	gh.Env = map[string]string{"GH_HOST": "{host}", "X": "y"}
	if err := store.AddDestination(gh); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "stale" {
		t.Fatalf("after edit = %q, want stale", s)
	}

	// A second destination setting GH_HOST differently → conflict → error.
	if err := store.AddDestination(Destination{Name: "gh2", Route: "/x/", Upstream: "https://x.example", Env: map[string]string{"GH_HOST": "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", Role{Name: "w", Scope: Scope{Destinations: []string{"gh", "gh2"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "error" {
		t.Fatalf("conflict = %q, want error", s)
	}
}

func actorByID(t *testing.T, store Store, id string) Actor {
	t.Helper()
	for _, a := range store.ListActors() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no actor %s", id)
	return Actor{}
}
```

(If `PutRole` returns no error in this store, drop the `if err :=` wrapper.)

- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/attach/ -run 'Connector' -v` → FAIL.

- [ ] **Step 3: Implement.**

`instance.go` — add after `EgressFailures`:

```go
	Connector          string    `json:"connector,omitempty"`           // snippet.Fingerprint of the connector the cove last reported applying to an agent spawn; "" = never reported (an older image, or no turn yet)
```

`supervisor.go` — next to `Heartbeat`:

```go
// RecordConnector records the connector fingerprint a cove reports having
// applied to its latest agent spawn. Staleness is derived on read
// (CoveSummaries), never stored, so it cannot itself drift.
func (s *Supervisor) RecordConnector(actorID, fp string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone || inst.Connector == fp {
		return nil
	}
	inst.Connector = fp
	return s.store.PutInstance(inst)
}
```

`attach/server.go` recv loop:

```go
		case *attachpb.StatusUp_Connector:
			_ = s.sup.RecordConnector(actorID, m.Connector.GetFingerprint())
```

`admin.go` — `CoveSummary` gains `Connector string \`json:"connector"\`` (doc: "ok | stale | unknown | error — the cove's reported connector vs its role's current one"). `CoveSummaries`:

```go
func CoveSummaries(store Store) []CoveSummary {
	actors := map[string]Actor{}
	for _, a := range store.ListActors() {
		actors[a.ID] = a
	}
	var out []CoveSummary
	for _, i := range store.ListInstances() {
		out = append(out, CoveSummary{
			ID: i.ActorID, Project: i.Project, Role: i.Role, Unit: i.Unit,
			Phase: string(i.Phase), Activity: string(i.Activity),
			LeaseHolder: i.Lease.Holder, RaisedAt: i.RaisedAt, LastSeen: i.LastSeen,
			Connector: connectorStatus(store, actors, i),
		})
	}
	return out
}

// connectorStatus compares the connector a cove reported applying with the one
// its identity's grants yield now: ok, stale, unknown (never reported — an older
// image), or error (the actor is gone or its destinations conflict).
func connectorStatus(store Store, actors map[string]Actor, i Instance) string {
	if i.Connector == "" {
		return "unknown"
	}
	a, ok := actors[i.ActorID]
	if !ok {
		return "error"
	}
	want, err := ConnectorFor(store, a)
	if err != nil {
		return "error"
	}
	if snippet.Fingerprint(want) == i.Connector {
		return "ok"
	}
	return "stale"
}
```

`cmd/at-jam/main.go` `studio list`:

```go
			fmt.Fprintf(stdout, "%s\trole=%s\tunit=%s\tphase=%s\tactivity=%s\tholder=%s\tconnector=%s\n",
				cv.ID, cv.Role, cv.Unit, cv.Phase, cv.Activity, cv.LeaseHolder, cv.Connector)
```

(Also make sure `adminclient`'s cove-list decoding uses `jam.CoveSummary` so the field flows; if it has its own struct, add `Connector`.)

`templates/coves.html`: add `<th>Connector</th>` after `<th>Activity</th>`, the cell `<td>{{if eq .Connector "ok"}}ok{{else if .Connector}}<span class="none">{{.Connector}}</span>{{else}}<span class="none">—</span>{{end}}</td>` after the Activity cell, and bump both `colspan` values by one (11/10).

- [ ] **Step 4: Run** `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... ./cmd/at-jam/ 2>&1 | tail -20` → PASS (fix any golden/HTML tests that pin the coves table columns or the list line format).

- [ ] **Step 5: Docs.** `docs/usage/jam/coves.md`: the `studio list` comment line becomes `# id  role  unit  phase  activity  lease-holder  connector` and add a sentence under the verbs: "`connector` is `ok` when the studio's last agent turn ran with its role's current connector, `stale` when a destination or grant changed since (it refreshes at the next turn), `unknown` when it never reported (an image built before the per-turn refresh — re-raise it), `error` when the role's destinations conflict." If `docs/usage/jam/ui-pages.md` enumerates the Studios table columns, add Connector there.

- [ ] **Step 6: Commit** `git add -A internal/jam cmd/at-jam docs/usage/jam && git commit -m "feat(jam): record applied connectors and flag stale studios"`

---

### Task 7: Backlog + full verification

**Files:**
- Modify: `docs/TODO.md`

- [ ] **Step 1:** Append to `docs/TODO.md` (match its existing bullet style):
  - "Studio kits: resolve a tag-form `base.image` to its `@sha256` digest before `BuildDigest`, so a moved tag rebuilds (spec 2026-10-02-kit-build-drift)."
  - "Studio kits: garbage-collect superseded `cove-kit:*` images on the substrate (every assembly-fingerprint change leaves the old tags)."
  - "Managed studios: flag a running studio whose *image* predates the current assembly fingerprint (record the tag on the Instance), mirroring the connector `stale` column."

- [ ] **Step 2: Full suite + lint**

Run: `GOPROXY=https://proxy.golang.org,direct just test 2>&1 | tail -30` → all `ok`.
Run: `GOPROXY=https://proxy.golang.org,direct just lint 2>&1 | tail -30` → clean (or only pre-existing findings — compare against `main`).

- [ ] **Step 3: Docs audit** — run the docs-audit skill's checker on the repo; fix any dangling link/orphan it reports in docs touched by this branch.

- [ ] **Step 4: Commit** `git commit -am "docs: backlog follow-ups for kit build drift"`
