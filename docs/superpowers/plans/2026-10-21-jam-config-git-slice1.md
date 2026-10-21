# A Jam's configuration in git — slice 1 (files and round trip) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Jam's configuration (destinations, model specs, users, connections, kits, projects with members, rooms and escalation, roles, context layers, and the portable serve config) round-trips through a checkout of one-file-per-object YAML. `at-jam config write <dir> [--all]` writes the tree. `at-jam config diff --dir` and `config apply --dir [--yes] [--force]` plan the tree and apply it in one transaction, using managed marks. Replicating a Jam from a checkout works at the end of the slice.

**Architecture:**
- **One canonical form per object:** every object has a *file form* (the YAML file structs in `config_files.go`). The exporter renders a Jam's objects into it, with ids replaced by names, and the loader parses a checkout into it.
- **Comparison and digests:** the planner compares the two sides by their canonical bytes, which are the rendered files. A kit's canonical bytes are its stored config JSON. Marks store the sha256 of the canonical bytes.
- **A pure planner over a scratch state:** the planner is a pure function of (tree, `ConfigSnapshot`, instances, marks, env). It rebuilds a private `memState` from the snapshot (`stateFromSnapshot`) and runs every create, update and delete through the store's own `prepare*`/`check*` rules, in the spec's order. References therefore resolve against the *planned* state, and each delete guard sees the state the plan leaves.
- **The write set:** the planner records every record it would write into an exported `ConfigWrite`. `Store.ApplyConfig` writes that set atomically: MemStore under one lock, Postgres in one transaction followed by a cache apply. This is the `PutKitDocs` pattern extended to every kind.
- **Kits:** inline kits are loaded with the slice-3 machinery. `loadKitSet` runs per `kits/<name>/`, and the sets are merged parents first. `resolveKitSet` is generalized to `resolveKitEntries`, and `buildKitDocs` produces the docs.
- **Apply:** apply takes `configMu`, then `kitMu`, across plan and write.
- **Upload:** the CLI packs `--dir` as a tar.gz. The admin routes unpack it into a temp dir under fixed caps, and the same loader reads it. Slice 2's git checkout will reuse that loader unchanged.

**Tech Stack:** Go 1.26; `gopkg.in/yaml.v3`; the existing `jam` store (MemStore + Postgres via pgx), `studio`, `gitsource` and `sessionctx` packages; `archive/tar` + `compress/gzip`.

**Spec:** [`docs/superpowers/specs/2026-10-21-jam-config-in-git-design.md`](../specs/2026-10-21-jam-config-in-git-design.md) §§1–5, §7, §8 and slice 1. Slice 2 (`track`/`sync`/`untrack`, `--git`, `.source.yml`) and slice 3 (UI, badges, `release`, `config.drift`) are out of scope.

## Spec deviations and interpretations

1. **Model-spec harness version key.** A model spec's own `version` (the harness release) collides with the file's `version: 1`, so in `model-specs/<name>.yml` it is written as `harness-version:`. Every other model-spec field keeps the kit model-spec block's YAML names.
2. **Connection kind key.** A connection's service kind collides with the file's `kind: connection`, so it is `service: linear|discord`. The credential name is `cred:`, as in `ConnectionBody`.
3. **`context.md` format.** The session-context authoring format is YAML (`core`, `leaves`). In a `.md` file it is written as optional YAML front matter holding `leaves:` (name, read-when, body), followed by the core as the markdown body.
   - The core is the body with trailing newlines trimmed.
   - It is written with exactly one trailing newline.
   - A role's layer is inline YAML (`context: {core, leaves}`), as the spec says.
4. **`launcher` host key** is the serve-config path `runtime.launcher`, which is where that block lives. The host-key table is one Go slice of dotted paths.
5. **`--dir` provenance.** Kit versions written by `apply --dir` record `KitVersionMeta.Source = {repo: "local", dir: "kits/<name>"}`, with no ref or commit. Marks written by a `--dir` apply record `commit: "dir"`. Slice 2 fills in the repo, ref and commit.
6. **The seeded `default` model spec.** Every serve seeds `default` (`EnsureDefaultModelSpec`), so a fresh replica already holds an unmanaged `default`. A repo `model-specs/default.yml` whose content differs is therefore planned as `update` (adopt it and write the repo's version) instead of being refused. This is the only "seeded" object.
7. **A managed mark whose object is gone from Jam:**
   - If the object is in the repo, it is planned as `create`.
   - If it is not, the mark is dropped silently. There is no op, because nothing exists to delete.
8. **Unmanaged objects stay untouched, as decision 2 requires.** Deleting a managed user is refused while the user is still a member of any project, managed or not. This refusal is added to the spec's user guards because `RemoveUser` would otherwise silently drop memberships in unmanaged projects.
9. **Upload limits.** The phrase "Kit context limits scaled to 16 MiB" is read as follows:
   - the gzip body is at most 16 MiB;
   - the unpacked file bytes total at most 16 MiB;
   - there are at most 2000 entries;
   - only regular files and directories are allowed;
   - no absolute path and no `..`.
10. **Accepted residual.** `configMu` and `kitMu` cover config applies and kit writes, as the spec says. A non-kit admin edit (a role put, say) landing between an apply's snapshot and its write can be overwritten by that write. Apply plans and writes within a few milliseconds under the locks, and a later diff shows the edit as drift.
11. **Marks are not part of `at-jam export`.** The backup format is unchanged. A restored Jam starts with no marks, and its next apply adopts identical objects.

## Global Constraints

- **Layout (exact paths):**
  - `jam.yml`, `context.md`
  - `destinations/<n>.yml`, `model-specs/<n>.yml`, `users/<n>.yml`, `connections/<n>.yml`
  - `kits/<n>/kit.yml`
  - `projects/<n>/project.yml`, `projects/<n>/context.md`, `projects/<n>/roles/<r>.yml`
  - Other top-level files and directories (README, `.git`) are ignored.
  - Inside the known directories, anything else is a refusal. Two cases have fixed wording: `kits/<n>.source.yml` gives `kit source references are not supported yet`, and a `.yaml` extension gives `use .yml`.
- **Files:**
  - Every non-kit file carries `kind:` (`destination`, `model-spec`, `user`, `connection`, `project`, `role`) and `version: 1`.
  - Unknown fields are refused: `users/bob.yml: unknown field "oidc"`.
  - A `name:`, if present, must equal the file's name.
  - Kit files are today's studio-kit format.
- **Mark kinds** are the file kinds plus `kit` and `jam-context` (name `jam`). A role's mark name is `<project>/<role>`.
- **Never in the repo:** token hashes, actors, grants, OIDC identities, accounts, instances, intercom state, channel membership, pool state, credential values, the legacy roster. The file types have no field for any of them.
- **Plan table (§3):** each object is one of `create`, `update`, `update`+drift (refused without `--force`), `adopt`, `delete`, `unchanged`, `unmanaged`, or a refusal. The refusal text is fixed:
  - `<kind> <name> exists and is not managed; config write --all it into the repo, or rename`
  - Drift: `<kind> <name> drifted (edited in Jam since config @<commit[:7]>); re-run with --force or config write it first`
- **Order:**
  - Creates and updates run users, connections → destinations, model specs → kits (parents first) → projects (members, rooms, escalation) → roles → Jam context.
  - Deletes run in the reverse order.
- **Delete guards** are refusals of the form `cannot delete <kind> <name>: <reason>`:
  - a role with a live instance;
  - a kit a role binds (`role app/ic binds it`) or another kit builds on;
  - a model spec a role resolves to;
  - a user who owns a live personal session, is named in a remaining escalation tier, or is a member of any project;
  - a project with live instances, members it will not remove, unmanaged roles, standing sessions or grants (`checkRemoveProject`);
  - a connection with accounts, a binding, or a chat service (`prepareRemoveConnection`).
- **Pins:** an `@vN` in a repo role's `kit` or a repo kit's `base.kit` is refused: `<path>: <ref> pins a version; versions are per-Jam — name the kit (or use a path parent)`. Path parents are rewritten to `name@vN`, as today.
- **Atomicity:** all loading, planning and validation happen before the write, and the write is one `Store.ApplyConfig` call. Any failure writes nothing.
- **Locks:** apply holds `configMu`, then `kitMu`, then calls the store, which takes its own `mu`.
- **Serve config:**
  - `serve --config` is repeatable. Maps merge by key; scalars and lists replace.
  - Host keys: `listen`, `admin-listen`, `ui-hosts`, `ui-origins`, `dev-identity`, `tls`, `admin-tls`, `store-postgres`, `state-dir`, `credentials-file`, `runtime.launcher`, `pool.store`.
  - With two or more `--config` files, the first may not set a host key, at serve start, diff and apply: `jam.yml: listen is a host key; put it in jam.local.yml`.
  - The diff reports `same`, `differs (restart to apply)` with dotted key paths, `absent` (no `jam.yml`), or `unknown` (no running serve config).
- **Write:**
  - Output is deterministic: yaml.v3 with 2-space indent, sorted maps, fixed struct field order, sorted members and rooms.
  - Write removes only the files of managed objects Jam no longer has (and a managed project's `context.md` when its context is empty).
  - It refuses a `<dir>` outside a git working tree unless `--force`.
- **Errors** are file-located and never echo a token, a credential value or unvalidated repo text. A yaml type error is reported as `line N: a value has the wrong type`, never with the value.
- **Audit:**
  - A `config applied` log entry carries source, commit, counts, operator and forced.
  - Each written object gets its usual `admin …` entry plus `via=config`.
- **Tests and docs:** hermetic by default. The Postgres tests use the `integration` tag and `JAM_TEST_POSTGRES_DSN`. Set `GOPROXY=https://proxy.golang.org,direct`. Docs change in the same task as the behavior. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` must not report more errors than before (708 at the start of this slice).

## Review Focus

1. **Re-applying a tree right after applying it.** Every kind must plan `unchanged` with no drift, including an unnormalized egress list (`GitHub.com`), a duration written `90m`, a room label equal to its key, a membership delivery, and a context core without a trailing newline. Normalization drift would mark every object as edited in Jam forever. *(Task 6: `TestPlanReapplyIsUnchanged`.)*
2. **Replication onto a freshly started Jam** that already seeded `default` and materialized nothing else. The plan adopts or updates `default` instead of refusing it. *(Task 6: `TestPlanSeededDefaultModelSpec`; Task 8's round trip seeds `default` on the target.)*
3. **Removing a user from the repo while an unmanaged project still lists them** is refused, and the unmanaged membership is untouched. *(Task 7: `TestPlanUserDeleteRefusedByUnmanagedMembership`.)*
4. **A hostile upload** (symlink, `../x`, absolute path, a device node, a gzip bomb, 2001 entries) is refused with 400 or 413 before anything is loaded. *(Task 8: `TestUnpackRefuses`, `TestConfigRoutesRejectBadUpload`.)*
5. **A kit whose `kit.yml` `name:` differs from its directory, or whose path parent leaves `kits/`,** gives a file-located refusal, and no other kit in the tree is written. *(Task 4: `TestLoadKitsRefusesMisplacedParent`; Task 6: `TestPlanRefusalWritesNothing`; Task 8: `TestApplyConfigTreeRefusesAndWritesNothing`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/config_files.go` (create) | `ConfigKey`, kinds, file structs, strict parse, render, `context.md` codec, role/user/connection file↔record conversion |
| `internal/jam/config_marks.go` (create) | `ConfigMark`, `ConfigWrite`, `checkConfigWrite`, `applyConfigWrite`, `memState.ListConfigMarks` |
| `internal/jam/migrations/0016_config_marks.sql` (create) | the `config_marks` table |
| `internal/jam/pgstore_config.go` (create) | `PostgresStore.ApplyConfig`, `writeConfigTx`, mark loading |
| `internal/jam/storetest/config.go` (create) | conformance for `ApplyConfig` and marks |
| `internal/jam/config_export.go` (create) | `stateFromSnapshot`, `exportObjects`, `renderKitFile`, `ExportConfigTree` |
| `internal/jam/config_tree.go` (create) | `ConfigTree`, `LoadConfigTree`, kit loading/order |
| `internal/jam/kitset.go` (modify) | `resolveKitEntries` (generalized `resolveKitSet`) |
| `internal/jam/config_serve.go` (create) | host-key table, merge, portable part, serve-config diff |
| `cmd/at-jam/serveconfig.go` (create), `cmd/at-jam/main.go` (modify) | repeatable `--config`, `loadServeConfigs` |
| `internal/jam/config_plan.go` (create) | `PlanConfig`: classification, create/update write set |
| `internal/jam/config_plan_delete.go` (create) | deletes and delete guards |
| `internal/jam/roleedit.go` (modify) | `checkStandingDecl` extracted from `AddStanding` |
| `internal/jam/configtar/configtar.go` (create) | `Pack`/`Unpack` with caps |
| `internal/jam/config_apply.go` (create) | `configMu`, `ApplyConfigTree`, audit |
| `internal/jam/config_admin.go` (create) | `WithConfig`: `/admin/config/diff`, `/apply`, `/export-tree` |
| `internal/jam/adminclient/adminclient.go` (modify) | `ConfigDiff`, `ConfigApply`, `ConfigExportTree` |
| `cmd/at-jam/configgit.go` (create) | `at-jam config write|diff|apply` |
| `docs/usage/jam/config-git.md` (create), `docs/usage/jam/INDEX.md`, `serve.md`, `backup.md` (modify) | docs |

---

### Task 1: File kinds and their YAML codecs

**Files:**
- Create: `internal/jam/config_files.go`, `internal/jam/config_files_test.go`

**Interfaces:**
- Consumes: `Destination`, `ModelSpec` (and `HarnessType`, `ModelPrincipal`, `ModelChoice`, `ModelPolicy`, `ClaudeSpec`), `Role`, `Scope`, `EgressPolicy`, `RoleAllocation`, `StandingSession`, `TurnEndPolicy`, `CollaboratePolicy`, `sessionctx.Layer`, `sessionctx.Leaf`, `sessionctx.Resource`, `NormalizeEgress`.
- Produces:
```go
const ConfigFileVersion = 1
const (
	ConfigKindDestination = "destination"
	ConfigKindModelSpec   = "model-spec"
	ConfigKindUser        = "user"
	ConfigKindConnection  = "connection"
	ConfigKindKit         = "kit"
	ConfigKindProject     = "project"
	ConfigKindRole        = "role"
	ConfigKindJamContext  = "jam-context"
)
type ConfigKey struct {
	Kind string `json:"kind"`
	Name string `json:"name"` // a role is "<project>/<role>"; the Jam context is "jam"
}
func (k ConfigKey) String() string // "destination anthropic", "role app/ic"
func (k ConfigKey) Path() string   // its repo file (a project: project.yml)

type DestinationFile struct{ Kind string; Version int; Destination }       // yaml inline
type ModelSpecFile struct{ Kind, Name string; Version int; Type HarnessType; HarnessVersion, VersionConstraint string; Principal ModelPrincipal; Model ModelChoice; Policy ModelPolicy; Note string; Claude *ClaudeSpec }
type UserFile struct{ Kind string; Version int; Name string; Logins []string }
type ConnectionFile struct{ Kind string; Version int; Name, Service, Cred string }
type ProjectFile struct{ Kind string; Version int; Name, ChatService string; Escalation []EscalationTierFile; EscalationByCategory map[string][]EscalationTierFile; Resources []sessionctx.Resource; Members []MemberFile; Rooms []RoomFile; Context sessionctx.Layer /* yaml:"-" */ }
type RoleFile struct{ Kind string; Version int; Name string; Scope RoleScopeFile; Kit, ModelSpec string; Allocation AllocationFile; TurnEnd TurnEndFile; Collaborate CollaborateFile; Context *ContextFile }
type JamContextFile struct{ Layer sessionctx.Layer }

func ParseDestinationFile(path, name string, data []byte) (DestinationFile, error)
func ParseModelSpecFile(path, name string, data []byte) (ModelSpecFile, error)
func ParseUserFile(path, name string, data []byte) (UserFile, error)
func ParseConnectionFile(path, name string, data []byte) (ConnectionFile, error)
func ParseProjectFile(path, name string, data []byte) (ProjectFile, error) // normalized
func ParseRoleFile(path, name string, data []byte) (RoleFile, error)       // normalized
func ParseContextMarkdown(path string, data []byte) (sessionctx.Layer, error)
func RenderContextMarkdown(l sessionctx.Layer) []byte

// configFile is any non-kit object in file form.
type configFile interface {
	configKey() ConfigKey
	configFiles() map[string][]byte // repo path → rendered content
}
// configObject is one object in canonical form; Canon is canonFiles(Files),
// except a kit's, which is its stored config JSON.
type configObject struct {
	Key   ConfigKey
	Files map[string][]byte
	Canon []byte
}
func newConfigObject(f configFile) configObject
func (o configObject) Digest() string // hex sha256 of Canon

func (f ModelSpecFile) spec() ModelSpec
func modelSpecFileOf(ms ModelSpec) ModelSpecFile
func (f RoleFile) role() (Role, error)          // durations parsed; negatives refused
func roleFileOf(name string, r Role, userName func(ident.ID) (string, bool)) RoleFile
func (f *ProjectFile) normalize() error         // sort members/rooms, drop label==key and mode both, canonical durations
func formatConfigDuration(d time.Duration) string // "" for 0; "4h", "1h30m", "1m30s", "30s"
```

- [ ] **Step 1: Write the failing tests**

```go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestParseUserFileStrict(t *testing.T) {
	f, err := ParseUserFile("users/bob.yml", "bob", []byte("kind: user\nversion: 1\nlogins: [bob@acme]\n"))
	if err != nil || f.Name != "bob" || len(f.Logins) != 1 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	for in, want := range map[string]string{
		"kind: user\nversion: 1\noidc: [x]\n":       `users/bob.yml: unknown field "oidc"`,
		"kind: role\nversion: 1\n":                  "users/bob.yml: kind must be user",
		"kind: user\nversion: 2\n":                  "users/bob.yml: version must be 1",
		"kind: user\nversion: 1\nname: alice\n":     `users/bob.yml: name "alice" does not match the file name "bob"`,
		"kind: user\nversion: 1\nlogins: {a: b}\n":  "users/bob.yml: line 3: a value has the wrong type",
		"":                                          "users/bob.yml: empty file",
	} {
		if _, err := ParseUserFile("users/bob.yml", "bob", []byte(in)); err == nil || err.Error() != want {
			t.Errorf("%q: err=%v, want %q", in, err, want)
		}
	}
}

func TestTypeErrorNeverEchoesValue(t *testing.T) {
	_, err := ParseConnectionFile("connections/acme.yml", "acme", []byte("kind: connection\nversion: 1\nservice: [sk-secret-value]\n"))
	if err == nil || strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelSpecFileHarnessVersion(t *testing.T) {
	f, err := ParseModelSpecFile("model-specs/default.yml", "default", []byte(
		"kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ms := f.spec(); ms.Name != "default" || ms.Version != "2.1.3" || ms.Principal.Credential != "anthropic" {
		t.Fatalf("spec = %+v", ms)
	}
	if got := string(newConfigObject(f).Files["model-specs/default.yml"]); !strings.Contains(got, "harness-version: 2.1.3\n") {
		t.Fatalf("render:\n%s", got)
	}
}

func TestRoleFileRoundTrip(t *testing.T) {
	in := "kind: role\nversion: 1\nscope:\n  destinations: [anthropic]\n  ttl: 90m\n  egress: [GitHub.com]\n" +
		"kit: web-dev\nallocation:\n  max-personal: 2\n  idle-after: 4h\n  standing: [{name: lead, prompt: run it}]\n" +
		"turn-end: {idle-timeout: 30m, on-idle: wake}\ncollaborate: {ssh: members}\n" +
		"context:\n  core: be brief\n  leaves: [{name: a.md, read-when: always, body: x}]\n"
	f, err := ParseRoleFile("projects/app/roles/ic.yml", "ic", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.role()
	if err != nil {
		t.Fatal(err)
	}
	if r.Scope.TTL != 90*time.Minute || r.Scope.Egress == nil || r.Scope.Egress.Domains[0] != "github.com" ||
		r.Allocation.IdleAfter != 4*time.Hour || r.Allocation.Standing[0].Name != "lead" || r.Context.Core != "be brief" {
		t.Fatalf("role = %+v", r)
	}
	back := roleFileOf("ic", r, func(ident.ID) (string, bool) { return "", false })
	if a, b := newConfigObject(f).Canon, newConfigObject(back).Canon; string(a) != string(b) {
		t.Fatalf("canon differs:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(string(newConfigObject(back).Canon), "ttl: 1h30m\n") {
		t.Fatalf("duration not canonical:\n%s", newConfigObject(back).Canon)
	}
	if _, err := ParseRoleFile("projects/app/roles/ic.yml", "ic", []byte("kind: role\nversion: 1\nallocation: {max-personal: -1}\n")); err == nil ||
		err.Error() != "projects/app/roles/ic.yml: allocation.max-personal must be >= 0" {
		t.Fatalf("negative: %v", err)
	}
}

func TestRoleFileAddressingByName(t *testing.T) {
	r := Role{Name: "ic", Scope: Scope{Addressing: []string{"user:usr_01hx0000000000000000000000", "channel:*"}}}
	f := roleFileOf("ic", r, func(id ident.ID) (string, bool) { return "bob", id == "usr_01hx0000000000000000000000" })
	if f.Scope.Addressing[0] != "user:bob" || f.Scope.Addressing[1] != "channel:*" {
		t.Fatalf("addressing = %v", f.Scope.Addressing)
	}
}

func TestContextMarkdown(t *testing.T) {
	l, err := ParseContextMarkdown("context.md", []byte("---\r\nleaves:\r\n  - {name: a.md, read-when: always, body: hi}\r\n---\r\nBe kind.\r\n\r\n"))
	if err != nil || l.Core != "Be kind." || len(l.Leaves) != 1 || l.Leaves[0].ReadWhen != "always" {
		t.Fatalf("l=%+v err=%v", l, err)
	}
	if got := string(RenderContextMarkdown(sessionctx.Layer{Core: "Be kind."})); got != "Be kind.\n" {
		t.Fatalf("plain render %q", got)
	}
	again, _ := ParseContextMarkdown("context.md", RenderContextMarkdown(l))
	if again.Core != l.Core || len(again.Leaves) != 1 {
		t.Fatalf("round trip %+v", again)
	}
	for in, want := range map[string]string{
		"---\nleaves: []\nBe kind.\n":     "context.md: front matter is not closed with ---",
		"---\ncore: x\n---\nBe kind.\n":   `context.md: unknown field "core"`,
	} {
		if _, err := ParseContextMarkdown("context.md", []byte(in)); err == nil || err.Error() != want {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestProjectFileNormalize(t *testing.T) {
	f, err := ParseProjectFile("projects/app/project.yml", "app", []byte("kind: project\nversion: 1\n"+
		"escalation: [{targets: [user:bob], timeout: 60m}]\n"+
		"members: [{user: zed}, {user: bob, delivery: [{service: discord, address: \"123\"}]}]\n"+
		"rooms: [{key: ops, label: ops, bindings: [{connection: acme, ref: ACME-1, mode: both}]}, {key: alpha, bindings: [{connection: acme, ref: ACME-2}]}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Members[0].User != "bob" || f.Rooms[0].Key != "alpha" || f.Rooms[1].Label != "" || f.Rooms[1].Bindings[0].Mode != "" || f.Escalation[0].Timeout != "1h" {
		t.Fatalf("not normalized: %+v", f)
	}
}

func TestFormatConfigDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "", 30 * time.Second: "30s", 90 * time.Second: "1m30s",
		5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Minute: "1h30m", time.Hour + 5*time.Second: "1h0m5s"} {
		if got := formatConfigDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestConfigKeyPaths(t *testing.T) {
	for k, want := range map[ConfigKey]string{
		{ConfigKindDestination, "gh"}: "destinations/gh.yml", {ConfigKindModelSpec, "m"}: "model-specs/m.yml",
		{ConfigKindUser, "bob"}: "users/bob.yml", {ConfigKindConnection, "acme"}: "connections/acme.yml",
		{ConfigKindKit, "web"}: "kits/web/kit.yml", {ConfigKindProject, "app"}: "projects/app/project.yml",
		{ConfigKindRole, "app/ic"}: "projects/app/roles/ic.yml", {ConfigKindJamContext, "jam"}: "context.md",
	} {
		if k.Path() != want {
			t.Errorf("%v: %s", k, k.Path())
		}
	}
}
```
(Add `"github.com/aethons-tools/cove/internal/ident"` to the imports.)

- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'TestParseUserFile|TestTypeError|TestModelSpecFile|TestRoleFile|TestContextMarkdown|TestProjectFile|TestFormatConfigDuration|TestConfigKey' -count=1`. Expected: FAIL, because nothing is defined yet.

- [ ] **Step 3: Implement `config_files.go`.** The core helpers:

```go
var (
	unknownFieldRE = regexp.MustCompile(`^line \d+: field (\S+) not found in type`)
	typeLineRE     = regexp.MustCompile(`^line (\d+):`)
)

// decodeStrict decodes one YAML document into out with unknown fields
// refused. Its errors are path-prefixed and never carry a value from the file
// (only an unknown key's name, clipped).
func decodeStrict(path string, data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(out)
	switch {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%s: empty file", path)
	case err == nil:
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		if m := unknownFieldRE.FindStringSubmatch(te.Errors[0]); m != nil {
			return fmt.Errorf("%s: unknown field %q", path, clipName(m[1]))
		}
		if m := typeLineRE.FindStringSubmatch(te.Errors[0]); m != nil {
			return fmt.Errorf("%s: line %s: a value has the wrong type", path, m[1])
		}
		return fmt.Errorf("%s: a value has the wrong type", path)
	}
	return fmt.Errorf("%s: %s", path, strings.TrimPrefix(err.Error(), "yaml: ")) // syntax errors carry no content
}

func clipName(s string) string {
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

// checkHeader enforces kind:, version: and name: (defaulting an empty name to the file's).
func checkHeader(path, wantKind, kind string, version int, wantName string, name *string) error {
	if kind != wantKind {
		return fmt.Errorf("%s: kind must be %s", path, wantKind)
	}
	if version != ConfigFileVersion {
		return fmt.Errorf("%s: version must be %d", path, ConfigFileVersion)
	}
	if *name == "" {
		*name = wantName
	} else if *name != wantName {
		return fmt.Errorf("%s: name %q does not match the file name %q", path, clipName(*name), wantName)
	}
	return nil
}

// renderYAML is the one renderer config write, digests and the diff use:
// yaml.v3, 2-space indent (maps sorted by yaml.v3). The file types hold only
// strings, numbers, bools, slices, maps and structs, so encoding cannot fail.
func renderYAML(v any) []byte {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		panic("config: render: " + err.Error()) // unreachable for the file types (tested per type)
	}
	enc.Close()
	return b.Bytes()
}

// canonFiles is an object's canonical bytes: its files in path order, each
// introduced by its path.
func canonFiles(files map[string][]byte) []byte {
	var b bytes.Buffer
	for _, p := range slices.Sorted(maps.Keys(files)) {
		b.WriteString("# " + p + "\n")
		b.Write(files[p])
	}
	return b.Bytes()
}

func formatConfigDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	s := d.String() // "1h30m0s", "5m0s", "30s", "1h0m5s"
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2] // "1h30m", "5m", "1h0m"
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2] // "1h"
	}
	return s
}

