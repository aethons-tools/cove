# Kit git sources — slice 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator can `at-jam kit import|track|untrack|sync` a studio kit from a git repo (GitHub, GitLab, plain smart-HTTP git). Jam fetches it, every version records where it came from, and the kit page shows and drives it.

**Architecture:** a new `internal/jam/gitsource` package parses `<repo>[//<dir>]@<ref>` sources and fetches one ref shallowly with go-git into a temp dir, under size and time limits and with credentials matched by URL prefix. A containment-checked loader turns `<dir>/kit.yml` into a resolved `StudioKit`. `jam` gains `ImportKit`/`TrackKit`/`UntrackKit`/`SyncKit` over a `KitFetcher` seam. They push through the existing `ensureStudioKit` with per-version provenance stored in the kit's jsonb doc, so no migration is needed. The admin API, `adminclient`, CLI and admin UI surface them.

**Tech Stack:** Go 1.26, `github.com/go-git/go-git/v5` (new direct dependency), pgx (existing jsonb `kits.doc`), html/template + htmx. Tests use a real smart-HTTP server: `git http-backend` via `net/http/cgi` behind `httptest`, skipped when `git` is absent.

**Spec:** [`docs/superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../specs/2026-10-20-kit-git-sources-and-inheritance-design.md), §1, §4 (source panel), §5, §6 (fetcher) and slice 1. Slices 2–3 (inheritance) are out of scope here.

## Global Constraints

- **Source syntax:** `<repo-url>[//<dir>]@<ref>`.
  - `<repo-url>` is `https://…`. `http://` is accepted only for loopback hosts (`127.0.0.1`, `::1`, `localhost`).
  - URLs with userinfo are refused.
  - `<dir>` is a clean relative path (no `..`, not absolute) and defaults to `.`.
  - `<ref>` is a branch, a tag or a 40-hex commit SHA.
- **The kit file:** Jam reads exactly `<dir>/kit.yml` (studio-kit format). Its `context-dir` and note `file:` paths must resolve, after `EvalSymlinks`, inside the checkout root. Otherwise the import is refused.
- **Existing limits apply unchanged:** context ≤ 1 MiB encoded, ≤ 64 MiB decoded, ≤ 2000 entries; prompt ≤ 800 B; notes ≤ 20.
- **Fetch:** shallow (depth 1), single ref, ≤ **50 MiB** transferred, ≤ **60 s** per fetch, in a temp dir that is always removed. Fetches of the same repo URL are serialized.
- **Credentials:** serve config `kits: { git: [ { url: <prefix>, cred: <name> } ] }`.
  - `cred` is a demanded, plain static credential.
  - The longest matching URL prefix wins; with no match the fetch is anonymous.
  - HTTP Basic username: `x-access-token` for `github.com` hosts, `oauth2` for any host whose name contains `gitlab`, otherwise taken from the credential value when it is `user:token`, else `git`.
- **Name:** defaults to `kit.yml`'s `name:` field, then to `path.Base(<dir>)` (repo name when `<dir>` is `.`). It must pass `studio.CheckName`.
- **Provenance:** each version records `{repo, dir, ref, commit}` or `local`. The tracked source is stored on the kit.
- **Sync** pushes only when the commit moved *and* the canonical kit differs. Otherwise it answers `unchanged`.
- **Secrets:** tokens never reach logs, errors, provenance, argv or disk. A git error that may contain a URL is passed through `redactURL`.
- **Tests:** hermetic. The real-git tests skip when `git`/`git-http-backend` is missing.
- **Toolchain:** `GOPROXY=https://proxy.golang.org,direct`.
- **Docs:** docs change in the same task as the behaviour.

## Review Focus

1. **A malicious repo** with a symlink in `<dir>` pointing at `/etc` or the Jam state dir, `context-dir: ../../..`, or a note `file:` symlinked out: the import is refused, and nothing outside the checkout is read. *(Task 2: `TestLoadRefusesEscapes`.)*
2. **A huge repo or a slow server:** the fetch fails at 50 MiB or 60 s with a clear error, and the temp dir is gone. *(Task 3: `TestFetchLimits`.)*
3. **A wrong or expired token:** "auth failed for <url-prefix> (check kits.git cred <name>)" with no token in the message, and the current version is untouched. *(Task 3: `TestFetchAuth`; Task 4: `TestSyncFailureLeavesKit`.)*
4. **Sync on an unchanged commit, or a new commit with byte-identical kit output:** `unchanged`, and no new version. *(Task 4: `TestSyncUnchanged`.)*
5. **A kit pushed from a file after being tracked:** `sync` still works, and `versions` shows `local` for that version. *(Task 4: `TestMixedProvenance`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/identity.go` (modify), `memstate.go`, `memstore.go`, `pgstore.go`, `storetest/kits_source.go` | `Kit.Source`, `Kit.Meta`, `PushKitMeta`, `SetKitSource` |
| `internal/studio/jail.go` | containment-checked `context-dir` / note resolution |
| `internal/jam/gitsource/{source.go,fetch.go,creds.go,load.go}` | source parsing, go-git fetch, credentials, `kit.yml` loading |
| `internal/jam/gitsource/gittest/server.go` | test helper: a bare repo served over `git http-backend` |
| `internal/jam/kitsource.go` | `ImportKit`, `TrackKit`, `UntrackKit`, `SyncKit`, `KitFetcher` |
| `internal/jam/admin.go`, `adminclient/adminclient.go` | routes and client methods |
| `cmd/at-jam/config.go`, `cmd/at-jam/main.go` | `kits:` config, cred resolution, wiring, CLI verbs |
| `internal/jam/adminui/kits.go`, `kit_detail.go`, `templates/kit.html` | source panel, buttons |
| `docs/usage/jam/kits-git.md` (new), `kits.md`, `serve.md`, `ui-pages.md`, INDEX | docs |

---

### Task 1: Provenance and tracked source in the store

**Files:**
- Modify: `internal/jam/identity.go` (`Kit`), `internal/jam/memstate.go` (`copyKit`, `applyPushKit` → `applyPushKitMeta`, new `applySetKitSource`), `internal/jam/memstore.go`, `internal/jam/pgstore.go`, `internal/jam/store.go` (interface)
- Create: `internal/jam/storetest/kits_source.go` (wired into the conformance runner like `sharegrants.go`)

**Interfaces:**
- Produces:
```go
// KitSource is where a kit version came from, or a kit's tracked source.
type KitSource struct {
	Repo   string `json:"repo"`
	Dir    string `json:"dir,omitempty"`    // "" = repo root
	Ref    string `json:"ref"`
	Commit string `json:"commit,omitempty"` // set on versions; on a tracked source, the last synced commit
}
// KitVersionMeta is per-version metadata. Source nil = pushed from a local file.
type KitVersionMeta struct {
	PushedAt time.Time  `json:"pushed_at"`
	Source   *KitSource `json:"source,omitempty"`
}
// Kit gains:
	Meta   map[int]KitVersionMeta `json:"meta,omitempty"`
	Source *KitSource             `json:"source,omitempty"` // tracked source; nil = not tracked
```
  - Store methods: `PushKitMeta(name, config string, meta KitVersionMeta) (int, error)`. `PushKit(name, config)` becomes `PushKitMeta(name, config, KitVersionMeta{PushedAt: now})`.
  - `SetKitSource(name string, src *KitSource) error` (nil clears it; an unknown kit is `ErrNotFound`).
  - `copyKit` deep-copies `Meta` and `Source`.

- [ ] **Step 1: Failing conformance tests**

```go
// storetest/kits_source.go
func kitSourceCases(t *testing.T, mk func(t *testing.T) jam.Store) {
	t.Run("push records meta", func(t *testing.T) {
		st := mk(t)
		at := time.Unix(1_000_000, 0).UTC()
		src := &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main", Commit: strings.Repeat("a", 40)}
		v, err := st.PushKitMeta("web", `{"kind":"studio"}`, jam.KitVersionMeta{PushedAt: at, Source: src})
		if err != nil || v != 1 {
			t.Fatalf("push: %d %v", v, err)
		}
		k, _ := st.GetKit("web")
		if got := k.Meta[1]; !got.PushedAt.Equal(at) || !reflect.DeepEqual(got.Source, src) {
			t.Fatalf("meta %+v", got)
		}
		if _, err := st.PushKit("web", `{"kind":"studio","prompt":"x"}`); err != nil {
			t.Fatal(err)
		}
		k, _ = st.GetKit("web")
		if k.Meta[2].Source != nil || k.Meta[2].PushedAt.IsZero() {
			t.Fatalf("local push meta %+v", k.Meta[2])
		}
	})
	t.Run("tracked source set/clear and copies are deep", func(t *testing.T) {
		st := mk(t)
		_, _ = st.PushKit("web", `{"kind":"studio"}`)
		src := &jam.KitSource{Repo: "https://gitlab.example.com/g/k", Ref: "v1"}
		if err := st.SetKitSource("web", src); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("web")
		k.Source.Ref = "mutated"
		k2, _ := st.GetKit("web")
		if k2.Source.Ref != "v1" {
			t.Fatal("GetKit returned an aliased Source")
		}
		if err := st.SetKitSource("web", nil); err != nil {
			t.Fatal(err)
		}
		if k3, _ := st.GetKit("web"); k3.Source != nil {
			t.Fatal("source not cleared")
		}
		if !errors.Is(st.SetKitSource("nope", src), jam.ErrNotFound) {
			t.Fatal("unknown kit")
		}
	})
}
```
Add a Postgres reload test (in `pgstore_integration_test.go`, like `TestPostgresShareGrantsReload`): push with meta, set the source, reopen, and assert `Meta` and `Source` round-trip.

- [ ] **Step 2: Run** `go test ./internal/jam/ -run Conformance -count=1`. Expect FAIL (undefined).
- [ ] **Step 3: Implement.**
  - Mem: `applyPushKitMeta` sets `Versions[v]`, `Meta[v]` and `Current`.
  - Postgres: same doc update path as `PushKit` (`pgstore.go:637`); `SetKitSource` marshals the doc with the new `Source` via the same `INSERT … ON CONFLICT` update. Cache only after the write succeeds.
  - `ensureStudioKit` keeps calling `store.PushKit` (unchanged behaviour); Task 4 adds the meta path.
  - Existing kits with no `Meta` show as `local` with an unknown time.
- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1` and `go vet -tags integration ./internal/jam/`. Expect PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): kit version provenance and tracked source in the store"`

---

### Task 2: Containment-checked kit loading

**Files:**
- Create: `internal/studio/jail.go`, `internal/studio/jail_test.go`, `internal/jam/gitsource/load.go`, `internal/jam/gitsource/load_test.go`

**Interfaces:**
- Produces:
  - `func (sk *StudioKit) ResolveInRoot(root, dir string) error`. Like `ResolveContextDir(dir)` plus `ResolveNoteFiles(dir)`, but every resolved path (the context dir and each note file, after `filepath.EvalSymlinks`) must lie within `root` (after `EvalSymlinks` of `root`). It also refuses absolute paths. Errors name the offending kit field and the relative path, never absolute host paths outside `root`.
  - `func Load(root, dir string) (studio.StudioKit, string /*name hint*/, error)` in gitsource:
    1. Reads `root/dir/kit.yml` (refusing a symlinked `kit.yml` that leaves `root`).
    2. Runs `ParseStudioKit`.
    3. Runs `ResolveInRoot`.
    4. Runs `CheckPrompt` and `CheckNotes`.
    5. Returns the name hint (`name:` field, else `path.Base(dir)`, else `""`).
  - `PackContextDir` already refuses symlinks *inside* the context tree; `ResolveInRoot` adds the root check.

- [ ] **Step 1: Failing tests**

```go
func TestLoadRefusesEscapes(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o600)
	os.MkdirAll(filepath.Join(outside, "ctx"), 0o755)
	os.WriteFile(filepath.Join(outside, "ctx", "Dockerfile"), []byte("FROM ${COVE_BASE_IMAGE}\n"), 0o644)
	for name, setup := range map[string]func(root string){
		"context-dir up":        func(r string) { writeKit(t, r, "web", "base:\n  context-dir: ../../"+filepath.Base(outside)+"/ctx\n") },
		"context-dir absolute":  func(r string) { writeKit(t, r, "web", "base:\n  context-dir: "+filepath.Join(outside, "ctx")+"\n") },
		"context-dir symlink":   func(r string) { os.Symlink(filepath.Join(outside, "ctx"), filepath.Join(r, "web", "img")); writeKit(t, r, "web", "base:\n  context-dir: img\n") },
		"note file symlink":     func(r string) { os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "n.md")); writeKit(t, r, "web", "notes:\n  - name: n.md\n    file: n.md\n") },
		"kit.yml symlink":       func(r string) { os.MkdirAll(filepath.Join(r, "web"), 0o755); os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(r, "web", "kit.yml")) },
		"dir symlink":           func(r string) { os.Symlink(outside, filepath.Join(r, "web")) },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(root)
			_, _, err := Load(root, "web")
			if err == nil {
				t.Fatal("escape accepted")
			}
			if strings.Contains(err.Error(), "top secret") || strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks outside content/path: %v", err)
			}
		})
	}
}

func TestLoadResolvesInsideCheckout(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "web", "name: web-dev\nbase:\n  context-dir: image\nnotes:\n  - name: guide.md\n    file: docs/guide.md\n")
	writeFile(t, root, "web/image/Dockerfile", "FROM ${COVE_BASE_IMAGE}\nRUN true\n")
	writeFile(t, root, "web/docs/guide.md", "hello")
	sk, hint, err := Load(root, "web")
	if err != nil || hint != "web-dev" || sk.Base.Context == "" || sk.Notes[0].Body != "hello" {
		t.Fatalf("%v %q %+v", err, hint, sk)
	}
}
```
(`writeKit(t, root, dir, body)` writes `root/dir/kit.yml` with `kind: studio\n` + body; `writeFile` makes parents.)

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (`filepath.EvalSymlinks` + `filepath.Rel` + reject `..`-prefixed). **Step 4: Run** `go test ./internal/studio/ ./internal/jam/gitsource/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): load a kit.yml from a checkout without leaving it"`

---

### Task 3: Source parsing, credentials and the go-git fetcher

**Files:**
- Create: `internal/jam/gitsource/source.go`, `creds.go`, `fetch.go`, `fetch_test.go`, `source_test.go`, `internal/jam/gitsource/gittest/server.go`
- Modify: `go.mod`/`go.sum` (`github.com/go-git/go-git/v5`)

**Interfaces:**
- Produces:
```go
type Source struct{ Repo, Dir, Ref string } // Dir "" = root
func Parse(s string) (Source, error)        // "<repo>[//<dir>]@<ref>"
func (s Source) String() string

type Cred struct{ Prefix, Name, Value string } // Value never logged
type Creds []Cred
func (c Creds) For(repo string) (Cred, bool)   // longest prefix match
func basicAuth(repo string, c Cred) *http.BasicAuth // username per Global Constraints

type Fetcher struct {
	Creds    Creds
	MaxBytes int64         // 50 MiB
	Timeout  time.Duration // 60 s
	TempDir  string        // "" = os.TempDir()
}
// Fetch checks out src.Ref shallowly into a fresh temp dir and calls fn(root, commit);
// the dir is removed when fn returns. Errors are redacted (no userinfo, no token).
func (f *Fetcher) Fetch(ctx context.Context, src Source, fn func(root, commit string) error) error
// Resolve returns the commit src.Ref points to without a checkout (ls-remote).
func (f *Fetcher) Resolve(ctx context.Context, src Source) (string, error)
```
- **How to fetch:**
  - Use `git.PlainCloneContext` with `Depth: 1`, `SingleBranch: true`, `NoCheckout: false`, `Tags: git.NoTags`.
  - `ReferenceName` is `refs/heads/<ref>`. If the remote has no such branch, use `refs/tags/<ref>`.
  - For a 40-hex ref, init plus fetch `+<sha>:refs/heads/jam`, depth 1, then check out.
  - The byte limit is a counting wrapper on the transport. Use `client.InstallProtocol` with an `http.Client` whose `Transport` counts response bytes and fails past `MaxBytes`. Scope it per Fetcher via a custom transport, not the global install, if go-git allows (`githttp.NewClient(httpClient)` plus `transport.Register` per scheme — check the v5 API; if only global registration exists, register once with a context-scoped counter).
- **Auth errors** map to `auth failed for <prefix-or-host> (check kits.git cred <name>)` when a cred was used, else `auth required for <host> (add a kits.git entry)`. A missing ref gives `ref <ref> not found in <repo>`.

- [ ] **Step 1: Test server helper** — `gittest.Serve(t, files map[string]string) (url string, commit string, update func(files map[string]string) string)`:
  1. Makes a bare repo, commits `files` via a temp work tree (using the git CLI, `t.Skip` if `git` is missing), and tags `v1`.
  2. Serves it with `cgi.Handler{Path: <git-http-backend>, Env: ["GIT_PROJECT_ROOT=…", "GIT_HTTP_EXPORT_ALL=1"]}` behind `httptest.NewServer`.
  3. Optionally wraps it in Basic auth (`gittest.WithAuth(user, pass)`).
- [ ] **Step 2: Failing tests**

```go
func TestParseSource(t *testing.T) {
	for in, want := range map[string]Source{
		"https://github.com/acme/kits@main":            {Repo: "https://github.com/acme/kits", Ref: "main"},
		"https://github.com/acme/kits//web/dev@v1.2":   {Repo: "https://github.com/acme/kits", Dir: "web/dev", Ref: "v1.2"},
		"http://127.0.0.1:9/r.git@" + strings.Repeat("a", 40): {Repo: "http://127.0.0.1:9/r.git", Ref: strings.Repeat("a", 40)},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "https://github.com/acme/kits", "http://github.com/acme/kits@main", "https://u:p@github.com/a/b@main",
		"https://github.com/a/b//../x@main", "https://github.com/a/b///abs@main", "ssh://git@github.com/a/b@main", "file:///tmp/r@main"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestCredsLongestPrefixAndUsername(t *testing.T) {
	c := Creds{{Prefix: "https://github.com/", Name: "gh", Value: "t1"}, {Prefix: "https://github.com/acme/", Name: "acme", Value: "t2"},
		{Prefix: "https://gitlab.example.com/", Name: "gl", Value: "t3"}, {Prefix: "https://git.example.org/", Name: "plain", Value: "bob:t4"}}
	if got, _ := c.For("https://github.com/acme/kits"); got.Name != "acme" {
		t.Fatalf("got %s", got.Name)
	}
	for repo, user := range map[string]string{"https://github.com/acme/kits": "x-access-token", "https://gitlab.example.com/g/k": "oauth2", "https://git.example.org/k": "bob"} {
		cr, _ := c.For(repo)
		if a := basicAuth(repo, cr); a.Username != user {
			t.Errorf("%s: user %q", repo, a.Username)
		}
	}
}

func TestFetchBranchTagCommitDir(t *testing.T) {
	url, c1, update := gittest.Serve(t, map[string]string{"web/kit.yml": "kind: studio\nprompt: one\n"})
	c2 := update(map[string]string{"web/kit.yml": "kind: studio\nprompt: two\n"})
	f := &Fetcher{MaxBytes: 50 << 20, Timeout: 60 * time.Second}
	for ref, want := range map[string]struct{ commit, prompt string }{"main": {c2, "two"}, "v1": {c1, "one"}, c1: {c1, "one"}} {
		var gotCommit, gotBody string
		err := f.Fetch(context.Background(), Source{Repo: url, Dir: "web", Ref: ref}, func(root, commit string) error {
			b, _ := os.ReadFile(filepath.Join(root, "web", "kit.yml"))
			gotCommit, gotBody = commit, string(b)
			return nil
		})
		if err != nil || gotCommit != want.commit || !strings.Contains(gotBody, want.prompt) {
			t.Errorf("ref %s: %v %s %q", ref, err, gotCommit, gotBody)
		}
	}
	if c, err := f.Resolve(context.Background(), Source{Repo: url, Ref: "main"}); err != nil || c != c2 {
		t.Fatalf("Resolve: %s %v", c, err)
	}
	if err := f.Fetch(context.Background(), Source{Repo: url, Ref: "nope"}, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "ref nope not found") {
		t.Fatalf("missing ref: %v", err)
	}
}

func TestFetchAuth(t *testing.T) {
	url, _, _ := gittest.Serve(t, map[string]string{"kit.yml": "kind: studio\n"}, gittest.WithAuth("x-access-token", "good-token"))
	ok := &Fetcher{Creds: Creds{{Prefix: url, Name: "gh-kits", Value: "good-token"}}, MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	if err := ok.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	bad := &Fetcher{Creds: Creds{{Prefix: url, Name: "gh-kits", Value: "WRONG-SECRET"}}, MaxBytes: 50 << 20, Timeout: 30 * time.Second}
	err := bad.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "check kits.git cred gh-kits") || strings.Contains(err.Error(), "WRONG-SECRET") {
		t.Fatalf("bad auth: %v", err)
	}
}

func TestFetchLimits(t *testing.T) {
	big := map[string]string{"kit.yml": "kind: studio\n", "blob.bin": randomString(3 << 20)}
	url, _, _ := gittest.Serve(t, big)
	tmp := t.TempDir()
	f := &Fetcher{MaxBytes: 1 << 20, Timeout: 30 * time.Second, TempDir: tmp}
	err := f.Fetch(context.Background(), Source{Repo: url, Ref: "main"}, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("size limit: %v", err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp dir left behind: %v", ents)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	f2 := &Fetcher{MaxBytes: 1 << 20, Timeout: 200 * time.Millisecond, TempDir: tmp}
	if err := f2.Fetch(context.Background(), Source{Repo: slow.URL + "/r.git", Ref: "main"}, func(string, string) error { return nil }); err == nil {
		t.Fatal("timeout not enforced")
	}
}
```
(`randomString` = incompressible bytes, base64.)

- [ ] **Step 3: Run** `GOPROXY=https://proxy.golang.org,direct go get github.com/go-git/go-git/v5 && go test ./internal/jam/gitsource/ -count=1` → FAIL.
- [ ] **Step 4: Implement** per interfaces. Serialize fetches per repo URL with a `map[string]*sync.Mutex`. `redactURL` strips userinfo.
- [ ] **Step 5: Run** → PASS (`-count=3` for timing stability). `just lint`.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): git kit sources — parse, credentials and a shallow go-git fetcher"`

---

### Task 4: Import, track, untrack, sync (server)

**Files:**
- Create: `internal/jam/kitsource.go`, `internal/jam/kitsource_test.go`
- Modify: `internal/jam/studiokit.go` (an `ensureStudioKitMeta` variant holding `kitMu` and calling `PushKitMeta`)

**Interfaces:**
- Consumes: `gitsource.Source`, `gitsource.Load` (T2), `Store.PushKitMeta/SetKitSource` (T1).
- Produces:
```go
type KitFetcher interface {
	Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error
	Resolve(ctx context.Context, src gitsource.Source) (string, error)
}
type KitSourceResult struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Commit    string `json:"commit"`
}
func ImportKit(ctx context.Context, store Store, f KitFetcher, name string, src gitsource.Source, now time.Time) (KitSourceResult, error)
func TrackKit(ctx context.Context, store Store, f KitFetcher, name string, src gitsource.Source, now time.Time) (KitSourceResult, error)
func UntrackKit(store Store, name string) error
func SyncKit(ctx context.Context, store Store, f KitFetcher, name string, now time.Time) (KitSourceResult, error) // ErrNotTracked
var ErrNotTracked = errors.New("kit has no tracked source (use kit track)")
```
  - **Import:**
    1. Fetch.
    2. `Load(root, src.Dir)`.
    3. Name defaulting: if `name == ""` use the hint; validate with `CheckName`.
    4. `ensureStudioKitMeta(store, name, sk, KitVersionMeta{PushedAt: now, Source: &KitSource{src.Repo, src.Dir, src.Ref, commit}})`.
    5. Return the result with the commit.
  - **Track:** Import, then `SetKitSource(name, &KitSource{…, Commit: commit})`.
  - **Sync:**
    1. Read `Kit.Source` (`ErrNotTracked` if nil).
    2. `Resolve`. If the commit equals `Source.Commit` and the kit's current version came from that commit, return `unchanged` without fetching.
    3. Otherwise import, which reports `unchanged` if the canonical JSON is identical.
    4. Update `Source.Commit`.
  - **Untrack:** `SetKitSource(name, nil)` (unknown → `ErrNotFound`).
  - **Audit:** `log.Info("kit imported"|"kit synced", "kit", name, "repo", src.Repo, "dir", src.Dir, "ref", src.Ref, "commit", commit, "version", v, "unchanged", u)`, with `log` passed in (add a `*slog.Logger` param, or use the package logger pattern used by `PushStudioKit` callers in admin.go).
  - **Failure:** any failure returns the error with the store untouched.

- [ ] **Step 1: Failing tests** — a `fakeFetcher` that serves an in-memory `map[commit]map[path]content` as a temp dir, plus refs → commit. Tests:

```go
func TestImportAndTrackRecordProvenance(t *testing.T) {
	st := NewMemStore()
	f := newFakeFetcher(map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"web/kit.yml": "kind: studio\nname: web-dev\nprompt: hi\n"}})
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	res, err := ImportKit(context.Background(), st, f, "", src, time.Unix(10, 0))
	if err != nil || res.Name != "web-dev" || res.Version != 1 || res.Commit != "c1" {
		t.Fatalf("%+v %v", res, err)
	}
	k, _ := st.GetKit("web-dev")
	if m := k.Meta[1]; m.Source == nil || m.Source.Commit != "c1" || m.Source.Dir != "web" || k.Source != nil {
		t.Fatalf("import must record version provenance and not track: %+v %+v", m, k.Source)
	}
	if _, err := TrackKit(context.Background(), st, f, "web-dev", src, time.Unix(11, 0)); err != nil {
		t.Fatal(err)
	}
	k, _ = st.GetKit("web-dev")
	if k.Source == nil || k.Source.Commit != "c1" || len(k.Versions) != 1 {
		t.Fatalf("track: %+v versions=%d", k.Source, len(k.Versions))
	}
}

func TestSyncUnchanged(t *testing.T) {
	st, f := trackedFixture(t) // web-dev tracked at c1
	res, err := SyncKit(context.Background(), st, f, "web-dev", time.Unix(20, 0))
	if err != nil || !res.Unchanged || f.fetches != 1 /* only the track's */ {
		t.Fatalf("same commit: %+v %v fetches=%d", res, err, f.fetches)
	}
	f.commit("main", "c2", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: hi\n"}) // new commit, identical kit
	res, err = SyncKit(context.Background(), st, f, "web-dev", time.Unix(21, 0))
	if err != nil || !res.Unchanged || res.Commit != "c2" {
		t.Fatalf("identical content: %+v %v", res, err)
	}
	if k, _ := st.GetKit("web-dev"); len(k.Versions) != 1 || k.Source.Commit != "c2" {
		t.Fatalf("versions=%d commit=%s", len(k.Versions), k.Source.Commit)
	}
	f.commit("main", "c3", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: changed\n"})
	if res, _ := SyncKit(context.Background(), st, f, "web-dev", time.Unix(22, 0)); res.Unchanged || res.Version != 2 {
		t.Fatalf("changed: %+v", res)
	}
}

func TestSyncFailureLeavesKit(t *testing.T) {
	st, f := trackedFixture(t)
	f.failNext = errors.New("auth failed for https://github.com/acme/ (check kits.git cred gh-kits)")
	before, _ := st.GetKit("web-dev")
	if _, err := SyncKit(context.Background(), st, f, "web-dev", time.Unix(30, 0)); err == nil {
		t.Fatal("expected error")
	}
	after, _ := st.GetKit("web-dev")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed sync changed the kit")
	}
}

func TestMixedProvenance(t *testing.T) {
	st, f := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "web-dev", `{"kind":"studio","prompt":"local edit"}`); err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("web-dev")
	if k.Meta[2].Source != nil {
		t.Fatal("local push must be local")
	}
	f.commit("main", "c9", map[string]string{"web/kit.yml": "kind: studio\nname: web-dev\nprompt: from git\n"})
	if res, err := SyncKit(context.Background(), st, f, "web-dev", time.Unix(40, 0)); err != nil || res.Version != 3 {
		t.Fatalf("%+v %v", res, err)
	}
}
```
Plus: `SyncKit` on an untracked kit → `ErrNotTracked`; `UntrackKit` keeps versions; an invalid `kit.yml` → error and no version; a name that fails `CheckName` → error.

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./internal/jam/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): import, track, untrack and sync kits from git"`