// parseConfigDuration reads a file duration ("" = 0); field names the key for errors.
func parseConfigDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s is not a duration (e.g. 90s, 30m, 4h)", field)
	}
	return d, nil
}
```
  - **File types:**
    - Each struct lists `Kind`, `Version` and `Name` first, with yaml tags `kind`, `version`, `name`.
    - `DestinationFile` embeds `Destination` with `yaml:",inline"`. Its `name` key comes from `Destination`.
    - The `ModelSpecFile` fields use the yaml keys `type`, `harness-version`, `version-constraint`, `principal`, `model`, `policy`, `note` and `claude`, each `omitempty` except `type`.
    - `spec()` and `modelSpecFileOf` copy field by field, so `HarnessVersion ↔ Version`.
  - **Role sub-structs:**
    - `RoleScopeFile{Destinations []string; Credentials map[string]string; Addressing []string; TTL string; Egress *[]string}`.
    - `AllocationFile{MaxEphemeral, MaxPersonal, MaxPersonalPerOwner int; IdleAfter, NagEvery, ReclaimAfter string; Standing []StandingFile}`, where `StandingFile{Name, Prompt string}`.
    - `TurnEndFile{IdleTimeout, OnIdle, TimeZone string}` and `CollaborateFile{Preview, SSH string}`.
    - The yaml keys are kebab-case (`max-personal-per-owner`, `idle-timeout`, `on-idle`, `time-zone`) and every one is `omitempty`.
    - `ContextFile{Core string \`yaml:"core,omitempty"\`; Leaves []sessionctx.Leaf \`yaml:"leaves,omitempty"\`}`.
  - **Role parsing:**
    - `ParseRoleFile` normalizes `Scope.Egress` with `NormalizeEgress`, so an error comes back as `<path>: scope.egress: …`.
    - Durations go through `parseConfigDuration` and are re-rendered with `formatConfigDuration`.
    - It refuses negative integers with `<path>: allocation.<key> must be >= 0`.
    - It does this by calling `f.role()` and replacing `f` with `roleFileOf(name, r, noNames)`, so a parsed file is already in canonical form.
  - **`roleFileOf`** maps `Scope.Addressing` entries that are exactly `user:<usr_id>` through `userName`, and leaves globs and other kinds verbatim.
  - **Project types:** `ProjectFile` uses `EscalationTierFile{Targets []string; Timeout string}`, `MemberFile{User string; Delivery []DeliveryFile}` (`DeliveryFile{Service, Address}`), `RoomFile{Key, Label string; Bindings []RoomBindingFile}` and `RoomBindingFile{Connection, Ref, Mode string}`.
  - **`normalize()`:**
    - sorts `Members` by `User` and `Rooms` by `Key`;
    - clears a `Label` equal to `Key` and a `Mode` equal to `both`;
    - canonicalizes each escalation `Timeout` (including the tiers in `EscalationByCategory`).
  - **`configFiles()`:**
    - `ProjectFile` renders `project.yml`, plus `context.md` via `RenderContextMarkdown` when `!Context.Empty()`.
    - `JamContextFile` renders `context.md` when its layer is not empty, else no files.
    - The rest render one file at `configKey().Path()`.
  - **`ParseContextMarkdown`:**
    - normalizes `\r\n` to `\n`;
    - accepts `---\n<yaml>\n---\n<body>` (an empty front matter `---\n---\n` too);
    - decodes the front matter strictly into `struct{ Leaves []sessionctx.Leaf \`yaml:"leaves"\` }`;
    - sets `Core = strings.TrimRight(body, "\n")`.
  - **`RenderContextMarkdown`** writes the front matter only when there are leaves, then the trimmed core plus `"\n"` when the core is non-empty.
  - **`Digest()`** is `hex.EncodeToString(sha256.Sum256(o.Canon))`.

- [ ] **Step 4: Run** the Step 2 command. Expected: PASS. Then run `go vet ./internal/jam/`.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_files.go internal/jam/config_files_test.go
git commit -m "feat(jam): config repo file kinds and their YAML codecs"
```

---

### Task 2: Managed marks and the atomic config write (store)

**Files:**
- Create: `internal/jam/config_marks.go`, `internal/jam/migrations/0016_config_marks.sql`, `internal/jam/pgstore_config.go`, `internal/jam/storetest/config.go`
- Modify: `internal/jam/store.go` (interface), `internal/jam/memstate.go` (`marks` field in `memState` and `newMemState`), `internal/jam/memstore.go` (`ApplyConfig`), `internal/jam/pgstore.go` (`load` reads `config_marks`), `internal/jam/pgstore_testhelpers.go` (truncate and reset `config_marks`), `internal/jam/storetest/conformance.go` (call `runConfigConformance`), `internal/jam/pgstore_integration_test.go` (`TestPostgresApplyConfigAtomic`)

**Interfaces:**
- Consumes: `ConfigKey` (Task 1), `checkKitDocs`, `copyKit`, `putConnectionTx`, `putUserTx`, `putAccountTx`, `upsertProjectTx`, `putMembershipTx`, `putChannelTx`, `memState.apply*`, `registryTx`.
- Produces:
```go
type ConfigMark struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Commit    string    `json:"commit"` // source commit; "dir" for a --dir apply
	Digest    string    `json:"digest"` // configObject.Digest() of the form applied
	AppliedAt time.Time `json:"applied_at"`
}
func (m ConfigMark) Key() ConfigKey

type ConfigRoleWrite struct {
	ProjectID ident.ID
	Role      Role
}
// ConfigWrite is one config apply's whole write set, in apply order. Removals
// of registry entities are tombstones in Connections/Users/Projects.
type ConfigWrite struct {
	Connections        []Connection
	Users              []User
	Accounts           []Account // accounts a user removal unlinks
	Destinations       []Destination
	RemoveDestinations []string
	ModelSpecs         []ModelSpec
	RemoveModelSpecs   []string
	Kits               []Kit // whole docs, as PutKitDocs
	RemoveKits         []string
	Projects           []Project // upserts; StatusRemoved = a tombstone
	RemoveMemberships  []Membership // by ProjectID + UserID
	Memberships        []Membership
	Channels           []Channel // rooms: creates, rebinds, archives
	RemoveRoles        []ConfigRoleWrite // by ProjectID + Role.Name
	Roles              []ConfigRoleWrite
	JamContext         *sessionctx.Layer // nil = untouched; empty = cleared
	Marks              []ConfigMark
	RemoveMarks        []ConfigKey
}
func (w ConfigWrite) Empty() bool

// Store gains:
//	ListConfigMarks() []ConfigMark          // sorted by kind, then name
//	ApplyConfig(w ConfigWrite) error        // every record or none
func checkConfigWrite(m *memState, w ConfigWrite) error
func applyConfigWrite(m *memState, w ConfigWrite)
```

- [ ] **Step 1: Write the failing conformance tests** in `storetest/config.go`, and register them in `RunConformance`:

```go
func runConfigConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	full := func() (jam.ConfigWrite, ident.ID, ident.ID, ident.ID) {
		uid, cid, pid := ident.New(ident.User), ident.New(ident.Connection), ident.New(ident.Project)
		return jam.ConfigWrite{
			Connections:  []jam.Connection{{ID: cid, Kind: "linear", Name: "acme", CredName: "linear", Status: jam.StatusLive}},
			Users:        []jam.User{{ID: uid, Name: "bob", Status: jam.StatusLive, Logins: []string{"bob@acme"}}},
			Destinations: []jam.Destination{{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com", CredName: "gh"}},
			ModelSpecs:   []jam.ModelSpec{{Name: "fast", Type: jam.HarnessClaude, Version: "2.1.3"}},
			Kits:         []jam.Kit{{Name: "web", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}},
			Projects:     []jam.Project{{ID: pid, Name: "app", ChatService: string(cid)}},
			Memberships:  []jam.Membership{{ProjectID: pid, UserID: uid}},
			Channels: []jam.Channel{{ID: ident.New(ident.Channel), ProjectID: pid, Kind: jam.SourceRoom, Key: "ops", Label: "ops",
				Status: jam.StatusLive, Bindings: []jam.Binding{{ConnectionID: cid, Ref: "ACME-1", Mode: jam.BindBoth}}}},
			Roles:      []jam.ConfigRoleWrite{{ProjectID: pid, Role: jam.Role{Name: "ic", Kit: "web", ModelSpec: "fast"}}},
			JamContext: &sessionctx.Layer{Core: "be kind"},
			Marks: []jam.ConfigMark{{Kind: jam.ConfigKindRole, Name: "app/ic", Commit: "dir", Digest: "d1", AppliedAt: time.Unix(100, 0).UTC()},
				{Kind: jam.ConfigKindDestination, Name: "gh", Commit: "dir", Digest: "d2", AppliedAt: time.Unix(100, 0).UTC()}},
		}, uid, cid, pid
	}
	t.Run("apply config writes every kind and the marks", func(t *testing.T) {
		st := mk(t)
		w, uid, cid, pid := full()
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		if u, ok := st.GetUser(uid); !ok || u.Name != "bob" || u.Logins[0] != "bob@acme" {
			t.Fatalf("user %+v", u)
		}
		if c, ok := st.GetConnection(cid); !ok || c.CredName != "linear" {
			t.Fatalf("connection %+v", c)
		}
		if _, ok := st.GetKit("web"); !ok {
			t.Fatal("kit")
		}
		if _, ok := st.GetModelSpec("fast"); !ok {
			t.Fatal("model-spec")
		}
		if p, ok := st.GetProject("app"); !ok || p.ID != pid {
			t.Fatalf("project %+v", p)
		}
		if _, ok := st.GetMembership(pid, uid); !ok {
			t.Fatal("membership")
		}
		if _, ok := st.ChannelByKey(pid, jam.SourceRoom, "ops"); !ok {
			t.Fatal("room")
		}
		if r, ok := st.GetRole("app", "ic"); !ok || r.Kit != "web" {
			t.Fatalf("role %+v", r)
		}
		if st.GetJamContext().Core != "be kind" {
			t.Fatal("jam context")
		}
		marks := st.ListConfigMarks()
		if len(marks) != 2 || marks[0].Kind != jam.ConfigKindDestination || marks[1].Name != "app/ic" {
			t.Fatalf("marks %+v", marks)
		}
	})
	t.Run("apply config removes and tombstones", func(t *testing.T) {
		st := mk(t)
		w, uid, cid, pid := full()
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		ch, _ := st.ChannelByKey(pid, jam.SourceRoom, "ops")
		ch.Status = jam.StatusArchived
		del := jam.ConfigWrite{
			RemoveRoles:       []jam.ConfigRoleWrite{{ProjectID: pid, Role: jam.Role{Name: "ic"}}},
			RemoveMemberships: []jam.Membership{{ProjectID: pid, UserID: uid}},
			Channels:          []jam.Channel{ch},
			Projects:          []jam.Project{{ID: pid, Name: "app", Status: jam.StatusRemoved}},
			RemoveKits:        []string{"web"}, RemoveModelSpecs: []string{"fast"}, RemoveDestinations: []string{"gh"},
			Connections:       []jam.Connection{{ID: cid, Kind: "linear", Name: "acme", Status: jam.StatusRemoved}},
			Users:             []jam.User{{ID: uid, Name: "bob", Status: jam.StatusRemoved}},
			JamContext:        &sessionctx.Layer{},
			RemoveMarks:       []jam.ConfigKey{{Kind: jam.ConfigKindRole, Name: "app/ic"}},
		}
		if err := st.ApplyConfig(del); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetProject("app"); ok {
			t.Fatal("project still live")
		}
		if _, ok := st.GetKit("web"); ok {
			t.Fatal("kit kept")
		}
		if u, _ := st.GetUser(uid); u.Status != jam.StatusRemoved {
			t.Fatalf("user %+v", u)
		}
		if !st.GetJamContext().Empty() || len(st.ListConfigMarks()) != 1 {
			t.Fatalf("context %+v marks %+v", st.GetJamContext(), st.ListConfigMarks())
		}
	})
	t.Run("a bad record late in the set writes nothing", func(t *testing.T) {
		st := mk(t)
		w, _, _, _ := full()
		w.Roles = append(w.Roles, jam.ConfigRoleWrite{ProjectID: ident.New(ident.Project), Role: jam.Role{Name: "orphan"}})
		if err := st.ApplyConfig(w); err == nil {
			t.Fatal("a role in an unknown project must be refused")
		}
		if len(st.ListUsers())+len(st.ListKits())+len(st.ListDestinations())+len(st.ListConfigMarks()) != 0 {
			t.Fatal("partial write")
		}
		if _, ok := st.GetProject("app"); ok {
			t.Fatal("project written")
		}
	})
	t.Run("an empty write is a no-op", func(t *testing.T) {
		st := mk(t)
		if err := st.ApplyConfig(jam.ConfigWrite{}); err != nil {
			t.Fatal(err)
		}
	})
}
```
  - **Postgres (`integration` tag), `TestPostgresApplyConfigAtomic`:**
    - Apply `full()`, but with the **last** role's name `"bad\x00role"`. Postgres text cannot hold NUL, so the failing insert runs after every earlier statement in the transaction.
    - Expect an error, and an empty store both in the cache and after `NewPostgresStore` reopens.
    - Then apply a good set and check that it survives a reopen, marks included.

- [ ] **Step 2: Run** `go test ./internal/jam/... -run Conformance -count=1`. Expected: FAIL to compile (`ApplyConfig`, `ConfigWrite`).

- [ ] **Step 3: Implement.**
  - **Migration `0016_config_marks.sql`:**
```sql
-- Config-in-git managed marks (docs/usage/jam/config-git.md): one row per
-- object the config repo manages, keyed by kind and name; doc is ConfigMark.
CREATE TABLE config_marks (
    kind       text        NOT NULL,
    name       text        NOT NULL,
    doc        jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, name)
);
```
  - **`memState`** gains `marks map[ConfigKey]ConfigMark`, initialized in `newMemState`. `ListConfigMarks` takes the read lock and returns the marks sorted by kind, then name.
  - **`checkConfigWrite`** (the caller holds the lock) refuses:
    - `checkKitDocs(w.Kits)`;
    - an empty name in any destination, model spec, project, role, user, connection or mark;
    - a role, membership or channel whose `ProjectID` is neither in `m.projectByID` nor among `w.Projects`;
    - a membership whose `UserID` is neither in `m.users` nor among `w.Users`.
    - Each error is `fmt.Errorf("config write: …")`.
  - **`applyConfigWrite`** applies in struct field order, using:
    - `applyPutConnection`, `applyPutUser` and `applyPutAccount`;
    - `applyPutDestination`/`applyRemoveDestination` and `applyPutModelSpec`/`applyRemoveModelSpec`;
    - `m.kits[k.Name] = copyKit(k)` and `applyRemoveKit`.
    - Projects: a `StatusRemoved` record is applied as `applyRemoveProject(p.Name)` when the live project with that name has that id, else as `m.removedProjects[p.ID] = p`. Any other record is applied as `applyPutProject`.
    - `applyRemoveMember`, `applyPutMembership` and `applyPutChannel`;
    - `applyRemoveRole`/`applyPutRole`;
    - `applySetJamContext` when `JamContext != nil`;
    - marks: put, then delete.
  - **`MemStore.ApplyConfig`:** `fs.mu.Lock(); if err := checkConfigWrite(fs.memState, w); err != nil { return err }; applyConfigWrite(fs.memState, w); return nil`.
  - **`PostgresStore.ApplyConfig`** (in `pgstore_config.go`):
    - under `s.mu.Lock()`, run `checkConfigWrite`;
    - `if w.Empty() { return nil }`;
    - run `s.registryTx("ApplyConfig", func(ctx, tx) error { return writeConfigTx(ctx, tx, w) })`;
    - then `applyConfigWrite(s.memState, w)`.
  - **`writeConfigTx`** issues, in this order:
    1. `putConnectionTx`, `putUserTx(ctx, tx, u, nil)` and `putAccountTx`.
    2. Destinations: the `PostgresStore.AddDestination` upsert SQL, then `DELETE FROM destinations WHERE name = $1`.
    3. Model specs and kits: the same upsert/delete pattern (`model_specs`, `kits`).
    4. `upsertProjectTx`.
    5. `DELETE FROM memberships WHERE project_id = $1 AND user_id = $2`, then `putMembershipTx`.
    6. `putChannelTx`.
    7. Roles: `DELETE FROM roles WHERE project_id = $1 AND name = $2`, then the `PutRole` upsert SQL.
    8. The Jam context: the `SetJamContext` upsert, or the delete for an empty layer.
    9. Marks: `INSERT INTO config_marks (kind, name, doc) VALUES ($1,$2,$3) ON CONFLICT (kind, name) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, then `DELETE FROM config_marks WHERE kind = $1 AND name = $2`.
  - **`load`:** after the jam settings, call `s.loadDocs(ctx, "config_marks", func(doc []byte) error { var mk ConfigMark; …; s.marks[mk.Key()] = mk })`.
  - **`TruncateAllForTest`** adds `config_marks` to the `TRUNCATE` and resets `s.marks`.

- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1` and `go vet -tags integration ./internal/jam/`. Expected: PASS. With a DSN, also run `JAM_TEST_POSTGRES_DSN=… go test -tags integration ./internal/jam/ -run 'Conformance|ApplyConfigAtomic' -count=1`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_marks.go internal/jam/migrations/0016_config_marks.sql internal/jam/pgstore_config.go internal/jam/storetest/config.go internal/jam/storetest/conformance.go internal/jam/store.go internal/jam/memstate.go internal/jam/memstore.go internal/jam/pgstore.go internal/jam/pgstore_testhelpers.go internal/jam/pgstore_integration_test.go
git commit -m "feat(jam): managed config marks and the atomic config write"
```

---

### Task 3: Rendering a Jam as a config tree (export)

**Files:**
- Create: `internal/jam/config_export.go`, `internal/jam/config_export_test.go`

**Interfaces:**
- Consumes: Task 1 file types, `newConfigObject` and `roleFileOf`; Task 2 `ConfigMark`; `ConfigSnapshot`, `applyImport`, `newMemState`, the `memState.applyPut*` helpers, `ListChannels`, `studio.ParseStudioKit`.
- Produces:
```go
// stateFromSnapshot is a private memState holding snap's config and registry
// (users, connections, accounts, memberships, rooms, standing sessions) and
// instances: the planner's and the exporter's scratch copy of a Jam.
func stateFromSnapshot(snap ConfigSnapshot, instances []Instance) *memState
// exportObjects renders every live config object of m in file form (ids →
// names; tombstones, archived rooms and empty context layers skipped). A
// kit's name@vN parent ref to a kit inTree reports renders as ../<name>.
func exportObjects(m *memState, inTree func(kit string) bool) (map[ConfigKey]configObject, error)
func projectFileOf(m *memState, p Project) ProjectFile
func renderKitFile(config string, inTree func(string) bool) ([]byte, error)

type ConfigTreeExport struct {
	Files  map[string]string `json:"files"`            // repo path → content
	Remove []string          `json:"remove,omitempty"` // managed paths Jam no longer has
}
// ExportConfigTree renders the managed objects (every live one with all) and,
// when serve is non-nil, jam.yml.
func ExportConfigTree(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, all bool) (ConfigTreeExport, error)
```

- [ ] **Step 1: Write the failing tests.** `populatedJam(t) (*MemStore, jamFixtureIDs)` builds a MemStore through the store and admin functions, and Task 8's round trip reuses it, so make it exhaustive. `jamFixtureIDs` is `struct{ bob, alice ident.ID }`. It must hold:
  - a destination (`gh`, with env and allow_paths), a model spec `fast`, and `default` (via `EnsureDefaultModelSpec`, then `UpdateModelSpec` with another harness version);
  - users `bob` (with login `bob@acme`) and `alice`;
  - connection `acme` (linear, cred `linear`);
  - kits `web-dev` and `app`, where `app` is `base: {kit: web-dev@v1}` and pushed through `PushStudioKit`;
  - project `app` with chat service `acme`, escalation `[{targets: [user:<bob id>], timeout: 1h}]`, member `bob` with a discord delivery, room `ops` on `acme`/`ACME-1`, and context with one leaf and one resource;
  - role `app/ic` with kit `app`, model spec `fast`, egress, a standing session, turn-end, collaborate and an inline context;
  - a Jam context.