---

### Task 5: Admin API, client, serve config and wiring

**Files:**
- Modify: `internal/jam/admin.go` (routes + `WithKitFetcher(KitFetcher)` option), `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/config.go` (+ test), `cmd/at-jam/main.go`
- Test: `internal/jam/admin_kitsource_test.go`, `cmd/at-jam/config_test.go`

**Interfaces:**
- Routes (operator-auth, logged with `OperatorID(r)`):
  - `POST /admin/kits/import` `{name?, source}` → `KitSourceResult`.
  - `PUT /admin/kits/{name}/source` `{source}` → track → `KitSourceResult`.
  - `DELETE /admin/kits/{name}/source` → 204.
  - `POST /admin/kits/{name}/sync` → `KitSourceResult`.
  - `GET /admin/kits/{name}/history` → `[]KitVersionInfo{Version, PushedAt, Source *KitSource, Current bool}`. The existing `/versions` stays `[]int`.
  - Errors:
    - `gitsource.Parse` → 400;
    - `ErrNotTracked` → 409;
    - fetch/auth/limit → 502, with the redacted message;
    - validation → 400;
    - no fetcher configured → 409 "kit git sources are not configured (add kits: to the serve config)", or allow anonymous public fetches. **Decision:** always construct a Fetcher, even with no creds, so public repos work without config.