```go
func TestExportConfigTreeAll(t *testing.T) {
	st, ids := populatedJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, map[string]any{"display-name": "Aethon"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"jam.yml", "context.md", "destinations/gh.yml", "model-specs/fast.yml", "model-specs/default.yml", "users/bob.yml",
		"users/alice.yml", "connections/acme.yml", "kits/web-dev/kit.yml", "kits/app/kit.yml",
		"projects/app/project.yml", "projects/app/context.md", "projects/app/roles/ic.yml"} {
		if _, ok := exp.Files[p]; !ok {
			t.Errorf("missing %s", p)
		}
	}
	proj := exp.Files["projects/app/project.yml"]
	for _, want := range []string{"chat-service: acme\n", "- user:bob\n", "user: bob\n", "connection: acme\n", "timeout: 1h\n"} {
		if !strings.Contains(proj, want) {
			t.Errorf("project.yml lacks %q:\n%s", want, proj)
		}
	}
	if strings.Contains(proj, string(ids.bob)) || strings.Contains(proj, "usr_") || strings.Contains(proj, "con_") {
		t.Fatalf("ids leaked:\n%s", proj)
	}
	if !strings.Contains(exp.Files["kits/app/kit.yml"], "kit: ../web-dev\n") {
		t.Fatalf("path parent not rendered:\n%s", exp.Files["kits/app/kit.yml"])
	}
	if strings.Contains(exp.Files["users/bob.yml"], "oidc") {
		t.Fatal("identities written")
	}
}

func TestExportConfigTreeDeterministic(t *testing.T) {
	st, _ := populatedJam(t)
	a, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	b, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two exports differ")
	}
}

func TestExportConfigTreeManagedOnly(t *testing.T) {
	st, _ := populatedJam(t)
	marks := []ConfigMark{{Kind: ConfigKindUser, Name: "bob"}, {Kind: ConfigKindKit, Name: "app"},
		{Kind: ConfigKindDestination, Name: "gone"}, {Kind: ConfigKindProject, Name: "old"}}
	exp, _ := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if _, ok := exp.Files["users/alice.yml"]; ok {
		t.Fatal("unmanaged user written without --all")
	}
	if !strings.Contains(exp.Files["kits/app/kit.yml"], "kit: web-dev@v1\n") {
		t.Fatal("a parent outside the tree must keep its stored ref")
	}
	want := []string{"destinations/gone.yml", "projects/old/context.md", "projects/old/project.yml"}
	if !slices.Equal(exp.Remove, want) {
		t.Fatalf("remove = %v, want %v", exp.Remove, want)
	}
}

func TestExportSkipsTombstonesAndEmptyContext(t *testing.T) {
	st, ids := populatedJam(t)
	if err := st.RemoveUser(ids.alice); err != nil {
		t.Fatal(err)
	}
	if err := st.SetJamContext(sessionctx.Layer{}); err != nil {
		t.Fatal(err)
	}
	exp, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if _, ok := exp.Files["users/alice.yml"]; ok {
		t.Fatal("tombstone written")
	}
	if _, ok := exp.Files["context.md"]; ok {
		t.Fatal("empty jam context written")
	}
}
```
- [ ] **Step 2: Run** `go test ./internal/jam/ -run TestExportConfig -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.**
  - **`stateFromSnapshot`:**
    - `m := newMemState(); applyImport(m, snap)`;
    - then add the registry: `applyPutConnection`, `applyPutUser`, `applyPutAccount`, `applyPutMembership`, `applyPutChannel`, `applyPutStandingSession`;
    - then `applyPutInstance` for each instance.
  - **`exportObjects`:**
    - **Destinations:** `DestinationFile{Kind: ConfigKindDestination, Version: 1, Destination: copyDestination(d)}`.
    - **Model specs:** `modelSpecFileOf`.
    - **Users:** live users only, as `UserFile{Name, Logins}`.
    - **Connections:** live ones only, as `ConnectionFile{Service: c.Kind, Cred: c.CredName}`.
    - **Projects:** live projects through `projectFileOf`.
    - **Roles:** `roleFileOf(name, r, userName)`, keyed `ConfigKey{ConfigKindRole, projectName + "/" + r.Name}`.
    - **The Jam context:** only when it is not empty.
    - **Kits:** each kit's current config becomes `configObject{Key, Files: {"kits/<n>/kit.yml": renderKitFile(cfg, inTree)}, Canon: []byte(cfg)}`.
  - **`projectFileOf`** converts:
    - escalation tiers, with targets that are exactly `user:<usr_id>` mapped to `user:<name>` and timeouts via `formatConfigDuration`;
    - `ChatService` (a connection id) to the connection's name;
    - members (`m.members[p.ID]`, each user name, delivery `{Service, Address}`);
    - live rooms from `m.ListChannels(p.ID, SourceRoom)`, each binding as its connection name, ref and mode;
    - `Resources` and `Context`;
    - and finally calls `normalize()`.
  - **`renderKitFile`:**
```go
func renderKitFile(config string, inTree func(string) bool) ([]byte, error) {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return nil, err
	}
	if name, v, err := sk.Base.ParentRef(); err == nil && v > 0 && inTree != nil && inTree(name) {
		sk.Base.Kit = "../" + name // a sibling in the tree: a path parent, re-pinned at apply
	}
	return renderYAML(sk), nil
}
```
  - **`ExportConfigTree`:**
    - `m := stateFromSnapshot(snap, nil)`;
    - `want(k) = all || managed[k]`;
    - `inTree(kit) = want(ConfigKey{ConfigKindKit, kit}) && m.kitExists(kit)`;
    - collect `Files` for each wanted object;
    - add `projects/<p>/context.md` to `Remove` for each wanted project with an empty context;
    - add every path of every managed key missing from the objects (for a project, `project.yml` and `context.md`) to `Remove`;
    - write `jam.yml` from `renderYAML(serve)` when `serve != nil`;
    - sort `Remove`.

- [ ] **Step 4: Run** `go test ./internal/jam/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): render a Jam's config as a repo tree"` (with the two new files).

---

### Task 4: Loading a config tree (and kit sets from `kits/`)

**Files:**
- Create: `internal/jam/config_tree.go`, `internal/jam/config_tree_test.go`
- Modify: `internal/jam/kitset.go` (`resolveKitEntries`; `resolveKitSet` becomes a wrapper)

**Interfaces:**
- Consumes: the Task 1 `Parse*File` functions and `ParseContextMarkdown`; `loadKitSet`, `checkoutDir`, `checkSetAncestor`, `validateKit`, `chainParent`, `nextKitVersion`, `overlayStore`; `studio.ReadFileInRoot`; `gitsource.Source`.
- Produces:
```go
const ConfigDirRepo = "local"  // KitSource.Repo of a --dir apply's kit versions
const ConfigDirCommit = "dir"  // ConfigMark.Commit of a --dir apply

type ConfigTree struct {
	Serve        map[string]any // jam.yml; nil = absent
	JamContext   *sessionctx.Layer
	Destinations map[string]DestinationFile
	ModelSpecs   map[string]ModelSpecFile
	Users        map[string]UserFile
	Connections  map[string]ConnectionFile
	Projects     map[string]ProjectFile
	Roles        map[string]RoleFile // "<project>/<role>"
	Kits         []kitSetEntry       // every kit under kits/, parents first; path refs as written
	Refusals     []string            // file-located load errors
}
func LoadConfigTree(root string) (*ConfigTree, error) // error only when root is unusable

// resolveKitEntries is resolveKitSet for any parents-first list: each path
// ref is rewritten to name@v<N> of the earlier entry at that dir; check (nil =
// none) vets entry i before it is validated.
func resolveKitEntries(store Store, set []kitSetEntry, check func(i int, e kitSetEntry) error) ([]kitSetEntry, error)
```

- [ ] **Step 1: Write the failing tests.** `writeTree(t, map[string]string) string` writes files under `t.TempDir()`.

```go
func TestLoadConfigTreeKinds(t *testing.T) {
	root := writeTree(t, map[string]string{
		"README.md":                    "ignored",
		"jam.yml":                      "display-name: Aethon\n",
		"context.md":                   "Be kind.\n",
		"destinations/gh.yml":          "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n",
		"users/bob.yml":                "kind: user\nversion: 1\n",
		"connections/acme.yml":         "kind: connection\nversion: 1\nservice: linear\n",
		"model-specs/fast.yml":         "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: a}\n",
		"kits/web-dev/kit.yml":         "kind: studio\negress: [registry.npmjs.org]\n",
		"kits/app/kit.yml":             "kind: studio\nbase: {kit: ../web-dev}\n",
		"projects/app/project.yml":     "kind: project\nversion: 1\nmembers: [{user: bob}]\n",
		"projects/app/context.md":      "Ship it.\n",
		"projects/app/roles/ic.yml":    "kind: role\nversion: 1\nkit: app\n",
	})
	tr, err := LoadConfigTree(root)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("err=%v refusals=%v", err, tr.Refusals)
	}
	if tr.Serve["display-name"] != "Aethon" || tr.JamContext.Core != "Be kind." || tr.Projects["app"].Context.Core != "Ship it." {
		t.Fatalf("tree %+v", tr)
	}
	if len(tr.Kits) != 2 || tr.Kits[0].Name != "web-dev" || tr.Kits[1].Name != "app" || tr.Kits[1].Dir != "kits/app" {
		t.Fatalf("kits %+v", tr.Kits)
	}
	if _, ok := tr.Roles["app/ic"]; !ok {
		t.Fatal("role")
	}
}

func TestLoadConfigTreeRefusals(t *testing.T) {
	root := writeTree(t, map[string]string{
		"users/bob.yaml":             "kind: user\nversion: 1\n",
		"users/sub/x.yml":            "kind: user\nversion: 1\n",
		"users/eve.yml":              "kind: user\nversion: 1\noidc: []\n",
		"kits/web.source.yml":        "kind: kit-source\n",
		"kits/stray.yml":             "x",
		"kits/empty/README":          "x",
		"projects/x/notes.txt":       "x",
		"projects/y/context.md":      "orphan\n",
		"projects/app/roles/ic.yml":  "kind: role\nversion: 1\nkit: web@v2\n",
	})
	tr, err := LoadConfigTree(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kits/empty: no kit.yml",
		"kits/stray.yml: unexpected file (kits are directories: kits/<name>/kit.yml)",
		"kits/web.source.yml: kit source references are not supported yet",
		"projects/app/roles/ic.yml: kit web@v2 pins a version; versions are per-Jam — name the kit",
		"projects/x/notes.txt: unexpected file",
		"projects/y/context.md: no projects/y/project.yml",
		"users/bob.yaml: use .yml",
		`users/eve.yml: unknown field "oidc"`,
		"users/sub: unexpected directory",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
}

func TestLoadKitsRefusesMisplacedParent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shared/kit.yml":        "kind: studio\n",
		"kits/app/kit.yml":      "kind: studio\nbase: {kit: ../../shared}\n",
		"kits/web/kit.yml":      "kind: studio\nname: web-dev\n",
		"kits/pinned/kit.yml":   "kind: studio\nbase: {kit: web@v3}\n",
		"kits/ok/kit.yml":       "kind: studio\n",
	})
	tr, _ := LoadConfigTree(root)
	joined := strings.Join(tr.Refusals, "\n")
	for _, want := range []string{
		"kits/app/kit.yml: parent ../../shared is not a kit under kits/ (kits/<name>/kit.yml)",
		"kits/web/kit.yml: ", // CheckName's own text follows: the name: differs from the directory
		"kits/pinned/kit.yml: base.kit web@v3 pins a version; versions are per-Jam — name the kit or use a path parent",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(tr.Kits) != 1 || tr.Kits[0].Name != "ok" {
		t.Fatalf("only the clean kit loads: %+v", tr.Kits)
	}
}

func TestLoadKitsOrderByRegistryParentInTree(t *testing.T) {
	root := writeTree(t, map[string]string{
		"kits/a/kit.yml": "kind: studio\nbase: {kit: z}\n", // follows z, which is in the tree
		"kits/z/kit.yml": "kind: studio\n",
	})
	tr, _ := LoadConfigTree(root)
	if len(tr.Kits) != 2 || tr.Kits[0].Name != "z" {
		t.Fatalf("order %+v refusals %v", tr.Kits, tr.Refusals)
	}
}

func TestResolveKitEntriesForest(t *testing.T) {
	st := NewMemStore()
	set := []kitSetEntry{
		{Name: "base", Dir: "kits/base", Kit: studio.StudioKit{Kind: "studio"}},
		{Name: "a", Dir: "kits/a", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}},
		{Name: "b", Dir: "kits/b", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../base"}}},
	}
	out, err := resolveKitEntries(st, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[1].Kit.Base.Kit != "base@v1" || out[2].Kit.Base.Kit != "base@v1" {
		t.Fatalf("siblings not both pinned to base: %+v", out)
	}
}
```
- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'TestLoadConfigTree|TestLoadKits|TestResolveKitEntries|TestSet|TestKitSet' -count=1`. Expected: the new tests FAIL and the slice-3 set tests PASS.

- [ ] **Step 3: Implement.**
  - **`kitset.go`:** replace `resolveKitSet`'s body with `return resolveKitEntries(store, set, func(i int, e kitSetEntry) error { if i < len(set)-1 { return checkSetAncestor(store, e.Name, repo, e.Dir) }; return nil })`. In `resolveKitEntries`, the parent rewrite is:
```go
if p, isPath := e.Kit.Base.ParentPath(); isPath {
	want := path.Join(checkoutDir(e.Dir), p)
	j := slices.IndexFunc(out[:i], func(o kitSetEntry) bool { return checkoutDir(o.Dir) == want })
	if j < 0 {
		return nil, writeErr(http.StatusBadRequest, "parent path %s (of %s) is not in this set", p, e.Name)
	}
	e.Kit.Base.Kit = fmt.Sprintf("%s@v%d", out[j].Name, out[j].Version)
}
```
    This behaves identically for a chain, and every slice-3 test must still pass.
  - **`LoadConfigTree`:**
    - Fail only when `root` cannot be opened (`os.OpenRoot(root)`).
    - List directories with `fs.ReadDir(r.FS(), dir)`.
    - Read every file with `studio.ReadFileInRoot(root, rel)`, so the read is jailed and only regular files are accepted.
    - Skip dot-entries.
  - **Flat dirs** (`destinations`, `model-specs`, `users`, `connections`):
    - a `<n>.yml` file → `Parse*File(path, n, data)`;
    - a `.yaml` file → `use .yml`;
    - a directory → `<dir>/<n>: unexpected directory`;
    - any other file → `unexpected file`.
  - **`projects/<p>/`:** `project.yml`, `context.md` (refused without `project.yml`) and `roles/<r>.yml` (`ParseRoleFile` plus the role pin check). Anything else is an unexpected file or directory. A dir holding only `roles/` is fine: its project need not be in the repo.
  - **`kits/`:**
    - **Each `<n>/` dir:**
      1. `loadKitSet(root, gitsource.Source{Repo: ConfigDirRepo, Dir: "kits/" + n}, n)`. An error becomes a refusal `kits/<n>/kit.yml: <err>`.
      2. A set entry with `e.Dir != "kits/"+e.Name` gives `kits/<n>/kit.yml: parent <p> is not a kit under kits/ (kits/<name>/kit.yml)`, where `<p>` is the child's `Base.Kit` as written.
      3. A registry pin (no path, `ParentRef` version > 0) gives the pin refusal.
      4. A refused set contributes no entries.
    - **Other files:** `<n>.source.yml` gives the slice-2 refusal, and any other file gives the "kits are directories" refusal.
    - **Order:** merge the clean sets by `Dir`, then order them parents first by Kahn's algorithm over two kinds of edge: path parents, and registry-name parents that are tree kits. Ties break by name. Kits left in a cycle give `kits: cycle among a, b`.
  - **`jam.yml`** is decoded with `yaml.Unmarshal` into `map[string]any`; a parse error is `jam.yml: not valid YAML`. **`context.md`** goes through `ParseContextMarkdown`.
  - **`Refusals`** are sorted.

- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1 && just lint`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): load a config repo tree, kits as one parents-first set"`

---

### Task 5: The serve-config overlay and host keys