- adminclient: `ImportKit(name, source string)`, `TrackKit(name, source string)`, `UntrackKit(name string)`, `SyncKit(name string)` (each returning `jam.KitSourceResult` where applicable), `KitHistory(name) ([]jam.KitVersionInfo, error)`. Use a longer timeout (90 s) for import/track/sync, since `do()` defaults to 10 s.
- Serve config:
  - `type kitsConfig struct { Git []kitGitCred \`yaml:"git"\` }`, with `type kitGitCred struct { URL string \`yaml:"url"\`; Cred string \`yaml:"cred"\` }`.
  - `validateKits`:
    - each `url` parses as `https://` (or loopback `http://`) and ends with `/`, with no userinfo;
    - `cred` is demanded, plain static (not gcp-exchange, not the pool);
    - no duplicate URL.
  - main resolves each cred with `secret.Resolve` and builds a `gitsource.Fetcher{Creds, 50<<20, 60s}`, passed with `jam.WithKitFetcher`.

- [ ] **Step 1: Failing tests:**
  - **Admin:** an httptest admin handler with a fake fetcher, covering import (201/200 + body), track/untrack/sync, the history JSON, and each error mapping. Assert the response never contains a fake token string the fetcher error might include. `adminclient` methods round-trip against the handler.
  - **Config:** valid; non-https url; userinfo; url without trailing `/`; undemanded cred; gcp cred; duplicate.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... ./cmd/at-jam/ -count=1 && just lint` → PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/serve.md`: a `kits` row (git credentials by URL prefix, username rules, anonymous fallback), linking `kits-git.md`. `docs/usage/jam/credentials.md`'s secret-reference table gains `kits.git[].cred`.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): kit git-source admin API, client and kits.git serve config"`