**Files:**
- Create: `internal/jam/config_serve.go`, `internal/jam/config_serve_test.go`, `cmd/at-jam/serveconfig.go`, `cmd/at-jam/serveconfig_test.go`
- Modify: `cmd/at-jam/main.go` (`cmdServe`: repeatable `--config`; pass `portable` on for Task 8's `WithConfig`), `docs/usage/jam/serve.md` (a "Layering serve configs" section and the host-key table, which serve.md owns; frontmatter `owns` and `updated`)

**Interfaces:**
- Produces (package `jam`):
```go
// ServeHostKeys are the dotted serve-config paths that belong to one host.
var ServeHostKeys = []string{"listen", "admin-listen", "ui-hosts", "ui-origins", "dev-identity", "tls",
	"admin-tls", "store-postgres", "state-dir", "credentials-file", "runtime.launcher", "pool.store"}
func MergeServeConfigs(docs ...map[string]any) map[string]any // deep copy; maps merge, scalars/lists replace
func ServeHostKeysIn(doc map[string]any) []string             // in ServeHostKeys order
func HostKeyRefusals(file string, doc map[string]any) []string // "<file>: <key> is a host key; put it in jam.local.yml"
func PortableServeConfig(doc map[string]any) map[string]any    // deep copy minus host keys
func DiffServeConfig(repo, running map[string]any) []string    // differing dotted leaf paths, sorted

type ServeConfigDiff struct {
	State string   `json:"state"` // same | differs | absent | unknown
	Keys  []string `json:"keys,omitempty"`
}
```
- Produces (`cmd/at-jam`):
```go
type configPaths []string // flag.Value: repeatable --config
func (p *configPaths) String() string
func (p *configPaths) Set(v string) error
// loadServeConfigs reads, host-key-checks (first of ≥2) and merges paths in
// order; merged is YAML for parseServeConfig/unknownServeKeys.
func loadServeConfigs(paths []string, read func(string) ([]byte, error)) (merged []byte, portable map[string]any, err error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestMergeServeConfigs(t *testing.T) {
	repo := map[string]any{"display-name": "A", "ui-origins": []any{"x"}, "pool": map[string]any{"cred-name": "anthropic", "refresh-interval": "5m"}}
	local := map[string]any{"listen": ":443", "ui-origins": []any{"y"}, "pool": map[string]any{"store": "/var/pool.json"}}
	got := MergeServeConfigs(repo, local)
	want := map[string]any{"display-name": "A", "listen": ":443", "ui-origins": []any{"y"},
		"pool": map[string]any{"cred-name": "anthropic", "refresh-interval": "5m", "store": "/var/pool.json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	repo["pool"].(map[string]any)["cred-name"] = "mutated"
	if got["pool"].(map[string]any)["cred-name"] != "anthropic" {
		t.Fatal("merge aliases its input")
	}
}

func TestHostKeysAndPortable(t *testing.T) {
	doc := map[string]any{"listen": ":1", "display-name": "A", "runtime": map[string]any{"launcher": map[string]any{}, "listen": ":2"},
		"pool": map[string]any{"store": "/p", "cred-name": "c"}}
	if got := ServeHostKeysIn(doc); !slices.Equal(got, []string{"listen", "runtime.launcher", "pool.store"}) {
		t.Fatalf("host keys %v", got)
	}
	if r := HostKeyRefusals("jam.yml", doc); r[0] != "jam.yml: listen is a host key; put it in jam.local.yml" {
		t.Fatalf("refusal %v", r)
	}
	p := PortableServeConfig(doc)
	want := map[string]any{"display-name": "A", "runtime": map[string]any{"listen": ":2"}, "pool": map[string]any{"cred-name": "c"}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("portable %v", p)
	}
}

func TestDiffServeConfig(t *testing.T) {
	a := map[string]any{"display-name": "A", "kits": map[string]any{"git": []any{"x"}}, "pool": map[string]any{"refresh-interval": "5m"}}
	b := map[string]any{"display-name": "A", "kits": map[string]any{"git": []any{"y"}}, "metrics": map[string]any{"token-cred": "m"}}
	if got := DiffServeConfig(a, b); !slices.Equal(got, []string{"kits.git", "metrics.token-cred", "pool.refresh-interval"}) {
		t.Fatalf("diff %v", got)
	}
	if DiffServeConfig(a, a) != nil {
		t.Fatal("same must be nil")
	}
}
```
  In `cmd/at-jam/serveconfig_test.go`:

```go
func TestLoadServeConfigs(t *testing.T) {
	files := map[string]string{
		"jam.yml":       "display-name: A\nruntime:\n  listen: \":7\"\n",
		"jam.local.yml": "listen: \":443\"\nadmin-listen: 127.0.0.1:8081\n",
		"bad.yml":       "listen: \":1\"\n",
	}
	read := func(p string) ([]byte, error) { return []byte(files[p]), nil }
	merged, portable, err := loadServeConfigs([]string{"jam.yml", "jam.local.yml"}, read)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseServeConfig(merged)
	if err != nil || cfg.Listen != ":443" || cfg.DisplayName != "A" || cfg.Runtime.Listen != ":7" {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
	if _, ok := portable["listen"]; ok || portable["display-name"] != "A" {
		t.Fatalf("portable %v", portable)
	}
	if _, _, err := loadServeConfigs([]string{"bad.yml", "jam.local.yml"}, read); err == nil ||
		err.Error() != "bad.yml: listen is a host key; put it in jam.local.yml" {
		t.Fatalf("host key in the first file: %v", err)
	}
	if m, _, err := loadServeConfigs([]string{"bad.yml"}, read); err != nil || !strings.Contains(string(m), "listen") {
		t.Fatalf("one --config behaves as today: %v", err)
	}
}
```
- [ ] **Step 2: Run** `go test ./internal/jam/ ./cmd/at-jam/ -run 'Serve|HostKeys' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **Merging:** `MergeServeConfigs` deep-copies each doc and merges them recursively. When both sides of a key are `map[string]any` the maps merge; otherwise the later value replaces.
  - **Host keys:** `ServeHostKeysIn` walks each dotted path through nested maps.
  - **Diffing:** `DiffServeConfig` recurses while both sides are maps. Otherwise it compares with `reflect.DeepEqual`, and a key present on only one side counts as a difference.
  - **`loadServeConfigs`:**
    - unmarshal each file into `map[string]any`, reporting a parse error as `<path>: <err>`;
    - when `len(paths) > 1`, return the first entry of `HostKeyRefusals(paths[0], first)` as the error;
    - merge, `yaml.Marshal` the result, and compute `PortableServeConfig`.
  - **`cmdServe`:**
    - declare `var cfgPaths configPaths; fs.Var(&cfgPaths, "config", "serve config YAML (repeatable: later files override; only the first may omit host keys — see serve.md)")`;
    - keep the `--config is required` message when the list is empty;
    - replace `os.ReadFile` + `parseServeConfig(data)` with `merged, portable, err := loadServeConfigs(cfgPaths, os.ReadFile)`;
    - pass `merged` to `parseServeConfig` and `unknownServeKeys`, and keep `portable` for Task 8.
  - **Docs:** in `serve.md`, document `--config jam.yml --config jam.local.yml`, the merge rule, the host-key table and the refusal. Link to `config-git.md`, which Task 9 writes; add the link in Task 9 if the file doesn't exist yet.
- [ ] **Step 4: Run** `go test ./internal/jam/ ./cmd/at-jam/ -count=1 && just lint`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): layered serve configs and the host-key table"`

---

### Task 6: The planner — classification and the create/update write set

**Files:**
- Create: `internal/jam/config_plan.go`, `internal/jam/config_plan_test.go`
- Modify: `internal/jam/roleedit.go` (`checkStandingDecl`, extracted from `AddStanding`, which keeps calling it)

**Interfaces:**
- Consumes: Tasks 1–5 (`ConfigTree`, `exportObjects`, `stateFromSnapshot`, `ConfigWrite`, `ConfigMark`, `resolveKitEntries`, `buildKitDocs`, `HostKeyRefusals`, `DiffServeConfig`); the validators `ValidateDestination`, `ValidateModelSpec`, `prepareModelSpec`, `ValidateCredentials`, `ValidateTurnEnd`, `CollaboratePolicy.Validate`, `checkRoleModelSpec`, `sessionctx.ValidateLayer`, `validateProjectContext` and `ValidateEntityName`; the scratch-state prepares `prepareCreateUser`, `prepareSetUserLogins`, `prepareCreateConnection`, `prepareSetConnectionCred`, `checkCreateProject`, `preparePutMembership`, `prepareCreateChannel`, `checkBindings` and `prepareArchiveChannel`; and the scratch lookups `liveUserNamed` and `liveConnectionNamed`.
- Produces:
```go
const (
	ConfigCreate    = "create"
	ConfigUpdate    = "update"
	ConfigDelete    = "delete"
	ConfigUnchanged = "unchanged"
	ConfigAdopt     = "adopt"
)
type ConfigOp struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Drift  bool   `json:"drift,omitempty"`
	Path   string `json:"path,omitempty"`
	Before string `json:"before,omitempty"` // Jam's canonical form (update, delete)
	After  string `json:"after,omitempty"`  // the repo's (create, update)
}
type ConfigPlan struct {
	Ops         []ConfigOp      `json:"ops"`
	Unmanaged   []ConfigKey     `json:"unmanaged,omitempty"`
	Refusals    []string        `json:"refusals,omitempty"`
	ServeConfig ServeConfigDiff `json:"serve_config"`
	Digest      string          `json:"digest"`
	write       ConfigWrite
}
func (p ConfigPlan) Counts() map[string]int // by action

type ConfigEnv struct {
	CredExists     func(string) bool
	PoolConfigured bool
	Serve          map[string]any // the running serve's portable config; nil = unknown
}
type ConfigPlanInput struct {
	Tree      *ConfigTree
	Snapshot  ConfigSnapshot
	Instances []Instance
	Marks     []ConfigMark
	Env       ConfigEnv
	Source    gitsource.Source // {Repo: ConfigDirRepo} for --dir
	Commit    string           // ConfigDirCommit for --dir
	Force     bool
	Now       time.Time
}
func PlanConfig(in ConfigPlanInput) ConfigPlan
func checkStandingDecl(have []StandingSession, s StandingSession) error // roleedit.go
```

- [ ] **Step 1: Write the failing tests** (package `jam`). The helpers:
  - `treeOf(t, files)` is `writeTree` followed by `LoadConfigTree`.
  - `planOn(t, st, tr, force)` calls `PlanConfig` with `st.ExportConfig()`, `st.ListInstances()`, `st.ListConfigMarks()` and `allCreds`.
  - `applyPlan(t, st, p)` calls `st.ApplyConfig(p.write)`.

```go
var allCreds = ConfigEnv{CredExists: func(string) bool { return true }}

func opOf(p ConfigPlan, kind, name string) ConfigOp {
	for _, o := range p.Ops {
		if o.Kind == kind && o.Name == name {
			return o
		}
	}
	return ConfigOp{}
}

func TestPlanCreatesInOrder(t *testing.T) {
	tr := treeOf(t, fullTreeFiles) // every kind once; role app/ic binds kit app (../web-dev) and model-spec fast
	p := planOn(t, NewMemStore(), tr, false)
	if len(p.Refusals) > 0 {
		t.Fatal(p.Refusals)
	}
	var order []string
	for _, o := range p.Ops {
		order = append(order, o.Kind)
	}
	want := []string{ConfigKindUser, ConfigKindConnection, ConfigKindDestination, ConfigKindModelSpec,
		ConfigKindKit, ConfigKindKit, ConfigKindProject, ConfigKindRole, ConfigKindJamContext}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v", order)
	}
	if p.write.Kits[1].Versions[1] == "" || !strings.Contains(p.write.Kits[1].Versions[1], `"web-dev@v1"`) {
		t.Fatalf("app not pinned to its parent: %+v", p.write.Kits)
	}
	if m := p.write.Kits[0].Meta[1]; m.Source == nil || m.Source.Repo != ConfigDirRepo || m.Source.Dir != "kits/web-dev" {
		t.Fatalf("provenance %+v", m)
	}
	if len(p.write.Marks) != len(p.Ops) || p.write.Marks[0].Commit != ConfigDirCommit {
		t.Fatalf("marks %+v", p.write.Marks)
	}
}

func TestPlanTable(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"destinations/gh.yml": ghDest})
	applyPlan(t, st, planOn(t, st, tr, false))

	if o := opOf(planOn(t, st, tr, false), ConfigKindDestination, "gh"); o.Action != ConfigUnchanged {
		t.Fatalf("re-plan %+v", o)
	}
	changed := treeOf(t, map[string]string{"destinations/gh.yml": ghDest + "note: hi\n"})
	if o := opOf(planOn(t, st, changed, false), ConfigKindDestination, "gh"); o.Action != ConfigUpdate || o.Drift {
		t.Fatalf("update %+v", o)
	}
	d, _ := findDestination(st, "gh")
	d.Note = "edited in Jam"
	_ = st.AddDestination(d)
	p := planOn(t, st, changed, false)
	if o := opOf(p, ConfigKindDestination, "gh"); !o.Drift || !slices.Contains(p.Refusals,
		"destination gh drifted (edited in Jam since config @dir); re-run with --force or config write it first") {
		t.Fatalf("drift %+v %v", o, p.Refusals)
	}
	if p := planOn(t, st, changed, true); len(p.Refusals) > 0 || !opOf(p, ConfigKindDestination, "gh").Drift {
		t.Fatalf("forced %+v", p)
	}
	_ = st.AddDestination(Destination{Name: "other", Route: "/o/", Upstream: "https://o.example"})
	same := treeOf(t, map[string]string{"destinations/gh.yml": ghDest + "note: edited in Jam\n",
		"destinations/other.yml": "kind: destination\nversion: 1\nroute: /o/\nupstream: https://o.example\n"})
	if o := opOf(planOn(t, st, same, false), ConfigKindDestination, "other"); o.Action != ConfigAdopt {
		t.Fatalf("adopt %+v", o)
	}
	differs := treeOf(t, map[string]string{"destinations/other.yml": "kind: destination\nversion: 1\nroute: /x/\nupstream: https://o.example\n"})
	if p := planOn(t, st, differs, false); !slices.Contains(p.Refusals,
		"destination other exists and is not managed; config write --all it into the repo, or rename") {
		t.Fatalf("refusal %v", p.Refusals)
	}
	if p := planOn(t, st, treeOf(t, nil), false); !slices.Contains(p.Unmanaged, ConfigKey{ConfigKindDestination, "other"}) {
		t.Fatalf("unmanaged %v", p.Unmanaged)
	}
}

func TestPlanReferencesResolveAgainstPlannedState(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: ghost}]\nescalation: [{targets: [user:bob]}]\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web-dev\nmodel-spec: nope\n",
		"users/bob.yml":             "kind: user\nversion: 1\n",
	})
	p := planOn(t, NewMemStore(), tr, false)
	for _, want := range []string{
		"projects/app/project.yml: member ghost is not a user in the repo or Jam",
		"projects/app/roles/ic.yml: kit web-dev not found (not in the repo or Jam)",
	} {
		if !slices.Contains(p.Refusals, want) {
			t.Errorf("missing %q in %v", want, p.Refusals)
		}
	}
	if slices.ContainsFunc(p.Refusals, func(s string) bool { return strings.Contains(s, "user:bob") }) {
		t.Fatal("bob is in the repo: escalation must resolve")
	}
}

func TestPlanSeededDefaultModelSpec(t *testing.T) {
	st := NewMemStore()
	if _, err := EnsureDefaultModelSpec(st, false); err != nil {
		t.Fatal(err)
	}
	tr := treeOf(t, map[string]string{"model-specs/default.yml": "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.99\nprincipal: {credential: anthropic}\n"})
	p := planOn(t, st, tr, false)
	if len(p.Refusals) > 0 || opOf(p, ConfigKindModelSpec, "default").Action != ConfigUpdate {
		t.Fatalf("%+v", p)
	}
}

func TestPlanReapplyIsUnchanged(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, unnormalizedTreeFiles) // egress GitHub.com, ttl 90m, room label == key, mode both, a delivery, core without "\n"
	applyPlan(t, st, planOn(t, st, tr, false))
	again := planOn(t, st, tr, false)
	for _, o := range again.Ops {
		if o.Action != ConfigUnchanged || o.Drift {
			t.Errorf("%s %s: %s drift=%v\nbefore:\n%s\nafter:\n%s", o.Kind, o.Name, o.Action, o.Drift, o.Before, o.After)
		}
	}
	exp, _ := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), nil, false)
	if p := planOn(t, st, treeOf(t, exp.Files), false); len(p.Refusals) > 0 || p.Counts()[ConfigUnchanged] != len(p.Ops) {
		t.Fatalf("write-then-plan is not a no-op: %+v", p)
	}
}

func TestPlanPinsRefused(t *testing.T) {
	p := planOn(t, NewMemStore(), treeOf(t, map[string]string{"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web@v2\n"}), false)
	if !slices.Contains(p.Refusals, "projects/app/roles/ic.yml: kit web@v2 pins a version; versions are per-Jam — name the kit") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanHostKeyInRepoJamYML(t *testing.T) {
	p := planOn(t, NewMemStore(), treeOf(t, map[string]string{"jam.yml": "listen: \":443\"\n"}), false)
	if !slices.Contains(p.Refusals, "jam.yml: listen is a host key; put it in jam.local.yml") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanServeConfigDiff(t *testing.T) {
	in := ConfigPlanInput{Tree: treeOf(t, map[string]string{"jam.yml": "display-name: A\n"}), Env: ConfigEnv{
		CredExists: allCreds.CredExists, Serve: map[string]any{"display-name": "B"}}, Commit: ConfigDirCommit, Now: time.Now()}
	if sc := PlanConfig(in).ServeConfig; sc.State != "differs" || !slices.Equal(sc.Keys, []string{"display-name"}) {
		t.Fatalf("%+v", sc)
	}
}

func TestPlanRefusalWritesNothing(t *testing.T) {
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n", "kits/app/kit.yml": "kind: studio\nbase: {kit: ../../x}\n"})
	p := planOn(t, NewMemStore(), tr, false)
	if len(p.Refusals) == 0 {
		t.Fatal("expected a refusal")
	}
	// Task 8's ApplyConfigTree refuses a plan with refusals before ApplyConfig; this pins the input it must refuse.
}

func TestPlanDigestStable(t *testing.T) {
	tr := treeOf(t, fullTreeFiles)
	a, b := planOn(t, NewMemStore(), tr, false), planOn(t, NewMemStore(), tr, false)
	if a.Digest == "" || a.Digest != b.Digest {
		t.Fatalf("%q vs %q", a.Digest, b.Digest)
	}
}
```
  `fullTreeFiles`, `unnormalizedTreeFiles` and `ghDest` are package-level test fixtures in `config_plan_test.go`, written out in full as YAML string maps. `fullTreeFiles` covers every kind:
  - users `bob` and `alice`, and connection `acme`;
  - destination `gh` and model spec `fast`;
  - kits `web-dev` and `app` (`../web-dev`);
  - project `app`, whose `members` are written in flow style with `bob` first (`members: [{user: bob, delivery: [...]}]`) and whose escalation names `user:alice`, plus an escalation category, room `ops` on `acme`, chat service `acme` and a `context.md`;
  - role `app/ic` (kit `app`, model spec `fast`, a standing session, an inline context);
  - a Jam `context.md`.

- [ ] **Step 2: Run** `go test ./internal/jam/ -run TestPlan -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement `config_plan.go`.** The skeleton:

```go
// seededConfigObjects are the objects every serve creates for itself: on a
// fresh replica they exist unmanaged, so a differing repo copy is adopted and
// updated instead of refused.
var seededConfigObjects = map[ConfigKey]bool{{ConfigKindModelSpec, DefaultModelSpec}: true}

type planner struct {
	in    ConfigPlanInput
	m     *memState // the planned state: the Jam with the plan's writes applied so far
	store *MemStore // m as a Store (kit resolution, model-spec checks)
	live  map[ConfigKey]configObject
	marks map[ConfigKey]ConfigMark
	repo  map[ConfigKey]bool
	plan  ConfigPlan
	w     ConfigWrite
}

func PlanConfig(in ConfigPlanInput) ConfigPlan {
	m := stateFromSnapshot(in.Snapshot, in.Instances)
	live, err := exportObjects(m, nil)
	p := &planner{in: in, m: m, store: &MemStore{memState: m}, live: live, marks: map[ConfigKey]ConfigMark{}, repo: map[ConfigKey]bool{}}
	if err != nil {
		p.refuse("jam: %s", err)
	}
	for _, mk := range in.Marks {
		p.marks[mk.Key()] = mk
	}
	p.plan.Refusals = append(p.plan.Refusals, in.Tree.Refusals...)
	p.planServe()
	p.planUsers()
	p.planConnections()
	p.planDestinations()
	p.planModelSpecs()
	p.planKits()
	p.planProjects()
	p.planRoles()
	p.planJamContext()
	p.planDeletes() // Task 7
	p.planUnmanaged()
	slices.Sort(p.plan.Refusals)
	p.plan.Refusals = slices.Compact(p.plan.Refusals)
	p.plan.write = p.w
	p.plan.Digest = planDigest(p.plan)
	return p.plan
}

// classify is §3's table for an object the repo holds. ok=false: refused.
func (p *planner) classify(key ConfigKey, repo configObject) (action string, drift, ok bool) {
	p.repo[key] = true
	cur, exists := p.live[key]
	mark, managed := p.marks[key]
	switch {
	case !exists:
		return ConfigCreate, false, true
	case !managed && bytes.Equal(cur.Canon, repo.Canon):
		return ConfigAdopt, false, true
	case !managed && seededConfigObjects[key]:
		return ConfigUpdate, false, true
	case !managed:
		p.refuse("%s exists and is not managed; config write --all it into the repo, or rename", key)
		return "", false, false
	case bytes.Equal(cur.Canon, repo.Canon):
		return ConfigUnchanged, false, true
	}
	drift = cur.Digest() != mark.Digest
	if drift && !p.in.Force {
		p.refuse("%s drifted (edited in Jam since config @%s); re-run with --force or config write it first", key, shortCommit(mark.Commit))
		p.op(key, ConfigUpdate, true, cur.Canon, repo.Canon) // the diff shows both sides
		return "", true, false
	}
	return ConfigUpdate, drift, true
}

// upsert classifies key and, for a create or update, runs write against the
// planned state; a write error is a refusal located at the object's file.
func (p *planner) upsert(key ConfigKey, repo configObject, write func(create bool) error) {
	action, drift, ok := p.classify(key, repo)
	if !ok {
		return
	}
	if action == ConfigCreate || action == ConfigUpdate {
		if err := write(action == ConfigCreate); err != nil {
			p.refuse("%s: %s", key.Path(), err)
			return
		}
	}
	var before []byte
	if action == ConfigUpdate {
		before = p.live[key].Canon
	}
	var after []byte
	if action == ConfigCreate || action == ConfigUpdate {
		after = repo.Canon
	}
	p.op(key, action, drift, before, after)
	p.w.Marks = append(p.w.Marks, ConfigMark{Kind: key.Kind, Name: key.Name, Commit: p.in.Commit, Digest: repo.Digest(), AppliedAt: p.in.Now})
}

func (p *planner) refuse(format string, a ...any) {
	p.plan.Refusals = append(p.plan.Refusals, fmt.Sprintf(format, a...))
}

func (p *planner) op(key ConfigKey, action string, drift bool, before, after []byte) {
	p.plan.Ops = append(p.plan.Ops, ConfigOp{Kind: key.Kind, Name: key.Name, Action: action, Drift: drift,
		Path: key.Path(), Before: string(before), After: string(after)})
}

func (o ConfigPlan) Counts() map[string]int {
	n := map[string]int{}
	for _, op := range o.Ops {
		n[op.Action]++
	}
	return n
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func planDigest(p ConfigPlan) string {
	b, _ := json.Marshal(struct {
		Ops       []ConfigOp
		Unmanaged []ConfigKey
		Refusals  []string
	}{p.Ops, p.Unmanaged, p.Refusals})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```
  The per-kind `plan*` functions iterate `slices.Sorted(maps.Keys(tree.X))`, call `upsert(newConfigObject(f).Key, newConfigObject(f), write)`, and write as follows:
  - **Users:**
    - create: `u, err := p.m.prepareCreateUser(User{Name: f.Name, Logins: f.Logins})`;
    - update: `p.m.prepareSetUserLogins(liveUser.ID, f.Logins)`;
    - then `p.m.applyPutUser(u)` and `p.w.Users = append(p.w.Users, u)`.
  - **Connections:**
    - create: `prepareCreateConnection(Connection{Kind: f.Service, Name: f.Name, CredName: f.Cred})`;
    - update: a changed service is `service cannot change (linear → discord); remove the file, apply, then add it back`, otherwise `prepareSetConnectionCred(id, f.Cred)`.
  - **Destinations:** `ValidateDestination(f.Destination, p.in.Env.CredExists)`, then `applyPutDestination` and append.
  - **Model specs:** `ValidateModelSpec(f.spec(), CredExists, PoolConfigured)`, then `c, err := prepareModelSpec(f.spec())`, apply and append.
  - **Kits:**
    1. `entries, err := resolveKitEntries(p.store, p.in.Tree.Kits, nil)`. An error is refused as `kits: <err>` (the message names the kit).
    2. For each entry, the repo side is `configObject{Key: {kit, e.Name}, Files: nil, Canon: []byte(e.text)}`.
    3. Collect the create/update entries (`classify` without `upsert`'s write, then the op and mark).
    4. `docs := buildKitDocs(p.store, changed, gitsource.Source{Repo: p.in.Source.Repo, Ref: p.in.Source.Ref}, provenanceCommit(p.in.Commit), p.in.Now, false)`, where `provenanceCommit("dir")` is `""`.
    5. `p.m.kits[d.Name] = copyKit(d)` for each doc, and `p.w.Kits = docs`.
  - **Projects:** `writeProject(f, create)`:
    - **Record:** create via `checkCreateProject` + `newProject`; update via `copyProject(p.m.projects[name])`.
    - **Escalation:** each target `user:<name>` without a glob character becomes `user:<id>` via `liveUserNamed`. An unknown name is `escalation names user <n>, who is not in the repo or Jam`. Timeouts go through `parseConfigDuration`.
    - **Chat service:** the connection name becomes its id; an unknown one is `chat-service connection <n> is not in the repo or Jam`.
    - **Context:** `validateProjectContext(f.Context, f.Resources)`, then `setProjectContext`, then apply and append.
    - **Members:**
      - desired: `preparePutMembership` for each `MemberFile`. An unknown user is `member <n> is not a user in the repo or Jam`.
      - write the ones that differ from `p.m.members[pid]`;
      - remove the rest (`RemoveMemberships`, `applyRemoveMember`).
    - **Rooms:**
      - a binding connection name must resolve, else `room <key>: connection <n> is not in the repo or Jam`;
      - an existing live room with a changed label or bindings: copy it, `checkBindings(bs, ch.ID)`, and update the label and bindings;
      - a new room: `ValidateEntityName(key)` + `prepareCreateChannel(Channel{ProjectID, Kind: SourceRoom, Key, Label: cmp.Or(label, key), Bindings})`;
      - a live room missing from the file: `prepareArchiveChannel`;
      - each result is applied with `applyPutChannel` and appended to `w.Channels`.
  - **Roles:**
    - The role's project must be live in `p.m`, else `project <p> is not in the repo or Jam`.
    - `r, err := f.role()`, then `validateConfigRole`.
    - Then `p.m.applyPutRole(pid, r)` and append a `ConfigRoleWrite`.
  - **The Jam context:** `sessionctx.ValidateLayer(l, sessionctx.BudgetJam)`. An empty tree layer counts as absent: no op, and a managed one is handled by the deletes.
  - **The serve config** (`planServe`) appends `HostKeyRefusals("jam.yml", tree.Serve)`. Then:
    - `Serve == nil` (no running serve config) → `unknown`;
    - `tree.Serve == nil` → `absent`;
    - a non-empty diff → `differs` with its keys;
    - otherwise → `same`.

```go
// validateConfigRole is the admin API's role-write rule set (POST /admin/roles
// plus the egress, standing and context writers), over the planned state.
func validateConfigRole(store Store, r *Role, credExists func(string) bool) error {
	if err := r.Collaborate.Validate(); err != nil {
		return err
	}
	if err := ValidateTurnEnd(r.TurnEnd); err != nil {
		return err
	}
	if r.Kit != "" {
		if _, ok := store.GetKit(r.Kit); !ok {
			return fmt.Errorf("kit %s not found (not in the repo or Jam)", r.Kit)
		}
	}
	if err := checkRoleModelSpec(store, r.ModelSpec); err != nil {
		return err
	}
	if err := ValidateCredentials(r.Scope, credExists); err != nil {
		return err
	}
	if err := sessionctx.ValidateLayer(r.Context, sessionctx.BudgetRole); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	var seen []StandingSession
	for _, s := range r.Allocation.Standing {
		if err := checkStandingDecl(seen, s); err != nil {
			return err
		}
		seen = append(seen, s)
	}
	return nil
}
```
  - **`checkStandingDecl`** carries `AddStanding`'s current rules: name and prompt required, no `/`, unique. `AddStanding` keeps using it, so its tests stay green. The duplicate message reads `standing session %q is declared twice`, and `AddStanding` keeps its own role-qualified text for that case.
- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1 && just lint`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): plan a config tree against a Jam (creates and updates)"`

---

### Task 7: The planner — deletes and delete guards

**Files:**
- Create: `internal/jam/config_plan_delete.go`, `internal/jam/config_plan_delete_test.go`

**Interfaces:**
- Consumes: Task 6's `planner` and `ConfigWrite`; `checkRemoveProject`, `projectChannels`, `RoleReferencingKit`, `KitsReferencing`, `checkRemoveModelSpec`, `prepareRemoveConnection`, `prepareRemoveUser`, `canonicalProject`, `PhaseGone`.
- Produces:
```go
func (p *planner) planDeletes()   // managed and not in the repo, reverse kind order
func (p *planner) planUnmanaged() // live, not in the repo, not managed → Unmanaged
func (p *planner) deleteObject(key ConfigKey) error // the guard's reason, or nil
```

- [ ] **Step 1: Write the failing tests.** Each starts by applying `fullTreeFiles` to a MemStore, then plans a tree with the object removed:

```go
func TestPlanDeletesReverseOrder(t *testing.T) {
	st := appliedFull(t)
	p := planOn(t, st, treeOf(t, nil), false)
	if len(p.Refusals) > 0 {
		t.Fatal(p.Refusals)
	}
	var kinds []string
	for _, o := range p.Ops {
		if o.Action == ConfigDelete {
			kinds = append(kinds, o.Kind)
		}
	}
	want := []string{ConfigKindRole, ConfigKindProject, ConfigKindKit, ConfigKindKit, ConfigKindModelSpec,
		ConfigKindDestination, ConfigKindConnection, ConfigKindUser, ConfigKindJamContext}
	if !slices.Equal(kinds, want) {
		t.Fatalf("delete order %v", kinds)
	}
	if kits := p.write.RemoveKits; !slices.Equal(kits, []string{"app", "web-dev"}) {
		t.Fatalf("children first: %v", kits)
	}
	applyPlan(t, st, p)
	if len(st.ListConfigMarks()) != 0 || len(st.ListKits()) != 0 {
		t.Fatal("not deleted")
	}
}

func TestPlanDeleteGuards(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(t *testing.T, st *MemStore)
		remove []string // repo paths dropped from fullTreeFiles
		want   string
	}{
		"kit bound by a kept role": {remove: []string{"kits/app/kit.yml"}, want: "cannot delete kit app: role app/ic binds it"},
		"kit a kept kit builds on": {remove: []string{"kits/web-dev/kit.yml"}, want: "cannot delete kit web-dev: kit app builds on it"},
		"model-spec a role binds":  {remove: []string{"model-specs/fast.yml"}, want: "cannot delete model-spec fast: "},
		"role with a live session": {setup: liveSessionOf("app", "ic"), remove: []string{"projects/app/roles/ic.yml"},
			want: "cannot delete role app/ic: live session s1 uses it"},
		"user in escalation": {remove: []string{"users/alice.yml"}, want: "cannot delete user alice: project app's escalation names them"},
		"user owning a session": {setup: personalSessionOf("bob"), remove: []string{"users/bob.yml"},
			want: "cannot delete user bob: they own the live personal session s2"},
		"project with a live session": {setup: liveSessionOf("app", "ic"), remove: []string{"projects/app/project.yml", "projects/app/context.md", "projects/app/roles/ic.yml"},
			want: "cannot delete project app: "},
		"connection a room binds": {remove: []string{"connections/acme.yml"}, want: "cannot delete connection acme: "},
	} {
		t.Run(name, func(t *testing.T) {
			st := appliedFull(t)
			if tc.setup != nil {
				tc.setup(t, st)
			}
			p := planOn(t, st, treeOf(t, without(fullTreeFiles, tc.remove...)), false)
			if !slices.ContainsFunc(p.Refusals, func(s string) bool { return strings.HasPrefix(s, tc.want) }) {
				t.Fatalf("refusals %v, want prefix %q", p.Refusals, tc.want)
			}
		})
	}
}

func TestPlanUserDeleteRefusedByUnmanagedMembership(t *testing.T) {
	st := appliedFull(t)
	if err := st.CreateProject("side"); err != nil {
		t.Fatal(err)
	}
	side, _ := st.GetProject("side")
	bob := userNamed(t, st, "bob")
	if err := st.AddMember(side.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	files := without(fullTreeFiles, "users/bob.yml")
	files["projects/app/project.yml"] = strings.Replace(files["projects/app/project.yml"], "{user: bob", "{user: alice", 1)
	p := planOn(t, st, treeOf(t, files), false)
	if !slices.Contains(p.Refusals, "cannot delete user bob: project side lists them as a member") {
		t.Fatal(p.Refusals)
	}
	if _, ok := st.GetMembership(side.ID, bob.ID); !ok {
		t.Fatal("planning touched the store")
	}
}

func TestPlanDeleteDrift(t *testing.T) {
	st := appliedFull(t)
	d, _ := findDestination(st, "gh")
	d.Note = "edited"
	_ = st.AddDestination(d)
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")), false)
	if !slices.Contains(p.Refusals, "destination gh drifted (edited in Jam since config @dir); re-run with --force or config write it first") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanForgetsMarkOfVanishedObject(t *testing.T) {
	st := appliedFull(t)
	_ = st.RemoveDestination("gh")
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")), false)
	if opOf(p, ConfigKindDestination, "gh").Action != "" || !slices.Contains(p.write.RemoveMarks, ConfigKey{ConfigKindDestination, "gh"}) {
		t.Fatalf("%+v", p)
	}
}

func TestPlanUnmanagedUntouched(t *testing.T) {
	st := appliedFull(t)
	_, _ = st.CreateUser(User{Name: "carol"})
	p := planOn(t, st, treeOf(t, fullTreeFiles), false)
	if !slices.Contains(p.Unmanaged, ConfigKey{ConfigKindUser, "carol"}) || opOf(p, ConfigKindUser, "carol").Action != "" {
		t.Fatalf("%+v", p)
	}
}
```
  The helpers live in the test file:
  - `appliedFull` applies `fullTreeFiles`, which includes user `alice` named in `app`'s escalation.
  - `without` copies the file map minus some paths.
  - `liveSessionOf` uses `PutInstance` with `Phase: PhaseRunning`, `ActorID: "s1"` and the project id.
  - `personalSessionOf` uses an instance with `OwnerID` set and `ActorID: "s2"`.
  - `userNamed` looks a user up by name.

- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'TestPlanDelete|TestPlanUser|TestPlanForgets|TestPlanUnmanaged' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**

```go
var configDeleteOrder = []string{ConfigKindRole, ConfigKindProject, ConfigKindKit, ConfigKindModelSpec,
	ConfigKindDestination, ConfigKindConnection, ConfigKindUser, ConfigKindJamContext}

func (p *planner) planDeletes() {
	for _, kind := range configDeleteOrder {
		keys := p.deleteKeys(kind) // managed, kind, not in p.repo; kits: children before parents (reverse of a parents-first order over the live kits), others by name
		for _, key := range keys {
			cur, exists := p.live[key]
			if !exists {
				p.w.RemoveMarks = append(p.w.RemoveMarks, key)
				continue
			}
			drift := cur.Digest() != p.marks[key].Digest
			if drift && !p.in.Force {
				p.refuse("%s drifted (edited in Jam since config @%s); re-run with --force or config write it first", key, shortCommit(p.marks[key].Commit))
				continue
			}
			if err := p.deleteObject(key); err != nil {
				p.refuse("cannot delete %s: %s", key, err)
				continue
			}
			p.op(key, ConfigDelete, drift, cur.Canon, nil)
			p.w.RemoveMarks = append(p.w.RemoveMarks, key)
		}
	}
}
```
  `deleteObject` switches on `key.Kind`. Each branch mutates `p.m` and appends to `p.w`:
  - **Role:** any instance with `Phase != PhaseGone`, `p.m.canonicalProject(inst.Project) == string(pid)` and `inst.Role == r` gives `live session <actor> uses it`. Otherwise append `RemoveRoles`, then `applyRemoveRole`.
  - **Project:**
    1. Remove every membership of the project (`RemoveMemberships`, `applyRemoveMember`).
    2. Archive every live channel from `p.m.projectChannels(pid)` (`w.Channels`).
    3. `if _, err := p.m.checkRemoveProject(name); err != nil { return err }`.
    4. Append the tombstone (`Status: StatusRemoved`) to `w.Projects`, then `applyRemoveProject`.
  - **Kit:**
    - `if pr, r, ok := p.m.RoleReferencingKit(name); ok` → `role <pr>/<r> binds it`;
    - `follow, pinned := KitsReferencing(p.store, name)`: non-empty → `kit <first sorted> builds on it`;
    - otherwise `RemoveKits` + `applyRemoveKit`.
  - **Model spec:** `p.m.checkRemoveModelSpec(name)`, then `RemoveModelSpecs` + `applyRemoveModelSpec`.
  - **Destination:** `RemoveDestinations` + `applyRemoveDestination`.
  - **Connection:** `c, _ := p.m.liveConnectionNamed(name); tomb, err := p.m.prepareRemoveConnection(c.ID)`, then append and apply.
  - **User:**
    - a live instance with `OwnerID == u.ID` (or the legacy `Owner == u.Name`) → `they own the live personal session <actor>`;
    - any live project whose escalation (default or by category) has the target `"user:"+id` → `project <p>'s escalation names them`;
    - any remaining `p.m.members[*][u.ID]` → `project <p> lists them as a member`, naming the first project by name;
    - otherwise `tomb, unlinked, err := p.m.prepareRemoveUser(u.ID)`, then append it to `Users`, add `unlinked` to `Accounts`, and apply.
  - **Jam context:** `p.w.JamContext = &sessionctx.Layer{}` + `applySetJamContext`.

  `planUnmanaged` appends every `p.live` key that is neither in `p.repo` nor in `p.marks`, sorted by kind order, then by name.
- [ ] **Step 4: Run** `go test ./internal/jam/... -count=1 && just lint`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): plan config deletes behind the delete guards"`

---

### Task 8: Transactional apply, the admin routes, audit and the round trip

**Files:**
- Create: `internal/jam/configtar/configtar.go`, `internal/jam/configtar/configtar_test.go`, `internal/jam/config_apply.go`, `internal/jam/config_admin.go`, `internal/jam/config_apply_test.go`, `internal/jam/config_admin_test.go`, `internal/jam/config_roundtrip_test.go`
- Modify: `internal/jam/adminclient/adminclient.go` (`doRaw`, the three config methods), `cmd/at-jam/main.go` (mount `jam.WithConfig(st, jam.ConfigEnv{CredExists: credExists, PoolConfigured: cfg.Pool != nil, Serve: portable}, log)`)

**Interfaces:**
- Consumes: `PlanConfig`, `LoadConfigTree`, `ExportConfigTree`, `Store.ApplyConfig`, `kitMu`, `OperatorID`, `writeJSON`, `WriteStatus`.
- Produces:
```go
// package configtar
const (
	MaxUpload   = 16 << 20
	MaxUnpacked = 16 << 20
	MaxEntries  = 2000
)
var ErrTooLarge = errors.New("config tree too large")
func Pack(dir string) ([]byte, error)          // tar.gz; skips .git; refuses symlinks and irregular files; deterministic
func Unpack(r io.Reader, dest string) error    // refuses abs/../links/devices; caps → ErrTooLarge

// package jam
var configMu sync.Mutex // taken before kitMu
var (
	ErrConfigRefused     = errors.New("config plan has refusals")
	ErrConfigPlanChanged = errors.New("the config plan changed since it was reviewed; run config diff again")
)
type ConfigApplyOptions struct {
	Source     gitsource.Source
	Commit     string
	Force      bool
	DryRun     bool
	WantDigest string
	Now        time.Time
}
type ConfigApplyResult struct {
	Plan    ConfigPlan     `json:"plan"`
	Applied bool           `json:"applied"`
	Counts  map[string]int `json:"counts,omitempty"`
}
func ApplyConfigTree(store Store, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error)
// WithConfig mounts POST /admin/config/diff, POST /admin/config/apply
// (?force=1&plan=<digest>) — both take a configtar body — and
// GET /admin/config/export-tree (?all=1).
func WithConfig(store Store, env ConfigEnv, log *slog.Logger) AdminOption

// package adminclient
func (c *Client) ConfigDiff(tree []byte) (jam.ConfigApplyResult, error)
func (c *Client) ConfigApply(tree []byte, force bool, planDigest string) (jam.ConfigApplyResult, error)
func (c *Client) ConfigExportTree(all bool) (jam.ConfigTreeExport, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// configtar_test.go
func TestPackUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "users/bob.yml"), "kind: user\n", 0o644)
	mustWrite(t, filepath.Join(src, "kits/web/ctx/run.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(src, ".git/HEAD"), "ref: x\n", 0o644)
	a, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Pack(src)
	if !bytes.Equal(a, b) {
		t.Fatal("Pack is not deterministic")
	}
	dst := t.TempDir()
	if err := Unpack(bytes.NewReader(a), dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "kits/web/ctx/run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git packed")
	}
}

func TestUnpackRefuses(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"abs":     {{name: "/etc/x", body: "x"}},
		"dotdot":  {{name: "a/../../x", body: "x"}},
		"symlink": {{name: "l", link: "/etc/passwd", typ: tar.TypeSymlink}},
		"device":  {{name: "d", typ: tar.TypeChar}},
		"bomb":    {{name: "big", body: strings.Repeat("0", MaxUnpacked+1)}},
		"entries": manyEntries(MaxEntries + 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Unpack(bytes.NewReader(tarGz(t, entries)), t.TempDir()); err == nil {
				t.Fatal("accepted")
			} else if (name == "bomb" || name == "entries") && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("want ErrTooLarge, got %v", err)
			}
		})
	}
}
```

```go
// config_apply_test.go
func TestApplyConfigTreeRefusesAndWritesNothing(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: missing\n"})
	_, err := ApplyConfigTree(st, tr, allCreds, dirOpts(), nil, "local")
	if !errors.Is(err, ErrConfigRefused) || len(st.ListUsers()) != 0 {
		t.Fatalf("err=%v users=%v", err, st.ListUsers())
	}
}

func TestApplyConfigTreePlanDigestGuard(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	opt := dirOpts()
	opt.WantDigest = "stale"
	if _, err := ApplyConfigTree(st, tr, allCreds, opt, nil, "local"); !errors.Is(err, ErrConfigPlanChanged) {
		t.Fatal(err)
	}
}

func TestApplyConfigTreeAudit(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n", "destinations/gh.yml": ghDest})
	res, err := ApplyConfigTree(st, tr, allCreds, dirOpts(), log, "alice")
	if err != nil || !res.Applied || res.Counts[ConfigCreate] != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"config applied"`, `"operator":"alice"`, `"commit":"dir"`, `"created":2`, `"forced":false`,
		`"msg":"admin user created"`, `"msg":"admin destination added"`, `"via":"config"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit lacks %s:\n%s", want, out)
		}
	}
}

func TestApplyVsConcurrentKitPush(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"kits/web/kit.yml": "kind: studio\n", "kits/app/kit.yml": "kind: studio\nbase: {kit: ../web}\n"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50 {
			_, _, _ = PushStudioKit(st, "web", fmt.Sprintf("kind: studio\nprompt: p%d\n", i))
		}
	}()
	for range 20 {
		_, _ = ApplyConfigTree(st, tr, allCreds, ConfigApplyOptions{Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Force: true, Now: time.Now()}, nil, "local")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock between config apply and kit push")
	}
	k, _ := st.GetKit("app")
	cfg, _ := st.KitConfig("app", k.Current)
	web, _ := st.GetKit("web")
	if _, ok := web.Versions[parentVersion(t, cfg)]; !ok {
		t.Fatalf("app pins a web version that does not exist: %s", cfg)
	}
}
```
  `dirOpts()` returns `ConfigApplyOptions{Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Unix(1000, 0).UTC()}`. `parentVersion` parses `base.kit`'s `@vN`. Run the package with `-race` in Step 4.

```go
// config_roundtrip_test.go — the replication check (spec §8)
func TestConfigRoundTripReplicates(t *testing.T) {
	src, _ := populatedJam(t) // Task 3's fixture
	exp, err := ExportConfigTree(src.ExportConfig(), nil, map[string]any{"display-name": "A"}, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for p, body := range exp.Files {
		mustWrite(t, filepath.Join(dir, p), body, 0o644)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	dst := NewMemStore()
	if _, err := EnsureDefaultModelSpec(dst, false); err != nil { // a fresh serve seeds default
		t.Fatal(err)
	}
	if _, err := ApplyConfigTree(dst, tr, allCreds, dirOpts(), nil, "local"); err != nil {
		t.Fatal(err)
	}
	got, _ := ExportConfigTree(dst.ExportConfig(), nil, map[string]any{"display-name": "A"}, true)
	if !reflect.DeepEqual(got.Files, exp.Files) {
		for p := range exp.Files {
			if got.Files[p] != exp.Files[p] {
				t.Errorf("%s differs:\n%s\n---\n%s", p, exp.Files[p], got.Files[p])
			}
		}
		t.FailNow()
	}
	if len(dst.ListActors()) != 0 {
		t.Fatal("actors replicated")
	}
	again, _ := ApplyConfigTree(dst, tr, allCreds, dirOpts(), nil, "local")
	if again.Plan.Counts()[ConfigUnchanged] != len(again.Plan.Ops) {
		t.Fatalf("second apply is not a no-op: %+v", again.Plan.Ops)
	}
}
```
  `populatedJam`'s model spec `default` must therefore be one of its objects, with an edited harness version. That exercises the seeded-default path.

```go
// config_admin_test.go
func TestConfigRoutes(t *testing.T) {
	st := NewMemStore()
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil, nil,
		WithConfig(st, allCreds, log)))
	defer ts.Close()
	body := packTree(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	var diff ConfigApplyResult
	postTar(t, ts.URL+"/admin/config/diff", body, http.StatusOK, &diff)
	if diff.Applied || len(st.ListUsers()) != 0 || diff.Plan.Ops[0].Action != ConfigCreate {
		t.Fatalf("diff wrote or misplanned: %+v", diff)
	}
	postTar(t, ts.URL+"/admin/config/apply?plan=stale", body, http.StatusConflict, nil)
	var res ConfigApplyResult
	postTar(t, ts.URL+"/admin/config/apply?plan="+diff.Plan.Digest, body, http.StatusOK, &res)
	if !res.Applied || len(st.ListUsers()) != 1 {
		t.Fatalf("%+v", res)
	}
	var exp ConfigTreeExport
	getJSON(t, ts.URL+"/admin/config/export-tree", &exp)
	if _, ok := exp.Files["users/bob.yml"]; !ok {
		t.Fatalf("export-tree %+v", exp)
	}
}

func TestConfigRoutesRejectBadUpload(t *testing.T) {
	st := NewMemStore()
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil, nil, WithConfig(st, allCreds, log)))
	defer ts.Close()
	postTar(t, ts.URL+"/admin/config/diff", []byte("not gzip"), http.StatusBadRequest, nil)
	postTar(t, ts.URL+"/admin/config/diff", bytes.Repeat([]byte{0}, configtar.MaxUpload+1), http.StatusRequestEntityTooLarge, nil)
	postTar(t, ts.URL+"/admin/config/diff", tarWithSymlink(t), http.StatusBadRequest, nil)
}
```
  The test helpers are:
  - `packTree(t, files)`: writes `files` to a temp dir and returns `configtar.Pack`.
  - `postTar(t, url, body, wantStatus, out)`: POSTs `application/gzip`, checks the status, and JSON-decodes into `out` when it is non-nil.
  - `getJSON(t, url, out)`.
  - `tarWithSymlink(t)`: a hand-built tar.gz with one `TypeSymlink` entry.
  - `tarEntry`, `tarGz(t, entries)` and `manyEntries(n)` in `configtar_test.go`: build archives entry by entry.
  - `mustWrite(t, path, body, mode)`: `MkdirAll` + `WriteFile`.
- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/configtar/ -run 'Pack|Unpack|ApplyConfigTree|ApplyVs|RoundTrip|ConfigRoutes' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **`configtar.Pack`:**
    - `filepath.WalkDir` (lexical) over regular files and dirs, skipping `.git`;
    - a symlink or irregular file is `config tree: <rel> is a symlink or not a regular file`;
    - each header gets `Name` = the slash path, `Mode` = `perm & 0o755` with an owner read/write floor of 0o644, `ModTime` zero, and `Uid`/`Gid` 0;
    - gzip with `ModTime` zero.
  - **`configtar.Unpack`:**
    - `gzip.NewReader` → `tar.Reader`;
    - more than `MaxEntries` entries → `ErrTooLarge`;
    - a name that is absolute, has a `..` component or fails `filepath.IsLocal` is refused, as is any type other than a regular file or directory;
    - files are created under `dest` with `os.Root` (`dest` opened as a root) at mode `perm & 0o755 | 0o600`;
    - actual bytes are counted against `MaxUnpacked` → `ErrTooLarge`.
  - **`config_apply.go`:**

```go
func ApplyConfigTree(store Store, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	configMu.Lock()
	defer configMu.Unlock()
	kitMu.Lock() // configMu → kitMu → store mu, as every kit writer takes kitMu before the store
	defer kitMu.Unlock()
	plan := PlanConfig(ConfigPlanInput{Tree: tree, Snapshot: store.ExportConfig(), Instances: store.ListInstances(),
		Marks: store.ListConfigMarks(), Env: env, Source: opt.Source, Commit: opt.Commit, Force: opt.Force, Now: opt.Now})
	res := ConfigApplyResult{Plan: plan}
	if opt.DryRun {
		return res, nil
	}
	if len(plan.Refusals) > 0 {
		return res, fmt.Errorf("%w:\n%s", ErrConfigRefused, strings.Join(plan.Refusals, "\n"))
	}
	if opt.WantDigest != "" && opt.WantDigest != plan.Digest {
		return res, ErrConfigPlanChanged
	}
	if err := store.ApplyConfig(plan.write); err != nil {
		return res, fmt.Errorf("config apply: write: %w", err)
	}
	res.Applied, res.Counts = true, plan.Counts()
	logConfigApply(log, actor, opt, plan)
	return res, nil
}
```
  - **`logConfigApply`** (log may be nil):
    - one `config applied` entry with `operator`, `source` (`dir` for `ConfigDirRepo`, else the repo), `commit`, `created`, `updated`, `deleted`, `adopted`, `unchanged` and `forced`;
    - then, for each create, update or delete op, the usual message from a table with `operator`, `via=config`, `kind` and `name`.
    - The table:
      - destination: `admin destination added` (create, update) / `admin destination removed` (delete);
      - model spec: `admin model-spec created` / `updated` / `deleted`;
      - user: `admin user created` / `admin user updated` / `admin user removed`;
      - connection: `admin connection created` / `admin connection cred set` / `admin connection removed`;
      - kit: `admin kit pushed` (create, update) / `admin kit removed` (delete);
      - project: `admin project created` / `admin project updated` / `admin project removed`;
      - role: `admin role put` (create, update) / `admin role removed` (delete);
      - jam context: `admin jam context set` for every action.
    - No values are logged, only names.
  - **`config_admin.go` (`WithConfig`):**
    - A `tree` helper first reads the whole body with `io.ReadAll(http.MaxBytesReader(w, r.Body, configtar.MaxUpload))`, so an oversize body is a 413 before gzip ever sees it. It then runs `dir, _ := os.MkdirTemp("", "jam-config-")`, `defer os.RemoveAll(dir)` and `configtar.Unpack(bytes.NewReader(body), dir)`.
      - A `*http.MaxBytesError` or `configtar.ErrTooLarge` answers 413 `config tree too large (limit 16 MiB)`.
      - Any other unpack error answers 400 with its text.
      - Then `LoadConfigTree(dir)`.
    - The routes:
      - **diff:** `ApplyConfigTree(..., DryRun: true)` answers 200 with JSON.
      - **apply:** with `Force: r.URL.Query().Get("force") == "1"` and `WantDigest: r.URL.Query().Get("plan")`. `ErrConfigRefused`/`ErrConfigPlanChanged` answer 409 with the error text; any other error is logged and answers 500 `config store error`.
      - **export-tree:** `ExportConfigTree(store.ExportConfig(), store.ListConfigMarks(), env.Serve, all)`.
    - Each call passes `OperatorID(r)` as the actor, and a request whose `Content-Type` is `application/json` answers 400 `config sources (--git) are not supported yet; use --dir`.
  - **`adminclient`:**
    - `doRaw(method, path, contentType string, body []byte, out any) error` reuses `doWith`'s status mapping. Factor the response handling into `finish(resp, method, path, out)`.
    - `ConfigDiff` and `ConfigApply` post `application/gzip` with the git timeout; `ConfigApply` builds `?force=1&plan=<digest>`.
    - `ConfigExportTree` is a GET with `?all=1`.
  - **`cmd/at-jam`:** mount `jam.WithConfig` beside `jam.WithKitFetcher`.
- [ ] **Step 4: Run** `go test -race ./internal/jam/... -count=1 && go test ./cmd/at-jam/ -count=1 && just lint`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): transactional config apply, admin routes and the replication test"`