---

### Task 6: CLI verbs

**Files:**
- Modify: `cmd/at-jam/main.go` (`cmdKit`: add `import`, `track`, `untrack`, `sync`; `versions` prints history; `show` prints the source; update the command's Brief), Test: `cmd/at-jam/kit_test.go` (or the existing kit CLI test file)

- **Usage:**
  - `at-jam kit import --git <source> [--name N]` prints `imported NAME vN from <repo>[//dir]@<ref> (<commit12>)` or `NAME unchanged (current vN, <commit12>)`.
  - `at-jam kit track NAME --git <source>` is the same, plus `tracking <source>`.
  - `at-jam kit untrack NAME` prints `NAME no longer tracks a source`.
  - `at-jam kit sync NAME` prints `synced NAME vN (<commit12>)` or `NAME unchanged (<commit12>)`. Untracked: exit 1 with the server's message.
  - `at-jam kit versions NAME` prints `VERSION  CURRENT  PUSHED  SOURCE  COMMIT` rows, `SOURCE` = `local` or `<repo>[//dir]@<ref>` and `COMMIT` = 12 chars. It falls back to `v%d` lines if `/history` 404s (older Jam).
  - `at-jam kit show NAME` adds a leading `# source: …` comment line and `# tracking: <source> (last synced <commit12>)` when tracked.
- **Errors** print to stderr; the exit code is 2 for usage and 1 for runtime.

- [ ] **Step 1: Failing tests** against an httptest admin server (the existing CLI test harness for `kit push`): import output, track/untrack/sync output, versions table columns, show header lines, untracked sync exit 1, missing `--git` exit 2.
- [ ] **Step 2–4:** implement; `go test ./cmd/at-jam/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(at-jam): kit import, track, untrack and sync"`

---

### Task 7: Kit page source panel

**Files:**
- Modify: `internal/jam/adminui/kits.go` (routes `POST /ui/kits/{name}/sync`, `POST /ui/kits/{name}/source` (track, form field `source`), `DELETE /ui/kits/{name}/source`, `POST /ui/kits/import` (form `source`, `name`)), `internal/jam/adminui/kit_detail.go` (view model: tracked source, per-version source/commit/pushed-at), `templates/kit.html`, `templates/kits.html` (an "Import from git" form), `internal/jam/adminui/adminui.go` (option carrying the `KitFetcher`; buttons hidden when nil)
- Test: `internal/jam/adminui/kits_source_test.go`

- **The page shows:**
  - a **Source** card: tracked source with a linked repo (`https://` only, rendered as `href` via html/template), dir, ref and last-synced commit (12 chars), plus **Sync** and **Untrack** buttons; or a **Track…** form when not tracked;
  - the versions table gains Source and Commit columns;
  - all writes behind `guardWrite`, htmx swaps of `kit-body`, errors via `renderError`.
- **Commit links:** for `github.com`, link `…/commit/<sha>`; for hosts containing `gitlab`, `…/-/commit/<sha>`; otherwise show the commit as plain text.

- [ ] **Step 1: Failing tests:**
  - render with a tracked kit (source shown, links escaped, buttons present);
  - sync button POST → fake fetcher called, new version row;
  - cross-origin POST → 403;
  - an import form with a bad source → error fragment;
  - no fetcher → no buttons.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/adminui/ -count=1` → PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/ui-pages.md` kit pages: the Source card and Import form.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): kit page source panel — import, track, sync, untrack"`