---

### Task 9: The `at-jam config` CLI and docs

**Files:**
- Create: `cmd/at-jam/configgit.go`, `cmd/at-jam/configgit_test.go`, `docs/usage/jam/config-git.md`
- Modify: `cmd/at-jam/main.go` (register `config`), `docs/usage/jam/INDEX.md` (a new row after `backup.md`; bump `updated`), `docs/usage/jam/backup.md` (one line: for a reviewable, mergeable source, see config-git.md), `docs/usage/jam/serve.md` (link to config-git.md from the layering section, if Task 5 couldn't)

**Interfaces:**
- Consumes: `adminclient.Client.ConfigDiff`/`ConfigApply`/`ConfigExportTree`, `configtar.Pack`, `clientFlags`, `jam.ConfigPlan`.
- Produces:
```go
func cmdConfig(args []string, _ cli.Globals, stdout, stderr io.Writer) int // write | diff | apply
var configStdin io.Reader = os.Stdin                                      // the apply prompt (tests swap it)
func insideGitWorkTree(dir string) bool                                   // some ancestor holds .git
func writeConfigTree(dir string, exp jam.ConfigTreeExport) error         // write files, remove exp.Remove
func printConfigPlan(w io.Writer, p jam.ConfigPlan)
```

- [ ] **Step 1: Write the failing tests.** `configServer(t)` is an `httptest` admin handler over a MemStore with `jam.WithConfig`. `runJam` is the helper in `modelspec_test.go`.

```go
func TestConfigWriteThenApplyReplicates(t *testing.T) {
	srcURL, src := configServer(t)
	seedJam(t, src) // a user, a connection, a destination, kits web-dev + app (path parent), a project with a member and a room, a role
	repo := t.TempDir()
	mustMkdir(t, filepath.Join(repo, ".git"))
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	first := readTree(t, repo)
	if code, _, _ := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 || !reflect.DeepEqual(readTree(t, repo), first) {
		t.Fatal("second write changed the tree")
	}
	dstURL, dst := configServer(t)
	code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", dstURL)
	if code != 0 || !strings.Contains(out, "create  user bob") || !strings.Contains(out, "create  kit app") {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	if len(dst.ListUsers()) != 0 {
		t.Fatal("diff wrote")
	}
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", dstURL); code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	back := t.TempDir()
	mustMkdir(t, filepath.Join(back, ".git"))
	runJam("config", "write", back, "--all", "--admin-url", dstURL)
	if !reflect.DeepEqual(readTree(t, back), first) {
		t.Fatal("the replica's tree differs from the source's")
	}
}

func TestConfigWriteRefusesOutsideGit(t *testing.T) {
	url, _ := configServer(t)
	dir := t.TempDir()
	code, _, errs := runJam("config", "write", dir, "--admin-url", url)
	if code != 1 || !strings.Contains(errs, "is not inside a git working tree (use --force to write anyway)") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, _ := runJam("config", "write", dir, "--force", "--admin-url", url); code != 0 {
		t.Fatal("--force")
	}
}

func TestConfigWriteRemovesOnlyManagedGone(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	mustWrite(t, filepath.Join(repo, "README.md"), "mine\n", 0o644)
	runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	bob := findUser(t, st, "bob")
	if err := st.RemoveUser(bob.ID); err != nil {
		t.Fatal(err)
	}
	runJam("config", "write", repo, "--admin-url", url)
	if _, err := os.Stat(filepath.Join(repo, "users/bob.yml")); !os.IsNotExist(err) {
		t.Fatal("managed file of a removed user kept")
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal("unrelated file removed")
	}
}

func TestConfigApplyPromptsAndRefuses(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	configStdin = strings.NewReader("n\n")
	t.Cleanup(func() { configStdin = os.Stdin })
	if code, out, _ := runJam("config", "apply", "--dir", repo, "--admin-url", url); code != 1 || !strings.Contains(out, "Apply 1 change? [y/N]") || len(st.ListUsers()) != 0 {
		t.Fatalf("declined apply: %d %s", code, out)
	}
	mustWrite(t, filepath.Join(repo, "projects/app/roles/ic.yml"), "kind: role\nversion: 1\nkit: nope\n", 0o644)
	code, out, _ := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 1 || !strings.Contains(out, "refused:\n  projects/app/roles/ic.yml: ") {
		t.Fatalf("refusals: %d\n%s", code, out)
	}
}

func TestConfigNeedsDir(t *testing.T) {
	if code, _, errs := runJam("config", "diff"); code != 2 || !strings.Contains(errs, "--dir is required (git sources are not supported yet)") {
		t.Fatalf("%d %s", code, errs)
	}
}
```
  The test helpers are:
  - `gitDir(t)`: a temp dir with a `.git` subdirectory.
  - `readTree(t, dir)`: `map[path]content`, excluding `.git`.
  - `findUser(t, st, name)`: searches `st.ListUsers()`.
  - `mustMkdir` and `mustWrite`.
  - `seedJam`: populates the store like Task 3's `populatedJam`, through the exported store API.
- [ ] **Step 2: Run** `go test ./cmd/at-jam/ -run TestConfig -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement `configgit.go`.**
  - **Command:** register `{Name: "config", Brief: "keep the Jam's configuration in a git working tree: write it out (write <dir> [--all]), or plan and apply a checkout (diff|apply --dir <path> [--yes] [--force]) via the admin API", Run: cmdConfig}`.
  - **`write <dir>`:**
    - `--all` and `--force`;
    - `!insideGitWorkTree(abs) && !force` exits 1 with `at-jam config write: <dir> is not inside a git working tree (use --force to write anyway)`;
    - otherwise `exp := c.ConfigExportTree(all)` and `writeConfigTree(dir, exp)`. Every path must pass `filepath.IsLocal`. Files are written with `os.MkdirAll` + `os.WriteFile(…, 0o644)`; `Remove` paths are `os.Remove`d, ignoring not-exist.
    - Print `wrote N files to <dir> (removed M); review with git diff`.
  - **`diff --dir <path>`:** `data := configtar.Pack(path)`, `res := c.ConfigDiff(data)`, `printConfigPlan(stdout, res.Plan)`. Exit 1 when there are refusals.
  - **`apply --dir <path> [--yes] [--force]`:**
    1. Run diff and print the plan.
    2. With refusals, exit 1.
    3. With no changes (`Counts` create+update+delete+adopt == 0), print `nothing to apply` and still apply when marks would change (`len(Ops) > 0`) so commits refresh.
    4. Unless `--yes`, prompt `Apply N change(s)? [y/N] ` (singular `change` when N=1, matching the test) on stdout and read a line from `configStdin`. Anything but `y`/`yes` exits 1 with `aborted`.
    5. `c.ConfigApply(data, force, res.Plan.Digest)`, then print `applied: N created, N updated, N deleted, N adopted`.
  - **`printConfigPlan`:**
    - one line per op except `unchanged`: `fmt.Fprintf(w, "%s  %s %s%s\n", action, kind, name, driftMark)` (two spaces after the action, so `create  user bob`), where the drift mark is `  (drift)`;
    - for drift ops, the `Before` and `After` blocks indented by four spaces under `jam:` and `repo:`;
    - then `N unchanged`;
    - `unmanaged: kind name, …`;
    - `serve-config: same | differs (restart to apply): k1, k2 | absent | unknown`;
    - `refused:` with each refusal indented two spaces.
  - **`--git`** is not defined. Neither verb accepts it in slice 1, and a missing `--dir` exits 2 with `at-jam config <verb>: --dir is required (git sources are not supported yet)`.
- [ ] **Step 4: Write the docs.**
  - **`docs/usage/jam/config-git.md`** is a leaf. Use docs-author, and keep it within the leaf budget in `/agent-data/reference/progressive-disclosure.md`. Its frontmatter:
    - `summary`: keeping a Jam's configuration in a git repo — the file layout, `config write|diff|apply --dir`, managed objects, drift and refusals, and replicating a Jam from a checkout;
    - `read_when`: you want a Jam's config reviewable in git, are standing up a replica from a checkout, or a config diff/apply refused, flagged drift or listed something unmanaged;
    - `owns`: the config repo layout and file kinds, the `at-jam config write|diff|apply` verbs, the plan actions and refusals, managed marks, the `/admin/config/diff|apply|export-tree` routes;
    - `prereqs`: serve.md (layered serve configs, host keys), backup.md (how this differs from export/import);
    - `tier: leaf`, `updated: 2026-10-21`.
  - **The doc's sections:**
    - **Layout:** the tree, plus one example file per kind, with the `harness-version` and `service` key names and `context.md` front matter.
    - **The verbs** and the operator loop (write → `git diff` → commit; edit → diff → apply).
    - **The plan:** a table of actions (create, update, drift, adopt, delete, unchanged, unmanaged, refused) with the exact refusal texts, the order, the delete guards and pins.
    - **Kits:** inline only in this slice, path parents across `kits/`, provenance `local`.
    - **Replicating a Jam:** serve with `--config jam.yml --config jam.local.yml`, credentials, `config apply --dir`. People sign in again, and `default` is adopted.
    - **Limits:** 16 MiB, 2000 entries. Marks are not in `at-jam export`.
    - **Not yet:** `--git`, `track`/`sync`, `.source.yml`, the UI page and drift attention.
  - **The INDEX row** links `config-git.md` with the read-when text: `You want a Jam's config (kits, destinations, model-specs, users, connections, projects, roles, context, serve settings) in a git repo — writing it out, diffing/applying a checkout, replicating a Jam — or a config apply refused, flagged drift or listed unmanaged objects.`
- [ ] **Step 5: Run** `just test && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`. Expected: PASS, and the audit's error count is at most 708 (the new leaf adds none).
- [ ] **Step 6: Commit** `git commit -m "feat(jam): at-jam config write|diff|apply; config-in-git docs"`

---

### Task 10: Manual acceptance (operator)

- [ ] On a dev Jam (`just dev`), run `at-jam config write ~/jam-config --all` in a fresh `git init` repo, commit it, and confirm `git status` is clean after a second `config write`.
- [ ] Start a second dev Jam on another Postgres database with `--config jam.yml --config jam.local.yml`, where `jam.yml` is the written one. Run `at-jam config apply --dir ~/jam-config` and answer `y`. The Users, Projects, Kits and Model-specs pages should match the first Jam's.
- [ ] Edit a role's egress in the first Jam's UI. `config diff --dir ~/jam-config` should show the drift with both sides, `apply` should refuse it, and `apply --force` should take the repo's version.
- [ ] Put `listen:` in the repo's `jam.yml`. `config diff` should refuse it, and so should serve when it is started with two `--config` files.