---

### Task 8: End-to-end test and docs

**Files:**
- Create: `cmd/at-jam/kit_git_integration_test.go` (`//go:build integration`), `docs/usage/jam/kits-git.md`
- Modify: `docs/usage/jam/kits.md` (verbs table rows + a short "From git" paragraph linking `kits-git.md`; "The model" gains version provenance), `docs/usage/jam/INDEX.md`, `docs/OVERVIEW.md` (one clause), `justfile` (`integration-jam` already covers `./cmd/at-jam/...` — confirm)

- [ ] **Step 1: Integration test.**
  1. Set up a real `gittest.Serve` repo with Basic auth and a `web/kit.yml` with `context-dir: image` and a note file.
  2. Run serve's real admin handler with a real `gitsource.Fetcher` configured from a `kits.git` entry.
  3. Run `at-jam kit track web --git <url>//web@main` through `run(...)` → `imported web v1`.
  4. Run `kit show` and check the source header and the packed context present.
  5. Commit a change and run `kit sync web` → v2.
  6. Run `kit sync web` again → unchanged.
  7. Run `kit versions web` → two rows with commits.
  8. Run with a wrong token → the auth error naming the cred, with the token absent from all output.
- [ ] **Step 2: Docs** — `kits-git.md`, with progressive-disclosure frontmatter: summary, read_when, owns, prereqs, tier, updated. It covers:
  - the source syntax and examples (GitHub, GitLab, plain git, `//dir`, tag, commit);
  - the `kit.yml` layout (`context-dir`/note paths stay inside the checkout);
  - import vs track/sync semantics (manual sync; `unchanged` rules);
  - provenance in `versions` and `show`;
  - credentials (`kits.git`, username rules, anonymous public repos);
  - limits (50 MiB/60 s, existing kit limits);
  - troubleshooting (auth failed, ref not found, kit.yml not found, escape refused, exceeded);
  - future work (attention-only poll; inheritance slices).
- [ ] **Step 3:** `just test && just lint && go test -tags integration ./cmd/at-jam/ -run KitGit -count=1`; docs audit (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`) — no new errors.
- [ ] **Step 4: Commit** `git commit -m "docs(jam): kits from git guide; kit git end-to-end test"`

---

### Task 9: Manual acceptance (operator)

- [ ] Put a kit in a private GitHub repo and add `kits: { git: [{ url: https://github.com/<org>/, cred: <pat> }] }`; restart serve.
- [ ] `at-jam kit track <name> --git https://github.com/<org>/<repo>//<dir>@main` → v1 with the commit; the kit page shows the Source card.
- [ ] Push a change, click **Sync** → a new version; a role on that kit shows `image=stale`; `standing upgrade` picks it up.
- [ ] Repeat with a GitLab repo (and a plain git server if available).
