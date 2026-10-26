# Jam only — slice 2 "Delete" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete `cmd/at-cove`, `cmd/at-switchboard`, `cmd/at-task` and every package, payload, release slot, kit, test and doc that only they reach, leaving Jam (`at-jam`, `at-mint`, `cove-master`, the dev tools) building, tested, released and documented accurately.

**Architecture:** Eight deletion tasks ordered so `go build ./... && go vet ./... && go vet -tags integration ./...`, `just test` and `just lint` pass after each: first the two in-image binaries (Discord client moved to `internal/discord` on the way), then `cmd/at-cove` with its release slot (an `at-cove` tombstone keeps an older `at-jam update` working), then the packages only at-cove imported, then the tracker and shared-package dead code the compiler and `deadcode` prove unreachable, then docs, then a permanent guard test. Nothing is renamed (slice 3) and OVERVIEW/AGENTS/in-image agent docs get only the edits that remove now-false statements (slice 4 rewrites them).

**Tech Stack:** Go 1.26 module `github.com/aethons-tools/cove`, `just`, bash (`install.sh`, `scripts/*.sh`), goreleaser v2, GitHub Actions, Markdown docs checked by `docs_audit.py`.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-only-design.md`](../specs/2026-10-26-jam-only-design.md) — §2 "Delete (slice 2)" and Decisions 1, 3, 4, 7.

## Global Constraints

- **Delete outright.** No deprecation window, no aliases for the deleted commands (Decision 1). The one compatibility artefact is the release-archive `at-cove` tombstone (Task 3), which exists only so an *older* `at-jam update` can still install the new release; slice 3 drops it.
- **No renames in this slice.** Binary names (`at-jam`, `at-mint`, `cove-master`), image names, labels (`harbor.cove`), tag prefixes, env vars (`AT_JAM_*`, `AT_COVE_*`, `COVE_*`), in-image paths, `~/.config/at-cove` launcher defaults, the release archive name `cove_<ver>_<os>_<arch>`, the goreleaser/GitHub job id `atcove`, Go identifiers `AtCoveIdentity`/`LaunchCoveMaster`, and the Go module path stay as they are (spec §3 owns them).
- **The hardening layer, the sealed egress base and the egress ceiling are not weakened** (spec §2): only the binaries staged into the image change. Every existing test under `internal/assemble/` keeps passing unmodified except the ones this plan names.
- **The assembly identity changes**, so every studio image tag changes once and each kit rebuilds lazily on its next raise (spec §2). That is expected; document it (Task 7), do not avoid it.
- **One fact, one place.** Delete the deleted things' docs; fix every link and INDEX row to them; move a still-true fact to its new owner instead of copying it. OVERVIEW.md, AGENTS.md and the in-image agent docs (`images/cove-base-image/image-files/home/agent/.init-agent-data/`) get minimal edits only — slice 4 rewrites them.
- **Docs audit:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` must report no `ERROR` outside `superpowers/` that is not in the baseline captured in Task 7 Step 1 (four OVERVIEW.md anchor errors today). Superpowers orphans (including this plan's own) are pre-existing and out of scope.
- **Toolchain:** `export GOPROXY=https://proxy.golang.org,direct` before any `go` command in this sandbox. Run `gofmt -w` on every Go file you touch. Postgres-backed tests run in CI only (they skip locally).
- **Gates per task:** `go build ./... && go vet ./... && go vet -tags integration ./...`, `just test` (exit 0), `just lint` (exit 0), plus the task's own checks.
- **Commits:** one commit per task (more is fine), message body ending with a blank line then `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Work on branch `feat/jam-only-delete`; do not push until the user asks.
- **Notation in this plan:** inside an inline "replace X with Y" pair, `\n` stands for a line break and `\`` for a literal backtick; match the file's real indentation. Code blocks are literal.

### Tooling used by several tasks

One throwaway helper (not committed — it lives in `/tmp`). Create it once before Task 1:

```bash
cat > /tmp/rmfunc.py <<'EOF'
#!/usr/bin/env python3
"""rmfunc.py FILE NAME... — delete top-level Go funcs and their doc comments.
NAME is Func or Recv.Method (receiver type without *). Exits non-zero, writing
nothing, if any NAME is not found."""
import re, sys
path, names = sys.argv[1], set(sys.argv[2:])
lines = open(path).read().split('\n')
out, i, removed = [], 0, set()
pat = re.compile(r'^func (?:\((?:\w+ )?\*?(\w+)\) )?(\w+)[\[(]')
while i < len(lines):
    m = pat.match(lines[i])
    if m:
        name = (m.group(1) + '.' if m.group(1) else '') + m.group(2)
        if name in names:
            while out and out[-1].startswith('//'):
                out.pop()
            if lines[i].rstrip().endswith('}'):
                i += 1
            else:
                while lines[i] != '}':
                    i += 1
                i += 1
            if out and out[-1] == '' and i < len(lines) and lines[i] == '':
                i += 1
            removed.add(name)
            continue
    out.append(lines[i]); i += 1
missing = names - removed
if missing:
    sys.exit(f"{path}: not found: {sorted(missing)}")
open(path, 'w').write('\n'.join(out))
EOF
chmod +x /tmp/rmfunc.py
```

`goimports` (to drop imports a deletion leaves unused): `go run golang.org/x/tools/cmd/goimports@latest -w <files>`.

## Review Focus

1. **A fresh Jam host with no launcher SSH key.** `at-cove` was the only code that generated `~/.config/at-cove/id_ed25519` (`keys.Ensure`); Jam's serve only *reads* `<identity-file>.pub` and exits on a missing file. A person standing up Jam on a new machine after this slice expects `serve` with a `runtime.launcher` to work, not to fail on a key nothing creates any more. Pinned by Task 3 (`internal/keys` kept, `EnsureFile` wired into serve, `TestEnsureFileGeneratesWhenMissing`).
2. **An operator runs their existing (pre-slice) `at-jam update` against the first release cut after this merges.** That binary embeds the old `install.sh`, which `place`s `at-cove` unconditionally under `set -e`; an archive without `at-cove` would abort the update. Expected: the update succeeds and installs the new `at-jam` and `at-mint`. Pinned by Task 3 (tombstone in the archive; `install.test.sh` drives a frozen copy of the old installer and also proves it fails without the tombstone).
3. **Jam's launcher builds a studio image after the payload change.** Expected: the assembled context still stages cove-master, every sealed hardening step is still in the Dockerfile in order, the egress lists and ceiling are still baked — only the at-task/at-switchboard stanzas are gone. Pinned by Tasks 1–2 (`TestAssembleContextStagesOnlyCoveMaster`, plus the untouched hardening/egress tests run by name).
4. **A serve config with `runtime.requisitioner.linear:` or `github:` after `kit.Config` and its parser are deleted.** Jam reads `kit.LinearTracker`/`kit.GitHubTracker`/`kit.StateMap` straight from YAML; deleting their siblings must not change their fields or tags, and the GitHub tracker must keep `owner/repo#N` keys and the dispatch gate now that the `#N` mode is gone. Pinned by Tasks 5–6 (converted githubissues tests, `TestRuntimeRequisitionerParsed`, `TestParseRequisitionerGitHubBlock`, `TestOpenTrackerPicksTheBlock`, `TestRelayEndToEndBothTrackers`).
5. **The release workflow on the PR and on `main`.** Expected: `goreleaser check` passes, the PR build compiles `at-mint`/`at-jam` with cove-master staged, and the archive holds exactly `at-cove` (tombstone, mode 0755), `at-mint`, `at-jam`. Pinned by Task 3 (local `goreleaser check` + snapshot archive listing) and Task 8 (the PR's `atcove` job).

---

## File Structure

| Area | Created | Deleted | Modified |
|---|---|---|---|
| Discord | `internal/discord/{discord.go,message.go,discord_test.go,discord_integration_test.go}` (moved) | `cmd/at-switchboard/`, `internal/switchboard/`, `internal/atswitchboard/` | `cmd/at-jam/{main.go,relay_discord.go,relay_discord_test.go}` |
| Payload | `scripts/stage-covemaster.sh` (renamed) | `cmd/at-task/`, `internal/attask/`, `scripts/stage-attask.sh` | `internal/assemble/{assemble.go,assemble_test.go,hardening/Dockerfile}`, `internal/install/{currency.go,currency_test.go}`, `internal/covemasterbin/{covemasterbin.go,bin/README}`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `scripts/build.sh` |
| at-cove + release | `scripts/release/at-cove`, `testdata/install-legacy.sh` | `cmd/at-cove/` | `.goreleaser.yaml`, `.github/workflows/release.yml`, `install.sh`, `install.test.sh`, `scripts/{build.sh,lint.sh}`, `justfile`, `.gitignore`, `internal/keys/*`, `cmd/at-jam/{main.go,update.go,update_test.go}`, `installer.go`, `internal/update/update.go` |
| Orphans | — | `internal/{dispatchrun,state,sshconfig,dockere2e}/`, `internal/dispatch/{exec,github,gitlab,worker}/`, `internal/dispatch/doc.go`, `internal/dispatch/scheduler/{engine.go,engine_test.go,fakes_test.go}`, `kits/`, `.at-cove/`, `internal/kit/refkit_test.go`, `internal/usersecret/runbook_example_test.go` | `cmd/adopt-base/main.go`, `internal/adoptbase/*`, `.air.toml`, `justfile` |
| Trackers | — | — | `internal/dispatch/{githubissues,linear,scheduler}/*`, `internal/tracker/trackertest/linear.go`, `cmd/at-jam/tracker.go` |
| Shared prune | `internal/connect/vmwrite.go`, `internal/sshargs/integration_test.go`, `internal/install/identity.go` (renamed from `currency.go`) | `internal/awake/`, `internal/connect/{connect,teammate,specargs,transport,integration}*.go`, `internal/install/install*.go`, `internal/kit/{discover,gitignore,imagesource,codec}.go` + tests, `internal/backend/backend_test.go`, `internal/usersecret/{usersecret_test,plan_test}.go` | `internal/{kit,backend,backend/colima,assemble,naming,usersecret,sshargs,connect,install}/*`, `internal/jam/launcher/{launcher.go,launcher_test.go}` |
| Docs | — | `docs/usage/{at-cove-config,at-cove-secrets,discord-teammate,workspace-visibility,at-task,at-task-inputs,at-task-output,observability}.md`, `docs/orchestration/` | `docs/{INDEX,OVERVIEW,DEVELOPMENT}.md`, `docs/usage/{INDEX,at-mint,docker-in-sandbox}.md`, `docs/usage/jam/*.md` (listed in Task 7), `AGENTS.md`, `README.md`, `dev/jam.dev.yml.example` |
| Guard | `deleted_test.go` | — | comment-only stragglers (Task 8) |

---
### Task 1: Move the Discord REST client to `internal/discord`; delete at-switchboard

Jam's Discord relay uses only `switchboard.RESTClient`, `NewRESTClient` and `Message` (verified: `grep -rn "switchboard\." cmd/at-jam`). Everything else in `internal/switchboard` is the in-sandbox conductor, which dies with `cmd/at-switchboard`; `internal/atswitchboard` only embeds its binary into the payload.

**Files:**
- Create (move): `internal/discord/discord.go`, `internal/discord/discord_test.go`, `internal/discord/discord_integration_test.go` (from `internal/switchboard/`), `internal/discord/message.go` (new)
- Delete: `cmd/at-switchboard/`, `internal/switchboard/`, `internal/atswitchboard/`
- Modify: `cmd/at-jam/main.go`, `cmd/at-jam/relay_discord.go`, `cmd/at-jam/relay_discord_test.go`, `internal/assemble/assemble.go`, `internal/assemble/assemble_test.go`, `internal/assemble/hardening/Dockerfile`, `internal/install/currency.go`, `internal/install/currency_test.go`, `scripts/stage-attask.sh`, `scripts/build.sh`, `.gitignore`, `justfile`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: package `github.com/aethons-tools/cove/internal/discord` with `type RESTClient`, `func NewRESTClient(token string, channels []string, opts ...RESTOption) *RESTClient`, `type RESTOption`, `WithBaseURL`, `WithHTTPClient`, `WithSleep`, methods `Poll(ctx, cursors map[string]string) ([]Message, map[string]string, error)`, `Seed(ctx) (map[string]string, error)`, `Post(ctx, channel, content string) error`, `PostID(ctx, channel, content string) (string, error)`, and `type Message struct{ID, Channel, Author, Content, AuthorID string; AuthorBot bool; ReferencedID string}` — identical to the `switchboard` ones. `TestAssembleContextStagesOnlyCoveMaster` in `internal/assemble/assemble_test.go` (Task 2 extends its lists).

- [ ] **Step 1: Move the client's tests first (they must fail)**

```bash
cd /home/agent/workspace/cove
export GOPROXY=https://proxy.golang.org,direct
mkdir -p internal/discord
git mv internal/switchboard/discord_test.go internal/discord/discord_test.go
git mv internal/switchboard/discord_integration_test.go internal/discord/discord_integration_test.go
sed -i 's/^package switchboard$/package discord/' internal/discord/*_test.go
go test ./internal/discord/
```

Expected: FAIL to compile — `undefined: NewRESTClient` (and friends): the implementation has not moved yet.

- [ ] **Step 2: Move the implementation and give `Message` its own file**

```bash
git mv internal/switchboard/discord.go internal/discord/discord.go
sed -i 's/^package switchboard$/package discord/' internal/discord/discord.go
cat > internal/discord/message.go <<'EOF'
// Package discord is Jam's Discord REST client: it polls, seeds and posts to a
// fixed set of channels for the Discord relay (cmd/at-jam relay_discord.go).
package discord

// Message is one inbound Discord message. ID is the Discord snowflake, used as
// the per-channel poll cursor.
type Message struct {
	ID      string
	Channel string
	Author  string // display name (global_name, else username) — user-editable, never an identity
	Content string
	// AuthorID is Discord's immutable author user id (snowflake); "" if absent.
	AuthorID string
	// AuthorBot is true when the author is a bot (Discord's author.bot).
	AuthorBot bool
	// ReferencedID is the id of the message this replies to; empty if not a reply.
	ReferencedID string
}
EOF
```

In `internal/discord/discord_integration_test.go`, rename the live-test env vars and the run line (they named the deleted package):

```go
//	Run: DISCORD_IT_TOKEN=… DISCORD_IT_CHANNEL=… \
//	       go test -tags integration ./internal/discord/ -run TestDiscordRoundTripLive -v
func TestDiscordRoundTripLive(t *testing.T) {
	token := os.Getenv("DISCORD_IT_TOKEN")
	channel := os.Getenv("DISCORD_IT_CHANNEL")
	if token == "" || channel == "" {
		t.Skip("set DISCORD_IT_TOKEN and DISCORD_IT_CHANNEL to run the live Discord round-trip")
	}
```

and in the same file change the marker text `"at-switchboard integration round-trip %d"` to `"jam discord integration round-trip %d"`.

- [ ] **Step 3: Point Jam at the new package**

```bash
sed -i 's#"github.com/aethons-tools/cove/internal/switchboard"#"github.com/aethons-tools/cove/internal/discord"#; s/\bswitchboard\./discord./g; s/\*switchboard\.RESTClient/*discord.RESTClient/g' \
  cmd/at-jam/main.go cmd/at-jam/relay_discord.go cmd/at-jam/relay_discord_test.go
gofmt -w cmd/at-jam/main.go cmd/at-jam/relay_discord.go cmd/at-jam/relay_discord_test.go internal/discord
grep -n "switchboard" cmd/at-jam/*.go
```

Expected: the grep prints nothing. (`gofmt -w` re-sorts the moved import.)

- [ ] **Step 4: Run the moved and the relay tests**

```bash
go test ./internal/discord/ ./cmd/at-jam/ -run 'RESTClient|Discord|Relay' -v 2>&1 | tail -20
go vet -tags integration ./internal/discord/
```

Expected: PASS — every `TestRESTClient_*` and every relay test; the vet is clean. (Review Focus: the relay is unchanged — same client, same `Message` fields.)

- [ ] **Step 5: Write the payload guard test (it must fail)**

Append to `internal/assemble/assemble_test.go`:

```go
// The build context carries no at-switchboard or at-task: those binaries were
// deleted with at-cove (jam-only slice 2), so neither their staging dirs nor any
// Dockerfile stanza installing them may survive. cove-master is still staged,
// and every sealed hardening step is still there, in order — only the two
// stanzas are gone (the hardening layer is not weakened).
func TestAssembleContextStagesOnlyCoveMaster(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := AssembleContext(buildDir, []byte("k\n"), Egress{Policy: []string{"pkg.go.dev"}}, "", nil, harnessinstall.Default()); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"switchboard"} {
		if _, err := os.Stat(filepath.Join(buildDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("build context still has %s/ (err=%v)", gone, err)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := os.Stat(filepath.Join(buildDir, "covemaster", "cove-master-linux-"+arch)); err != nil {
			t.Fatalf("cove-master %s not staged: %v", arch, err)
		}
	}
	df := read(t, filepath.Join(buildDir, "Dockerfile"))
	for _, gone := range []string{"at-switchboard", "/tmp/switchboard"} {
		if strings.Contains(df, gone) {
			t.Fatalf("assembled Dockerfile still mentions %q", gone)
		}
	}
	at := 0
	for _, sealed := range []string{
		"FROM harness",
		"PermitRootLogin no",
		"COPY covemaster/ /tmp/covemaster/",
		"COPY image-files/. /.",
		"systemctl enable cove-egress.service squid.service",
		"/usr/local/lib/cove/apply-sshenv.sh",
		"RUN rm -rf /.cove",
		`ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]`,
	} {
		i := strings.Index(df[at:], sealed)
		if i < 0 {
			t.Fatalf("sealed step %q missing or out of order in the assembled Dockerfile", sealed)
		}
		at += i + len(sealed)
	}
	for _, f := range []string{"allowed_domains.txt", "allowed_domains.kit.txt", "allowed_domains.infra.txt", "allowed_domains.session.txt", "egress_ceiling.txt", "squid.conf"} {
		if _, err := os.Stat(filepath.Join(buildDir, "image-files/etc/squid", f)); err != nil {
			t.Fatalf("sealed/baked egress file %s missing: %v", f, err)
		}
	}
}
```

Run: `go test ./internal/assemble/ -run TestAssembleContextStagesOnlyCoveMaster -v`
Expected: FAIL — `build context still has switchboard/`.

- [ ] **Step 6: Remove at-switchboard from the payload**

In `internal/assemble/assemble.go`:
- delete the import line `"github.com/aethons-tools/cove/internal/atswitchboard"`;
- in `AssembleContext` delete

```go
	if err := writeSwitchboard(buildDir); err != nil {
		return err
	}

```

- run `/tmp/rmfunc.py internal/assemble/assemble.go writeSwitchboard`;
- in the `AssembleContext` doc comment change `at-task/at-switchboard/cove-master binaries` to `at-task/cove-master binaries`;
- in the `writeCoveMaster` doc comment change `mirrors writeSwitchboard (COV-158)` to `mirrors writeAtTask (COV-158)`.

In `internal/assemble/hardening/Dockerfile` delete the whole stanza from `# Install the embedded, version-locked at-switchboard the orchestrating at-cove` through `&& rm -rf /tmp/switchboard` (and the blank line after it), and change the next stanza's first two comment lines to:

```dockerfile
# Install the embedded, version-locked cove-master the launcher staged into
# the build context (COV-158). Install
```

In `internal/assemble/assemble_test.go`: `/tmp/rmfunc.py internal/assemble/assemble_test.go TestWriteSwitchboard_WritesArchFiles`.

In `internal/install/currency.go` delete the import `"github.com/aethons-tools/cove/internal/atswitchboard"` and the block

```go
	sb, err := HashTree(atswitchboard.BinFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("switchboard"))
	writeField(h, []byte(sb))

```

and change `the embedded at-task,\n// at-switchboard, and cove-master binaries` in the `AtCoveIdentity` comment to `the embedded at-task\n// and cove-master binaries`. In `internal/install/currency_test.go`: `/tmp/rmfunc.py internal/install/currency_test.go TestAtCoveIdentityIncludesSwitchboard`, and in `TestAtCoveIdentityIncludesCoveMaster`'s comment drop `— mirrors TestAtCoveIdentityIncludesSwitchboard above`.

- [ ] **Step 7: Delete the binary, its packages and its build plumbing**

```bash
git rm -rq cmd/at-switchboard internal/switchboard internal/atswitchboard
```

In `scripts/stage-attask.sh` delete the `switchboard_dir="internal/atswitchboard/bin"` block (its `mkdir -p` and the `for a in amd64 arm64` loop building `./cmd/at-switchboard`) and replace the header comment's first eight lines with:

```bash
# stage-attask.sh — build the linux at-task and cove-master binaries at-cove
# embeds, into internal/attask/bin/ and internal/covemasterbin/bin/, BEFORE
# at-cove is built so their `//go:embed bin` picks them up. Both arches always
# (at-cove may build a sandbox for either VM arch). Shared by scripts/build.sh
# and the goreleaser before-hook so the staging logic has a single home. See
# internal/attask + COV-36, internal/covemasterbin + COV-158.
```

In `scripts/build.sh`: `BINARIES=(at-cove at-task at-mint at-switchboard at-jam)` → `BINARIES=(at-cove at-task at-mint at-jam)`; `# Stage the linux at-task and at-switchboard binaries at-cove embeds, before` → `# Stage the linux at-task and cove-master binaries the host binaries embed, before`; `echo "Staging embedded at-task + at-switchboard (linux amd64+arm64)"` → `echo "Staging embedded at-task + cove-master (linux amd64+arm64)"`.

In `.gitignore` delete the lines `/.switchboard/` and `/at-switchboard`.

In `justfile` replace the `integration-switchboard` recipe and its 4-line comment with:

```just
# Discord live check: a real Discord REST round-trip through internal/discord
# (DISCORD_IT_TOKEN + DISCORD_IT_CHANNEL); skips if they are unset.
integration-discord:
    go test -tags integration ./internal/discord/ -run TestDiscordRoundTripLive -v -timeout 5m
```

- [ ] **Step 8: Verify**

```bash
go run golang.org/x/tools/cmd/goimports@latest -w internal/assemble/assemble.go internal/install/currency.go
gofmt -w internal/assemble internal/install
go build ./... && go vet ./... && go vet -tags integration ./...
go test ./internal/assemble/ -run 'TestAssembleContextStagesOnlyCoveMaster|Embed|Nftables|Squid|Egress|Hardening|Ceiling' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go list ./... | grep -E 'switchboard' ; echo "list-exit=$?"
just test && just lint
```

Expected: build/vet clean; the named assemble tests all PASS; the `go list | grep` prints nothing (`list-exit=1`); `just test` and `just lint` exit 0.

- [ ] **Step 9: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "refactor: move the Discord REST client to internal/discord; delete at-switchboard

Jam's relay only ever used switchboard's REST client. The in-sandbox
conductor, its embed package and its hardening-layer stanza go with
cmd/at-switchboard (jam-only slice 2).

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Delete at-task and its payload; stage only cove-master

Only at-cove's `dispatchrun` ever ran `at-task` (Decision 4). After this task the hardening layer installs exactly one embedded binary, cove-master. `internal/dispatch/worker` stays until Task 4 (at-cove's scheduler engine still imports it).

**Files:**
- Delete: `cmd/at-task/`, `internal/attask/`
- Rename: `scripts/stage-attask.sh` → `scripts/stage-covemaster.sh`
- Modify: `internal/assemble/{assemble.go,assemble_test.go,hardening/Dockerfile}`, `internal/install/{currency.go,currency_test.go}`, `internal/covemasterbin/{covemasterbin.go,bin/README}`, `scripts/build.sh`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `.gitignore`

**Interfaces:**
- Consumes: `TestAssembleContextStagesOnlyCoveMaster` (Task 1).
- Produces: `scripts/stage-covemaster.sh` (the only staging script; goreleaser and `build.sh` call it); `assemble.Revision == "4"`; `install.AtCoveIdentity()` hashes hardening + `assemble.Revision` + harness payload + baseline + `covemasterbin.BinFS()` only.

- [ ] **Step 1: Extend the guard test (it must fail)**

In `TestAssembleContextStagesOnlyCoveMaster` change the two lists:

```go
	for _, gone := range []string{"switchboard", "attask"} {
```

```go
	for _, gone := range []string{"at-switchboard", "/tmp/switchboard", "at-task", "/tmp/attask"} {
```

Run: `go test ./internal/assemble/ -run TestAssembleContextStagesOnlyCoveMaster -v`
Expected: FAIL — `build context still has attask/`.

- [ ] **Step 2: Remove at-task from the payload**

In `internal/assemble/assemble.go`: delete the import `"github.com/aethons-tools/cove/internal/attask"`; delete

```go
	if err := writeAtTask(buildDir); err != nil {
		return err
	}

```

run `/tmp/rmfunc.py internal/assemble/assemble.go writeAtTask`; change the comments:
- `AssembleContext` doc: `and the injected\n// at-task/cove-master binaries)` → `and the injected\n// cove-master binaries)`;
- the in-body comment `// build context is the harness layer, the sealed hardening layer, the\n\t// injected at-task, the kit's egress lists, and the managed key.` → `// build context is the harness layer, the sealed hardening layer, the\n\t// injected cove-master, the kit's egress lists, and the managed key.`;
- `writeCoveMaster` doc: `layer can install the arch-matching one — mirrors writeAtTask (COV-158).` → `layer can install the arch-matching one (COV-158).`, and `a plain `go build` without scripts/stage-attask.sh` → `a plain `go build` without scripts/stage-covemaster.sh`;
- bump the revision with its log line:

```go
// 3: sshd trusts Jam's SSH user CA (cove.conf TrustedUserCAKeys) and ships
// the sealed install-ssh-ca.sh helper.
// 4: no attask/ or switchboard/ staging dirs (at-task and at-switchboard
// deleted, jam-only slice 2).
var Revision = "4"
```

In `internal/assemble/hardening/Dockerfile` delete the stanza from `# Install the embedded, version-locked at-task the orchestrating at-cove staged` through `&& rm -rf /tmp/attask` and the blank line after it.

In `internal/assemble/assemble_test.go`: `/tmp/rmfunc.py internal/assemble/assemble_test.go TestAssembleStagesAtTask` (its doc comment `// Assemble stages the at-task binaries …` goes with it).

In `internal/install/currency.go` delete the import `"github.com/aethons-tools/cove/internal/attask"` and the block

```go
	at, err := HashTree(attask.BinFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("attask"))
	writeField(h, []byte(at))

```

and in the `AtCoveIdentity` comment `— and the embedded at-task\n// and cove-master binaries.` → `— and the embedded cove-master binaries.`; in `CurrencyInputs` change `// hash of at-cove's embedded sealed layer + at-task binaries (AtCoveIdentity)` → `// hash of the embedded sealed layer + cove-master binaries (AtCoveIdentity)`.

- [ ] **Step 3: Delete the binary and its embed package; stage only cove-master**

```bash
git rm -rq cmd/at-task internal/attask
git mv scripts/stage-attask.sh scripts/stage-covemaster.sh
cat > scripts/stage-covemaster.sh <<'EOF'
#!/usr/bin/env bash
# stage-covemaster.sh — build the linux cove-master binaries at-jam embeds into
# internal/covemasterbin/bin/, BEFORE at-jam is built so its `//go:embed bin`
# picks them up. Both arches always (a studio may run on either VM arch).
# Shared by scripts/build.sh and the goreleaser before-hook so the staging logic
# has a single home. See internal/covemasterbin + COV-158.
#
# VERSION (env, optional) stamps the embedded binaries so they match the
# at-jam built alongside them; defaults to `git describe`.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

covemaster_dir="internal/covemasterbin/bin"
mkdir -p "$covemaster_dir"
for a in amd64 arm64; do
  echo "  staging cove-master linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${covemaster_dir}/cove-master-linux-${a}" ./cmd/cove-master
done
EOF
```

In `scripts/build.sh`: `BINARIES=(at-cove at-task at-mint at-jam)` → `BINARIES=(at-cove at-mint at-jam)`; replace the staging block with

```bash
# Stage the linux cove-master binaries at-jam embeds, before at-jam is built —
# shared with the goreleaser before-hook, see scripts/stage-covemaster.sh.
echo "Staging embedded cove-master (linux amd64+arm64)"
VERSION="$VERSION" ./scripts/stage-covemaster.sh
```

and the header line `# build.sh — build at-cove and at-task, host-sensitive by default.` → `# build.sh — build the host binaries, host-sensitive by default.`, `#   dist/<os>-<arch>/<binary>   # e.g. dist/linux-amd64/{at-cove,at-task}` → `#   dist/<os>-<arch>/<binary>   # e.g. dist/linux-amd64/{at-mint,at-jam}`.

In `.goreleaser.yaml` replace the first before-hook and its comment with

```yaml
    # Build the linux cove-master binaries at-jam embeds, before goreleaser builds.
    - bash scripts/stage-covemaster.sh
```

In `.github/workflows/release.yml`: in the `go:` paths filter `- 'scripts/stage-attask.sh'` → `- 'scripts/stage-covemaster.sh'`; the comment `# PR: compile all binaries (+ run the stage-attask / gen-blessed before-hooks)` → `# PR: compile all binaries (+ run the stage-covemaster / gen-blessed before-hooks)`.

In `internal/covemasterbin/covemasterbin.go` replace the package comment with

```go
// Package covemasterbin embeds the linux cove-master binaries into at-jam, so
// the launcher that builds a studio image installs the *exact* matching
// cove-master into the hardening layer — version lockstep, no coordination, no
// pin to drift. Both linux arches are embedded regardless of the host, since a
// studio may run on either VM arch.
```

and change `staged by scripts/stage-attask.sh before at-cove is built so` → `staged by scripts/stage-covemaster.sh before at-jam is built so`, the error text `run scripts/stage-attask.sh (or use a release build)` → `run scripts/stage-covemaster.sh (or use a release build)`, and `BinFS`'s comment `as part of at-cove's build identity for the\n// install currency check (COV-38), so a cove-master rebuild invalidates installs.` → `as part of the assembly identity (AtCoveIdentity),\n// so a cove-master rebuild retags every studio image.`.

Replace `internal/covemasterbin/bin/README` with:

```text
cove-master binaries embedded into at-jam for version lockstep (COV-158).

This placeholder keeps `//go:embed bin` compiling in a fresh checkout. The
real per-arch binaries (cove-master-linux-amd64, cove-master-linux-arm64)
are staged here by scripts/stage-covemaster.sh / the release workflow before
at-jam is built, and are gitignored.
```

In `.gitignore` delete the line `/at-task`.

- [ ] **Step 4: Verify**

```bash
go run golang.org/x/tools/cmd/goimports@latest -w internal/assemble/assemble.go internal/install/currency.go
gofmt -w internal/assemble internal/install internal/covemasterbin
go build ./... && go vet ./... && go vet -tags integration ./...
go test ./internal/assemble/ ./internal/install/ ./internal/covemasterbin/ ./internal/jam/launcher/ 2>&1 | tail -5
bash scripts/stage-covemaster.sh && ls -l internal/covemasterbin/bin/ && git status --short internal/covemasterbin/bin/
grep -rn "stage-attask" --exclude-dir=superpowers --exclude-dir=.git . ; echo "grep-exit=$?"
just test && just lint
```

Expected: all PASS (`TestAssembleContextStagesOnlyCoveMaster` included); the staged cove-master binaries appear but `git status` shows them ignored (no output); the only `stage-attask` hits left are in `docs/` (Task 7 fixes those), never in code or CI files; `just test`/`just lint` exit 0.

- [ ] **Step 5: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "refactor: delete at-task; the hardening layer embeds only cove-master

at-task was only ever run by at-cove's dispatchrun. Its embed package,
staging loop and Dockerfile stanza go; stage-attask.sh becomes
stage-covemaster.sh. assemble.Revision 4: every studio image retags once.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: Delete `cmd/at-cove`; its release slot becomes a tombstone; Jam generates its own launcher key

`cmd/at-cove` is deleted with its build, archive entry, installer slot and `just` recipes. Two things at-cove did for Jam must survive it:

1. **The launcher key.** `at-cove` (`keys.Ensure`) was the only code that generated `~/.config/at-cove/id_ed25519`, the key Jam's launcher bakes into studio images (`runtime.launcher.identity-file`, defaulting there). Jam's serve only reads `<identity-file>.pub`. So `internal/keys` **stays** (the starting inventory listed it as dying — it does not) and serve generates the pair on first use.
2. **`at-jam update` from an older Jam.** Every pre-slice `at-jam` embeds an `install.sh` that loops `for b in at-cove at-mint at-jam` and `place`s `at-cove` under `set -e`. The archive therefore keeps an `at-cove` entry — a tombstone script — so the old updater succeeds. The new `install.sh` does not install it. Slice 3 (new archive name) drops it.

**Files:**
- Delete: `cmd/at-cove/`
- Create: `scripts/release/at-cove` (0755), `testdata/install-legacy.sh` (frozen copy of today's `install.sh`)
- Modify: `internal/keys/keys.go`, `internal/keys/keys_test.go`, `cmd/at-jam/main.go`, `cmd/at-jam/update.go`, `cmd/at-jam/update_test.go`, `installer.go`, `internal/update/update.go`, `install.sh`, `install.test.sh`, `scripts/build.sh`, `scripts/lint.sh`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `justfile`, `.gitignore`

**Interfaces:**
- Consumes: `scripts/stage-covemaster.sh` (Task 2).
- Produces: `keys.EnsureFile(r runner.Runner, priv string) ([]byte, error)` (replaces `keys.Ensure(r, dir) (string, []byte, error)`); release archive `cove_<ver>_<os>_<arch>.tar.gz` = `at-cove` (tombstone, 0755) + `at-mint` + `at-jam`; `install.sh` installs `at-mint` + `at-jam`.

- [ ] **Step 1: Write the key tests (they must fail)**

Replace `internal/keys/keys_test.go` with:

```go
package keys

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestEnsureFileUsesExistingKey(t *testing.T) {
	priv := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(priv, []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	pub, err := EnsureFile(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA test\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("should not call ssh-keygen when the key exists: %+v", f.Calls)
	}
}

// A fresh Jam host has no launcher key (at-cove used to generate it): the
// launcher generates it at the configured path.
func TestEnsureFileGeneratesWhenMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "at-cove")
	priv := filepath.Join(dir, "id_ed25519")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// runner.Fake runs nothing, so pre-place the .pub the real ssh-keygen writes.
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	pub, err := EnsureFile(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA gen\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh-keygen" {
		t.Fatalf("expected one ssh-keygen call, got %+v", f.Calls)
	}
	if args := f.Calls[0].Args; len(args) == 0 || args[len(args)-1] != priv {
		t.Fatalf("ssh-keygen must write %s, args = %q", priv, args)
	}
}

func TestEnsureFileCreatesKeyDir0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "at-cove")
	_, _ = EnsureFile(&runner.Fake{}, filepath.Join(dir, "id_ed25519")) // the .pub read fails: the fake generated nothing
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("key dir not created: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("key dir mode = %v, want 0700", st.Mode().Perm())
	}
}
```

Run: `go test ./internal/keys/`
Expected: FAIL — `undefined: EnsureFile`.

- [ ] **Step 2: Implement `EnsureFile` and wire it into serve**

Replace `internal/keys/keys.go` with:

```go
// Package keys manages the SSH keypair Jam's launcher uses to reach the
// studios it raises (runtime.launcher.identity-file): the public half is baked
// into each studio image's authorized_keys.
package keys

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/runner"
)

// EnsureFile returns the public key of the ed25519 keypair whose private half
// is priv (read from priv + ".pub"), generating the pair with ssh-keygen when
// priv does not exist yet. priv's directory is created 0700 if missing.
func EnsureFile(r runner.Runner, priv string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(priv); errors.Is(err, os.ErrNotExist) {
		if err := r.Run("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "jam-launcher", "-f", priv); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return os.ReadFile(priv + ".pub")
}
```

In `cmd/at-jam/main.go` (the `if lc := cfg.Runtime.Launcher; lc != nil {` block) replace

```go
		// The launcher builds each studio kit's image on demand, so it needs the
		// public half of the SSH identity to bake into the image's authorized_keys
		// (the same key at-cove install baked; its private half is IdentityFile). No
		// kit source directory: the build context comes from the kit definition
		// (data) + resources compiled into this binary, and the FROM-base is resolved
		// per kit on the substrate (gate ON).
		pub, err := os.ReadFile(lc.IdentityFile + ".pub")
```

with

```go
		// The launcher builds each studio kit's image on demand, so it needs the
		// public half of the SSH identity to bake into the image's authorized_keys
		// (its private half is IdentityFile), generated on first use. No kit
		// source directory: the build context comes from the kit definition
		// (data) + resources compiled into this binary, and the FROM-base is
		// resolved per kit on the substrate (gate ON).
		pub, err := keys.EnsureFile(runner.OS{}, lc.IdentityFile)
```

and add the import `"github.com/aethons-tools/cove/internal/keys"`. In the same file change the `update` command's Brief from `"update the cove installation (at-jam, at-cove, at-mint) to the latest release"` to `"update the cove installation (at-jam, at-mint) to the latest release"`.

In `cmd/at-jam/config.go` fix the two comments that describe the key as at-cove's: in `launcherConfig` replace

```go
	// IdentityFile/KnownHostsDir are the SSH identity Jam uses to reach a
	// raised cove. They must be the same key `at-cove install` baked into the
	// image's authorized_keys. Default to the at-cove config dir's
	// id_ed25519 / known_hosts.d when empty, so a Jam host colocated with
	// at-cove needs no explicit path.
```

with

```go
	// IdentityFile/KnownHostsDir are the SSH identity Jam uses to reach a
	// raised cove; serve generates the key on first use and bakes its public
	// half into every studio image. Default to the at-cove config dir's
	// id_ed25519 / known_hosts.d when empty (the directory keeps its old name
	// until the jam rename).
```

and replace the `atCoveConfigDir` comment with

```go
// atCoveConfigDir is $XDG_CONFIG_HOME/at-cove, else ~/.config/at-cove — the
// launcher's default home for its SSH identity and known_hosts, kept from the
// deleted at-cove CLI so existing Jam hosts keep their key.
```

- [ ] **Step 3: Run the key tests**

Run: `go test ./internal/keys/ && go build ./cmd/at-jam`
Expected: PASS; at-jam builds (cmd/at-cove still calls the old `keys.Ensure` — it is deleted in Step 6, so do not run `go build ./...` yet).

- [ ] **Step 4: Write the installer compatibility test (it must fail)**

Freeze today's installer before changing it, then add the compat block:

```bash
mkdir -p testdata
{ printf '%s\n' '#!/usr/bin/env bash' \
  '# FROZEN FIXTURE — do not edit. This is install.sh exactly as it shipped before' \
  '# jam-only slice 2: the copy embedded in every older at-jam, which `at-jam' \
  '# update` runs. install.test.sh drives it against a current-shaped release to' \
  '# prove an older at-jam can still update (it installs at-cove unconditionally,' \
  '# hence the at-cove tombstone in the release archive). Delete in slice 3.'
  tail -n +2 install.sh; } > testdata/install-legacy.sh
```

In `install.test.sh`:
- header: `# executables on PATH that serve a locally-built fixture "release" (fake\n# at-cove/at-mint/at-jam binaries + a real checksums.txt)` → `… (fake\n# at-cove-tombstone/at-mint/at-jam binaries + a real checksums.txt)`;
- replace `build_archive`'s comment and first lines with

```bash
# build a fixture archive carrying fake at-cove (the tombstone) + at-mint +
# at-jam for a given os/arch. Optional 3rd arg: a version (default
# FIXTURE_VERSION); a 4th arg of "nojam" omits at-jam, like a release cut before
# at-jam shipped, and "nocove" omits the at-cove tombstone.
build_archive() {
  local os="$1" arch="$2" ver="${3:-$FIXTURE_VERSION}" variant="${4:-}"
  local stage="$WORK/stage-$ver-$os-$arch"
  local bins=(at-cove at-mint at-jam)
  [ "$variant" = nojam ] && bins=(at-cove at-mint)
  [ "$variant" = nocove ] && bins=(at-mint at-jam)
```

- after `build_archive linux amd64 "$PRE_JAM_VERSION" nojam` add

```bash
NO_COVE_VERSION="124-0722"
build_archive linux amd64 "$NO_COVE_VERSION" nocove
```

- replace the four `BIN1` assertions with

```bash
if [ -x "$BIN1/at-mint" ]; then ok "at-mint installed + executable"; else bad "at-mint installed + executable" "$out"; fi
if [ -x "$BIN1/at-jam" ]; then ok "at-jam installed + executable"; else bad "at-jam installed + executable" "$out"; fi
if [ -e "$BIN1/at-cove" ]; then bad "at-cove tombstone NOT installed"; else ok "at-cove tombstone NOT installed"; fi
if [ -e "$BIN1/at-task" ]; then bad "at-task NOT installed"; else ok "at-task NOT installed"; fi
```

- pre-at-jam: `[ -x "$BIN0/at-cove" ]` → `[ -x "$BIN0/at-mint" ]`; latest: `[ -x "$BIN3/at-cove" ]` → `[ -x "$BIN3/at-jam" ]`; tamper: `[ -e "$BIN4/at-cove" ]` → `[ -e "$BIN4/at-jam" ]`;
- immediately before `echo "== e2e: tampered checksum aborts, nothing installed =="` (the tamper block rewrites the gh stub, so this must come first) insert

```bash
echo "== compat: an older at-jam's embedded installer updates to this release =="
# testdata/install-legacy.sh is install.sh as embedded in every pre-slice-2
# at-jam (`at-jam update` runs it). It places at-cove unconditionally, so the
# release must keep the at-cove tombstone for it to succeed.
LEGACY_SH="$HERE/testdata/install-legacy.sh"
BIN5="$WORK/bin5"
out="$(UNAME_S=Linux UNAME_M=x86_64 COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN5" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
if [ "$rc" = 0 ] && [ -x "$BIN5/at-jam" ] && [ -x "$BIN5/at-mint" ]; then ok "legacy installer updates at-jam + at-mint"; else bad "legacy installer updates at-jam + at-mint" "rc=$rc" "$out"; fi
BIN6="$WORK/bin6"
out="$(UNAME_S=Linux UNAME_M=x86_64 COVE_VERSION="$NO_COVE_VERSION" BINDIR="$BIN6" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
if [ "$rc" != 0 ]; then ok "legacy installer needs the at-cove tombstone (fails without it)"; else bad "legacy installer needs the at-cove tombstone (fails without it)" "$out"; fi

```

Run: `bash install.test.sh 2>&1 | tail -15`
Expected: FAIL — `at-cove tombstone NOT installed` fails (today's `install.sh` still installs at-cove); both compat checks pass (they prove the fixture behaves like the old updater).

- [ ] **Step 5: Change the installer; add the tombstone**

In `install.sh`:
- line 2 → `# install.sh — one-command installer for the cove host binaries: at-jam and at-mint.`;
- replace `# release's checksums.txt, and drops `at-cove` + `at-mint` + `at-jam` onto your PATH.\n# `at-task` is embedded inside `at-cove`, so it is not installed separately.` with

```bash
# release's checksums.txt, and drops `at-mint` + `at-jam` onto your PATH. The
# archive also carries an `at-cove` tombstone (scripts/release/at-cove) for older
# installers; this one does not install it.
```

- `for b in at-cove at-mint at-jam; do` → `for b in at-mint at-jam; do`;
- replace the final version print with

```bash
  if command -v "$bindir/at-jam" >/dev/null 2>&1; then
    info "$("$bindir/at-jam" version 2>/dev/null || echo "at-jam $version")"
  fi
```

Create the tombstone:

```bash
mkdir -p scripts/release
cat > scripts/release/at-cove <<'EOF'
#!/bin/sh
# at-cove tombstone. The standalone at-cove CLI was removed (jam-only slice 2);
# Jam (at-jam) is the only runtime. This stub ships in the release archive only
# so an older `at-jam update` — whose embedded install.sh installs at-cove,
# at-mint and at-jam unconditionally — keeps working. Slice 3 (the rename to
# `jam`) drops it. Safe to delete from your PATH.
if [ "${1:-}" = version ]; then
  echo "at-cove (removed; use at-jam)"
  exit 0
fi
echo "at-cove was removed: Jam (at-jam) is the only runtime — see docs/usage/jam/INDEX.md" >&2
exit 1
EOF
chmod 0755 scripts/release/at-cove
```

In `scripts/lint.sh` change `  shellcheck install.sh install.test.sh \` to `  shellcheck install.sh install.test.sh scripts/release/at-cove testdata/install-legacy.sh \`.

Run: `bash install.test.sh 2>&1 | tail -4`
Expected: `-------- 25 passed, 0 failed --------`.

- [ ] **Step 6: Delete the binary and its build/release slot**

```bash
git rm -rq cmd/at-cove
```

`.goreleaser.yaml`:
- replace the header comment (everything above `version: 2`) with

```yaml
# goreleaser config for the cove host binaries — the BUILD tool for the binaries
# leg of the monolithic pipeline (.github/workflows/release.yml, COV-44). CI runs
# `goreleaser release --snapshot` on every main push: goreleaser builds + archives
# at-mint + at-jam (darwin/linux × amd64/arm64) stamped <N>-<MMDD> via the VERSION
# env; the workflow then cuts the (private) GitHub release with `gh`. Snapshot mode
# is used deliberately — it skips goreleaser's own publishing, so the non-SemVer
# <N>-<MMDD> version needs no git tag to satisfy goreleaser. cove-master is NOT
# shipped standalone — it is embedded in at-jam (the before-hook stages the linux
# cove-master binaries first). The archive also carries scripts/release/at-cove,
# a tombstone that keeps an older `at-jam update` (whose embedded install.sh
# installs at-cove unconditionally) working; slice 3 of the jam-only design drops it.
#
# Public phase (later): add a `brews:` block that installs at-mint and at-jam
# from the single archive below.
```

- in the gen-blessed hook comment `before at-cove is built` → `before at-jam is built`;
- delete the whole `- id: at-cove` build entry (from `  - id: at-cove` to the line before `  - id: at-mint`);
- replace the `archives:` block with

```yaml
archives:
  # One archive carrying every host binary — install.sh (and so `at-jam update`)
  # installs them together — plus the at-cove tombstone for older installers.
  - id: cove
    ids: [at-mint, at-jam]
    name_template: "cove_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - src: scripts/release/at-cove
        strip_parent: true
        info:
          mode: 0755
```

(`files:` replaces goreleaser's default README/LICENSE globs; `install.sh` extracts only the binaries, so nothing reads them.)

`.github/workflows/release.yml` (keep the job id and output key `atcove` — job names are slice 3's):

```bash
sed -i \
  -e 's|at-cove (embeds at-task + list)|at-jam + at-mint (embed cove-master + list)|' \
  -e 's|# gh cuts the at-cove release + its <N>-<MMDD> tag|# gh cuts the release + its <N>-<MMDD> tag|' \
  -e 's|# re-cut at-cove (+ blessed list)|# re-cut the host binaries (+ blessed list)|' \
  -e "s|- 'install.sh' # embedded in at-cove + at-jam (\`update\`)|- 'install.sh' # embedded in at-jam (\`update\`)\n              - 'scripts/release/**' # the at-cove tombstone in the archive|" \
  -e 's|AND cove-image (downstream) AND at-cove|AND cove-image (downstream) AND the binaries|' \
  -e 's|#   go change       => at-cove only|#   go change       => the binaries only|' \
  -e "s|before the at-cove leg's|before the binaries leg's|" \
  -e 's|# Re-cut at-cove (with at-task embedded + the blessed-list snapshot)|# Re-cut the host binaries (cove-master embedded + the blessed-list snapshot)|' \
  -e 's|# Run when the at-cove leg is selected|# Run when the binaries leg is selected|' \
  -e 's|name: Build at-cove (PR — no publish)|name: Build binaries (PR — no publish)|' \
  -e 's|name: Build at-cove (main)|name: Build binaries (main)|' \
  -e 's|name: Publish at-cove release|name: Publish release|' \
  -e 's|(at-cove + at-mint + at-jam; at-task embedded)|(at-mint + at-jam; cove-master embedded; at-cove tombstone)|' \
  -e 's|## Released at-cove|## Released|' \
  .github/workflows/release.yml
grep -n "at-cove\|at-task" .github/workflows/release.yml
```

Expected: exactly two hits, both naming the tombstone (the `scripts/release/**` filter comment and the release notes).

`scripts/build.sh`: `BINARIES=(at-cove at-mint at-jam)` → `BINARIES=(at-mint at-jam)`; `# The binaries are pure Go (no cgo; at-cove shells out to ssh/docker), so` → `# The binaries are pure Go (no cgo; at-jam shells out to ssh/docker), so`; `# binary); -X main.version: stamp the version reported by `at-cove version`.` → `… reported by `at-jam version`.`; `# embedded (gitignored) blessed/generated.txt, before at-cove is built (COV-44` → `… before at-jam is built (COV-44`.

`justfile`: replace the `build`/`build-all`/`run`/`run-dispatch` recipes and the `install` recipe with

```just
# build for the current host into dist/<os>-<arch>/{at-mint,at-jam} (host-sensitive)
build:
    ./scripts/build.sh

# cross-compile every supported target into dist/<os>-<arch>/{at-mint,at-jam}
build-all:
    ./scripts/build.sh all
```

```just
# install the host binaries (at-mint, at-jam) onto your PATH.
# Default dir: $(go env GOBIN) or $(go env GOPATH)/bin (~/go/bin) — no sudo.
# Override with BINDIR, e.g. `BINDIR=/usr/local/bin just install` (may need sudo)
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    bin="${BINDIR:-$(go env GOBIN)}"
    [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    plat="$(go env GOOS)-$(go env GOARCH)"
    mkdir -p "$bin"
    for b in at-mint at-jam; do
      install -m 0755 "dist/$plat/$b" "$bin/$b"
      echo "installed dist/$plat/$b -> $bin/$b"
    done
    if command -v at-jam >/dev/null 2>&1; then
      echo "on PATH: $(command -v at-jam) ($(at-jam version))"
    else
      echo "warning: $bin is not on your PATH — add it or use BINDIR=/usr/local/bin"
    fi
```

`.gitignore`: delete the line `/at-cove`.

Comments that name `at-cove update` as a sibling:
- `cmd/at-jam/update.go`: `// cmdUpdate updates the on-PATH cove binaries (at-jam ships in the same release\n// archive as at-cove and at-mint) via the shared update.Do — the same embedded\n// install.sh flow as `at-cove update`.` → `// cmdUpdate updates the on-PATH cove binaries (at-jam and at-mint ship in one\n// release archive) via the shared update.Do and the embedded install.sh.`;
- `cmd/at-jam/update_test.go`: `the same knob install.sh and `at-cove update` honor.` → `the same knob install.sh honors.`;
- `installer.go`: `Currently that is install.sh — the one-command installer. `at-cove update`\n// (COV-128) re-runs it` → `Currently that is install.sh — the one-command installer. `at-jam update`\n// (COV-128) re-runs it`; `is the one baked into at-cove.` → `is the one baked into at-jam.`; `temp file and drives it via the Runner; see cmd/at-cove doUpdate.` → `temp file and drives it via the Runner; see cmd/at-jam update.go.`;
- `internal/update/update.go` package comment: `binaries (at-cove, at-mint, at-jam) to a GitHub release (COV-128). Both\n// `at-cove update` and `at-jam update` call Do.` → `binaries (at-mint, at-jam) to a GitHub release (COV-128). `at-jam update`\n// calls Do.`; `so `at-cove update` must never replace a` → `so `at-jam update` must never replace a`.

- [ ] **Step 7: Verify the build, the suite, and the release config**

```bash
gofmt -w cmd/at-jam internal/keys installer.go internal/update
go build ./... && go vet ./... && go vet -tags integration ./...
go list ./... | grep -E '/cmd/at-cove$' ; echo "list-exit=$?"
go test ./internal/keys/ ./internal/update/ ./cmd/at-jam/ 2>&1 | tail -3
bash install.test.sh 2>&1 | tail -2
GOTOOLCHAIN=local go run github.com/goreleaser/goreleaser/v2@v2.12.7 check
VERSION=9999-1026 GOTOOLCHAIN=local go run github.com/goreleaser/goreleaser/v2@v2.12.7 release --snapshot --clean --skip=publish
tar -tzvf dist/cove_9999-1026_linux_amd64.tar.gz
just test && just lint
```

Expected: build/vet clean; `list-exit=1`; tests PASS; `25 passed, 0 failed`; `1 configuration file(s) validated`; the snapshot (≈10 min: it builds 8 binaries and the web bundles) lists exactly `-rwxr-xr-x … at-cove` (a few hundred bytes), `at-mint`, `at-jam` (Review Focus 5); `just test`/`just lint` exit 0. (`GOTOOLCHAIN=local` and the `v2.12.7` pin keep goreleaser on this sandbox's Go; newer goreleaser wants a toolchain download that `GOSUMDB=off` blocks.) Remove `dist/` afterwards (`rm -rf dist`; it is gitignored).

- [ ] **Step 8: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "feat!: delete the at-cove CLI; Jam generates its launcher key

at-cove, its goreleaser build and installer slot go. The release
archive keeps an at-cove tombstone so an older at-jam update (whose
embedded install.sh installs at-cove unconditionally) still works;
install.test.sh drives a frozen copy of that installer. internal/keys
stays: serve now generates runtime.launcher.identity-file on first
use, which only at-cove used to do.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: Delete the packages, kits and data only at-cove reached

With `cmd/at-cove` gone these compile but nothing imports them. Prove that first, then delete. The scheduler package keeps its types (`Role`, `Issue`, `Comment`, `ClassifyLabels`, `AssembleBrief` — Jam uses them); only its `Engine` goes, and with it the last importer of `internal/dispatch/worker`. The repo's own `.at-cove/` kit is dead (the repo's Jam kit is `.at-jam/cove-ic`), so `adopt-base` — which pinned `.at-cove/config.yml`'s `image.base` — keeps only its watermark job (Jam's default base is the blessed head the release pipeline snapshots; a routine base needs no adoption).

**Files:**
- Delete: `internal/dispatchrun/`, `internal/state/`, `internal/sshconfig/`, `internal/dispatch/exec/`, `internal/dispatch/github/`, `internal/dispatch/gitlab/`, `internal/dispatch/worker/`, `internal/dispatch/doc.go`, `internal/dispatch/scheduler/{engine.go,engine_test.go,fakes_test.go}`, `internal/dockere2e/`, `kits/`, `.at-cove/`, `internal/kit/refkit_test.go`, `internal/usersecret/runbook_example_test.go`
- Modify: `internal/dispatch/scheduler/scheduler.go`, `cmd/adopt-base/main.go`, `internal/adoptbase/adoptbase.go`, `internal/adoptbase/adoptbase_test.go`, `justfile`, `.air.toml`

**Interfaces:**
- Consumes: Task 3's tree (no `cmd/at-cove`).
- Produces: `adopt-base <tag> [--pr] [--reason "..."]` (no `--breaking`: raising the watermark is its only job); `adoptbase.RewriteWatermark(watermarkTxt, tag, digest, reason string) (string, error)` unchanged; `adoptbase.RewriteImageBase` and `adoptbase.CoveImageRepo` deleted.

- [ ] **Step 1: Prove the packages are unreachable**

```bash
M=github.com/aethons-tools/cove
for c in at-jam at-mint cove-master adopt-base gen-blessed gen-icons; do go list -deps ./cmd/$c; done | grep "^$M/" | sort -u > /tmp/reached.txt
go list ./... | sort > /tmp/all.txt
comm -23 /tmp/all.txt /tmp/reached.txt | grep -v -e '/cmd/' -e 'test$' -e "^$M\$"
```

Expected exactly (test-helper packages like `…/storetest` are filtered by the `test$` pattern):

```
github.com/aethons-tools/cove/internal/dispatch
github.com/aethons-tools/cove/internal/dispatch/exec
github.com/aethons-tools/cove/internal/dispatch/github
github.com/aethons-tools/cove/internal/dispatch/gitlab
github.com/aethons-tools/cove/internal/dispatchrun
github.com/aethons-tools/cove/internal/sshconfig
github.com/aethons-tools/cove/internal/state
```

(`internal/dispatch/worker` is still reached — through `scheduler/engine.go` — until Step 2 deletes the engine; `internal/dockere2e` is an integration-tag-only test package that shells the deleted `at-cove`; `internal/keys` is reached via at-jam since Task 3.) If the list differs, stop and find the importer before deleting anything.

- [ ] **Step 2: Delete them, the engine, and the at-cove-only fixtures**

```bash
git rm -rq internal/dispatchrun internal/state internal/sshconfig \
  internal/dispatch/exec internal/dispatch/github internal/dispatch/gitlab internal/dispatch/worker \
  internal/dispatch/doc.go \
  internal/dispatch/scheduler/engine.go internal/dispatch/scheduler/engine_test.go internal/dispatch/scheduler/fakes_test.go \
  internal/dockere2e kits .at-cove \
  internal/kit/refkit_test.go internal/usersecret/runbook_example_test.go
```

(`refkit_test.go` and `runbook_example_test.go` only read `kits/reference-worker/`.)

In `internal/dispatch/scheduler/scheduler.go` replace the package comment with

```go
// Package scheduler holds the tracker vocabulary Jam's Requisitioner and its
// trackers share: lifecycle Roles, the Issue and Comment shapes, label
// classification (ClassifyLabels) and the brief a raised session receives
// (AssembleBrief). The polling engine that used to live here went with at-cove.
```

and in `Issue.DispatchLabeled`'s comment replace `Jam's resident\n\t// dispatcher raises a cove only for labeled issues; the standalone scheduler\n\t// ignores this field.` with `Jam's resident\n\t// dispatcher raises a cove only for labeled issues.`; in `Issue`'s and `Role`'s comments `the engine` → `Jam`.

`justfile`: delete the `e2e` recipe and the `integration-docker` recipe, each with its comment lines.

`.air.toml`: in `exclude_dir` delete the `".at-cove", ` entry.

- [ ] **Step 3: Narrow adopt-base to the watermark**

```bash
/tmp/rmfunc.py internal/adoptbase/adoptbase.go RewriteImageBase leadingWhitespace
/tmp/rmfunc.py internal/adoptbase/adoptbase_test.go TestRewriteImageBase TestRewriteImageBaseErrors TestRewriteImageBaseIgnoresSiblingRepo
```

In `internal/adoptbase/adoptbase.go` replace the package comment with

```go
// Package adoptbase holds the pure file transform that adopts a breaking base
// image: raising the blessed watermark. It operates on file *contents* (no
// I/O) so it is hermetically testable; the cmd/adopt-base shell reads/writes
// the file.
```

and delete `// CoveImageRepo is the toolchain-image repository that image.base names.` with its `const CoveImageRepo = …` line. In `internal/adoptbase/adoptbase_test.go` delete the `sampleConfig` const and the `goodDigest` const (neither is used any more; `baseDigest` stays).

Replace `cmd/adopt-base/main.go` with:

```go
// Command adopt-base raises the blessed watermark to a newly-published,
// breaking base image. It resolves a release tag to cove-base-image's @sha256
// index digest via the GitHub packages API and rewrites
// internal/basedigest/blessed/watermark.txt, so kits on older bases fail the
// provenance gate until they move. With --pr it branches, commits, and opens a
// PR; otherwise it edits the file and prints next steps. (A routine base needs
// no adoption: the release pipeline's blessed-list snapshot makes the newest
// base Jam's default.)
//
// It needs GITHUB_TOKEN with read:packages (same as gen-blessed) and — unlike
// gen-blessed — cannot no-op offline, since it has nothing to resolve.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/adoptbase"
	"github.com/aethons-tools/cove/internal/blessgen"
)

const (
	owner         = "aethons-tools"
	coveBasePkg   = "cove-base-image"
	watermarkPath = "internal/basedigest/blessed/watermark.txt"
)

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintln(os.Stderr, `usage: adopt-base <tag> [--pr] [--reason "..."]`)
		os.Exit(2)
	}
	tag := os.Args[1]
	fs := flag.NewFlagSet("adopt-base", flag.ExitOnError)
	pr := fs.Bool("pr", false, "branch, commit, push, and open a PR")
	reason := fs.String("reason", "", `parenthetical for the watermark comment`)
	_ = fs.Parse(os.Args[2:])

	if err := run(tag, *pr, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "adopt-base: %v\n", err)
		os.Exit(1)
	}
}

func run(tag string, pr bool, reason string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required (needs read:packages); export a token and retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpc := &http.Client{Timeout: 30 * time.Second}

	// Resolve BEFORE writing, so a resolution failure (bad/absent tag, auth,
	// transient error) never leaves a partial edit.
	baseDigest, err := blessgen.GHCR{HTTP: httpc, Token: token, Owner: owner, Package: coveBasePkg}.
		DigestForTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("resolve %s:%s: %w", coveBasePkg, tag, err)
	}
	if err := rewriteFile(watermarkPath, func(s string) (string, error) {
		return adoptbase.RewriteWatermark(s, tag, baseDigest, reason)
	}); err != nil {
		return err
	}
	fmt.Printf("watermark -> cove-base-image@%s (blessed floor raised; run `just gen-blessed` to preview the set)\n", baseDigest)

	if pr {
		return openPR(tag)
	}
	fmt.Println("done — file edited, not committed. Review the diff, then commit (or re-run with --pr).")
	return nil
}

// rewriteFile reads path, applies transform to its contents, and writes it back.
func rewriteFile(path string, transform func(string) (string, error)) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := transform(string(b))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// openPR branches, commits the edited watermark, pushes, and opens a PR via gh.
func openPR(tag string) error {
	branch := "chore/adopt-base-" + tag
	title := "chore(base-image): raise blessed watermark to " + tag + " (breaking)"
	body := fmt.Sprintf("Raise the blessed watermark to the base image published as `%s`, pinned by `@sha256` digest.\n\n**Breaking:** kits still pinned to an older base are rejected by the provenance gate until they adopt.", tag)
	steps := [][]string{
		{"git", "checkout", "-b", branch},
		{"git", "add", watermarkPath},
		{"git", "commit", "-m", title + "\n\n" + body},
		{"git", "push", "-u", "origin", branch},
		{"gh", "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body},
	}
	for _, args := range steps {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}
```

In `justfile` replace the `adopt-base` recipe's comment with

```just
# raise the blessed watermark to a published, breaking base image: resolve <tag>
# to its cove-base-image @sha256 index digest and rewrite
# internal/basedigest/blessed/watermark.txt. Add --pr to branch+commit+open a PR.
# Needs GITHUB_TOKEN (read:packages). e.g. `just adopt-base 527-0808 --pr`
```

- [ ] **Step 4: Verify nothing reaches the deleted paths**

```bash
go run golang.org/x/tools/cmd/goimports@latest -w internal/adoptbase internal/dispatch/scheduler
gofmt -w cmd/adopt-base internal/adoptbase internal/dispatch/scheduler
go build ./... && go vet ./... && go vet -tags integration ./...
for p in dispatchrun state sshconfig dispatch/exec dispatch/github dispatch/gitlab dispatch/worker dockere2e; do
  go list ./... | grep -x "github.com/aethons-tools/cove/internal/$p" && echo "STILL THERE: $p"
done
grep -rn --include=*.go -e 'internal/dispatchrun"' -e 'internal/state"' -e 'internal/sshconfig"' -e 'internal/dispatch/worker"' -e 'internal/dispatch/exec"' -e 'internal/dispatch/github"' -e 'internal/dispatch/gitlab"' . ; echo "grep-exit=$?"
go test ./internal/dispatch/... ./internal/adoptbase/ ./internal/kit/ ./internal/usersecret/ ./cmd/adopt-base/ 2>&1 | tail -6
just test && just lint
```

Expected: build/vet clean; no `STILL THERE`; `grep-exit=1`; tests PASS; `just test`/`just lint` exit 0.

- [ ] **Step 5: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "refactor: delete the packages, kits and data only at-cove reached

dispatchrun, state, sshconfig, dispatch/{exec,github,gitlab,worker},
the scheduler engine, dockere2e, kits/ and the repo's .at-cove/ kit.
adopt-base keeps only its watermark job: Jam's default base is the
blessed head, so a routine base needs no pin.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Narrow the trackers to Jam's contract

`githubissues.New` (at-cove's `#N` keys, no dispatch gate, first-page search, plain rate-limit errors) has no caller left; `NewJam` becomes the only constructor and the `jam` mode flag goes. With the engine gone, `scheduler.Tracker`/`Executor`/`InProgressIssue` and both trackers' `ListInProgress` (the stale-claim reaper's read) have no caller either. `linear.New` stops taking a whole `kit.Config` just to read `Tracker.Linear` (Task 6 deletes `kit.Config`).

**Files:**
- Modify: `internal/dispatch/githubissues/{githubissues.go,githubissues_test.go,jam_test.go,feed_test.go}`, `internal/dispatch/linear/{linear.go,linear_test.go,linear_integration_test.go}`, `internal/dispatch/scheduler/scheduler.go`, `internal/tracker/trackertest/linear.go`, `cmd/at-jam/tracker.go`

**Interfaces:**
- Consumes: `scheduler.Role/Issue/Comment/ClassifyLabels` (Task 4 keeps them).
- Produces: `githubissues.NewJam(gt kit.GitHubTracker, token string, httpc *http.Client) (*Client, error)` — the only constructor (validates the repo, rejects an empty token, defaults `class:`/`dispatch:`); `linear.New(lt kit.LinearTracker, token string, httpc *http.Client) (*linear.Client, error)`; both satisfy `tracker.Tracker` (and githubissues `tracker.CursorFeed`); `scheduler.Tracker`, `scheduler.Executor`, `scheduler.InProgressIssue`, `(*githubissues.Client).ListInProgress`, `(*githubissues.Client).enteredAt`, `(*linear.Client).ListInProgress` deleted.

- [ ] **Step 1: Pin the GitHub tests to Jam semantics (characterization — they pass today)**

In `internal/dispatch/githubissues/githubissues_test.go`:
- replace `testCfg()` with

```go
func testStates() kit.StateMap {
	return kit.StateMap{
		Ready: "status:ready", InProgress: "status:in-progress",
		InReview: "status:in-review", NeedsInput: "status:needs-input",
		Blocked: "status:blocked",
	}
}
```

- in `newTestClient` replace `c, err := New(testCfg(), "tok", &http.Client{Transport: rt})` / `t.Fatalf("New: %v", err)` with `c, err := NewJam(jamTracker(), "tok", &http.Client{Transport: rt})` / `t.Fatalf("NewJam: %v", err)`;
- replace `TestNewRejectsEmptyToken` with

```go
func TestNewJamRejectsEmptyToken(t *testing.T) {
	if _, err := NewJam(jamTracker(), "", nil); err == nil {
		t.Fatal("NewJam with empty token: want error, got nil")
	}
}

func TestNewJamDefaultsPrefixes(t *testing.T) {
	gt := jamTracker()
	gt.ClassLabelPrefix = ""
	c, err := NewJam(gt, "tok", nil)
	if err != nil {
		t.Fatalf("NewJam: %v", err)
	}
	if c.prefix != "class:" || c.dispatchPrefix != "dispatch:" {
		t.Fatalf("prefixes = %q, %q; want class:, dispatch:", c.prefix, c.dispatchPrefix)
	}
}
```

- `/tmp/rmfunc.py internal/dispatch/githubissues/githubissues_test.go TestNewRequiresTrackerGitHub TestNewResolvesRepoOverrideAndInheritance TestListInProgressDerivesTimestampFromTimeline TestListInProgressSkipsWhenNoLabelEvent`;
- every ready-search fixture now needs the dispatch label (Jam gates on it): `sed -i 's/"labels":\[{"name":"class:implementor"},{"name":"status:ready"}\]/"labels":[{"name":"class:implementor"},{"name":"status:ready"},{"name":"dispatch:go"}]/; s/"labels":\[{"name":"status:ready"}\]/"labels":[{"name":"status:ready"},{"name":"dispatch:go"}]/' internal/dispatch/githubissues/githubissues_test.go`;
- in `TestListReadyParsesClassAndIdentity` the key is now qualified: `got[0].Identifier != "#42"` → `got[0].Identifier != "acme/board#42"` and the message `want ID=42 Identifier=#42 Class=implementor` → `want ID=42 Identifier=acme/board#42 Class=implementor`.

In `internal/dispatch/githubissues/jam_test.go`: `jamTracker()` → `return kit.GitHubTracker{Repo: "acme/board", ClassLabelPrefix: "class:", States: testStates()}`; `/tmp/rmfunc.py internal/dispatch/githubissues/jam_test.go TestNewKeepsAtCoveOutputs`.

In `internal/dispatch/githubissues/feed_test.go`:
- rename `TestRateLimitOnlyForJam` → `TestReadsRateLimited` with comment `// Reads (getJSON) surface a rate limit as a RateLimitError.`, and delete its second half — from `	srv := httptest.NewServer(mux)` (the at-cove `New(testCfg(), …)` client) to the end of the function body;
- rename `TestWritesRateLimitedOnlyForJam` → `TestWritesRateLimited` with comment `// F4: writes (do: comment, label, close) surface a rate limit as a\n// RateLimitError so the relay can wait it out.`, and delete its second half the same way.

Run: `go run golang.org/x/tools/cmd/goimports@latest -w internal/dispatch/githubissues/*_test.go && go test ./internal/dispatch/githubissues/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok` — every converted assertion describes today's `NewJam` (qualified keys, the dispatch gate, typed rate limits). They pin Jam's behaviour before Step 3 deletes the at-cove branches; Step 3 must keep them green. (This step has no red phase: it moves the suite off the constructor being deleted.)

- [ ] **Step 2: Convert the Linear tests to the narrow constructor (they must fail)**

In `internal/dispatch/linear/linear_test.go` replace `testCfg()` with

```go
func testCfg() kit.LinearTracker {
	return kit.LinearTracker{
		Team:             "AET",
		ClassLabelPrefix: "class:",
		States: kit.StateMap{
			Ready: "Todo", InProgress: "In Progress", InReview: "In Review",
			Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog",
		},
	}
}
```

and `/tmp/rmfunc.py internal/dispatch/linear/linear_test.go TestListInProgressParsesStartedAtAndSkipsUnparseable`. In `linear_integration_test.go` replace the `cfg := kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{ … }}}` literal with

```go
	cfg := kit.LinearTracker{
		Team: team, ClassLabelPrefix: "class:",
		States: kit.StateMap{Ready: "Todo", InProgress: "In Progress", InReview: "In Review", Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog"},
	}
```

In `internal/tracker/trackertest/linear.go` replace the `linear.New(kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{ … }}}, Token, l.HTTPClient())` call with

```go
	c, err := linear.New(kit.LinearTracker{
		Team: l.Team, ClassLabelPrefix: "class:", DispatchLabelPrefix: "dispatch:", States: LinearStates()}, Token, l.HTTPClient())
```

In `cmd/at-jam/tracker.go` replace

```go
	// linear.New wants a full kit.Config; wrap the configured LinearTracker.
	c, err := linear.New(kit.Config{Tracker: &kit.Tracker{Linear: d.Linear}}, token, httpc)
```

with `c, err := linear.New(*d.Linear, token, httpc)`.

Run: `go vet ./internal/dispatch/linear/ ./internal/tracker/... ./cmd/at-jam/`
Expected: FAIL — `cannot use testCfg() (value of struct type kit.LinearTracker) as kit.Config value in argument to New` (and the same in trackertest/tracker.go).

- [ ] **Step 3: Implement**

`internal/dispatch/linear/linear.go`: replace the `New` header through the dispatch-prefix comment with

```go
// New constructs a Client for the team lt names and resolves its state names
// to ids up front.
func New(lt kit.LinearTracker, token string, httpc *http.Client) (*Client, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	// Default the dispatch-label prefix: without it the prefix arrives empty
	// and the gate opens (every ready issue would be raised).
```

(the body from `dispatchPrefix := lt.DispatchLabelPrefix` on is unchanged); `/tmp/rmfunc.py internal/dispatch/linear/linear.go Client.ListInProgress`; delete the line `var _ scheduler.Tracker = (*Client)(nil)`; package comment → `// Package linear is a Linear team as Jam's tracker.Tracker: a small GraphQL\n// client over the Linear API. Live calls are exercised by the integration-tagged test.`; `// Client talks to one Linear team. It satisfies scheduler.Tracker.` → `… It satisfies tracker.Tracker.`.

`internal/dispatch/githubissues/githubissues.go`:
- replace everything from `// New resolves the target repo` to just before `// send issues a REST call` with

```go
// NewJam builds the Client Jam uses. Ticket keys are "owner/repo#N", the
// dispatch-label gate defaults to "dispatch:", the class prefix to "class:",
// and pull requests and already-closed issues are dropped from search hits.
// The repo is required and validated; an empty token is an error. No network
// call is made at construction — the lifecycle states are labels, not ids.
func NewJam(gt kit.GitHubTracker, token string, httpc *http.Client) (*Client, error) {
	repo := strings.TrimSpace(gt.Repo)
	if err := ticketkey.ValidRepo(repo); err != nil {
		return nil, fmt.Errorf("githubissues: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("githubissues: empty tracker token")
	}
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{
		http: httpc, token: token, repo: repo,
		prefix:         cmp.Or(gt.ClassLabelPrefix, "class:"),
		dispatchPrefix: cmp.Or(gt.DispatchLabelPrefix, "dispatch:"),
		labels: map[scheduler.Role]string{
			scheduler.RoleReady:      gt.States.Ready,
			scheduler.RoleInProgress: gt.States.InProgress,
			scheduler.RoleInReview:   gt.States.InReview,
			scheduler.RoleNeedsInput: gt.States.NeedsInput,
			scheduler.RoleBlocked:    gt.States.Blocked,
		},
	}, nil
}

```

- in `Client` replace the `dispatchPrefix` and `jam` field lines with `	dispatchPrefix string                    // dispatch label prefix: only issues with a label under it are dispatchable`;
- remove every `c.jam` branch, keeping the Jam side:
  - `do`: `if err == nil && c.jam {` → `if err == nil {`;
  - `getJSONLink`: unwrap `if c.jam { if rl := rateLimit(…); rl != nil { return "", rl } }` to the inner `if`;
  - `toIssue`: delete `if !c.jam {\n\t\tdispatchLabeled = false // at-cove has no dispatch gate and never set the flag\n\t}`;
  - `key`: `func (c *Client) key(n int) string { return ticketkey.GitHub(c.repo, n) }` with comment `// key is issue n's identifier: "owner/repo#N".`;
  - `search`: delete the leading `if !c.jam { … return out.Items, nil // at-cove's search results, unfiltered }` block;
  - `ListReady`: unwrap `if c.jam { …gate… }` to its body (comment `// The dispatch gate first: an issue not tagged for dispatch is never\n\t// raised, so its blockers are not worth a GET each.`);
- `/tmp/rmfunc.py internal/dispatch/githubissues/githubissues.go Client.ListInProgress Client.enteredAt`;
- replace `// Compile-time proof the client satisfies the full tracker interface.\nvar _ scheduler.Tracker = (*Client)(nil)\n\n// Jam's contract (via NewJam; at-cove's New satisfies it too, with "#N" keys).\nvar _ tracker.Tracker = (*Client)(nil)` with `// Jam's tracker contract.\nvar _ tracker.Tracker = (*Client)(nil)`;
- comments: package comment → `// Package githubissues is a GitHub repo's Issues as Jam's tracker.Tracker\n// (NewJam), at parity with internal/dispatch/linear. Lifecycle states are\n// status labels (status:ready, status:in-progress, …) and Done is an issue\n// being closed; blocker gating is a body convention (Depends on #N / Blocked\n// by #N, same-repo). Live calls are exercised by the integration-tagged test.`; `Client` → `// Client talks to one GitHub repo's Issues; it satisfies tracker.Tracker.`; `do`'s and `getJSON`'s comments drop their `at-cove's (New) keeps its plain http error.` clauses; `search`'s comment → `// search runs a GitHub issue search for the given status label in this repo,\n// scoped to open issues: 100 a page, following Link rel="next" (on the API\n// host only) for at most maxFeedPages pages, then dropping pull requests and\n// already-closed hits.`; `return text // New refuses an empty token; …` → `return text // NewJam refuses an empty token; …`.

`internal/dispatch/scheduler/scheduler.go`: delete `InProgressIssue`, `Tracker` and `Executor` with their comments (`goimports` then drops the `context`/`time` imports).

- [ ] **Step 4: Verify**

```bash
go run golang.org/x/tools/cmd/goimports@latest -w internal/dispatch cmd/at-jam/tracker.go internal/tracker/trackertest
gofmt -w internal/dispatch cmd/at-jam internal/tracker
go build ./... && go vet ./... && go vet -tags integration ./...
grep -rn 'c\.jam\|at-cove\|ListInProgress\|scheduler\.Tracker\|scheduler\.Executor\|InProgressIssue' internal/dispatch internal/tracker --include=*.go ; echo "grep-exit=$?"
grep -rn 'ListInProgress\|scheduler\.Tracker\|kit\.Config' cmd/at-jam --include=*.go ; echo "grep2-exit=$?"
go test ./internal/dispatch/... ./internal/tracker/... ./internal/dispatcher/ 2>&1 | tail -6
go test ./cmd/at-jam/ -run 'TestRuntimeRequisitionerParsed|TestParseRequisitionerGitHubBlock|TestValidateRequisitionerTrackerChoice|TestOpenTrackerPicksTheBlock|TestTicketerOnBothTrackers|TestRelayEndToEndBothTrackers|TestTrackerSurface' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
just test && just lint
```

Expected: build/vet clean; `grep-exit=1` and `grep2-exit=1`; every listed test PASS (Review Focus 4: both requisitioner blocks parse, open, ticket and relay through the narrowed constructors with `owner/repo#N` keys); `just test`/`just lint` exit 0.

- [ ] **Step 5: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "refactor: narrow the trackers to Jam's contract

githubissues.NewJam is the only constructor (owner/repo#N keys, the
dispatch gate, paged search, typed rate limits); at-cove's New and its
#N mode go. linear.New takes a kit.LinearTracker. ListInProgress and the
scheduler Tracker/Executor interfaces went with the engine.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: Prune the shared packages to what Jam reaches

Each symbol below was confirmed dead by `go run golang.org/x/tools/cmd/deadcode@latest ./...` on the tree after Task 5 (no main reaches it) and by a build after removal; `kit.Config` and its YAML parser turned out entirely dead — Jam reads only `kit.LinearTracker`/`GitHubTracker`/`StateMap`/`SecretConfig`/the MCP types, and `studio.StudioKit` deliberately never reused `kit.Config`. Rulings: `backend.DispatchOps.ScavengeLabeled` (the dispatch reaper) and the Go side of session egress (`SessionEgress`/`ApplySessionEgress`) go, but the **sealed** `apply-session-domains.sh` helper and the empty `allowed_domains.session.txt` stay in the hardening layer untouched (removing them is a hardening change, not this slice's); `connect`'s real-ssh tests that exercised `sshargs.Base` move to `internal/sshargs`.

**Files:**
- Delete: `internal/awake/`; `internal/connect/{connect.go,connect_test.go,teammate.go,teammate_test.go,specargs.go,specargs_test.go,transport.go,transport_test.go,integration_test.go}`; `internal/install/{install.go,install_test.go}`; `internal/kit/{discover.go,gitignore.go,imagesource.go,codec.go,codec_test.go,discover_test.go,gitignore_test.go,imagesource_test.go,jam_alias_test.go,modelspec_test.go,protected_env_test.go}`; `internal/backend/backend_test.go`; `internal/usersecret/{usersecret_test.go,plan_test.go}`
- Create: `internal/connect/vmwrite.go`, `internal/sshargs/integration_test.go`
- Rename: `internal/install/currency.go` → `identity.go`, `currency_test.go` → `identity_test.go`
- Modify: `internal/kit/{config.go,config_test.go}`, `internal/backend/backend.go`, `internal/backend/colima/{colima.go,dispatch.go,egress.go,*_test.go}`, `internal/assemble/{assemble.go,assemble_test.go,harness_layer_test.go,managed_settings_test.go}`, `internal/naming/{naming.go,naming_test.go}`, `internal/usersecret/{usersecret.go,plan.go,flat_test.go}`, `internal/sshargs/{sshargs.go,sshargs_test.go}`, `internal/connect/covemaster_test.go`, `internal/jam/launcher/{launcher.go,launcher_test.go}`, `justfile`, comment-only: `internal/harnessinstall/harnessinstall.go`, `internal/jam/modelspec/{modelspec.go,validate.go,claudeargs.go}`, `internal/studio/studiokit.go`, `internal/baseimage/resolve.go`, `internal/dispatch/linear/linear_test.go`

**Interfaces:**
- Consumes: `linear.New(kit.LinearTracker, …)` (Task 5) — so nothing needs `kit.Config` any more.
- Produces (what survives, unchanged signatures):
  - `kit`: `SecretConfig`, `LinearTracker`, `GitHubTracker`, `StateMap`, `IsReservedSecretName(string) bool`, and everything in `mcp.go` (`MCPServer`, `MCPReservedName`, `MCPServersImagePath`, `ValidateMCPServers`).
  - `connect`: `LaunchCoveMaster(r runner.Runner, o CoveMasterOptions) error`, `CoveMasterOptions`, `CoveMasterLogVMPath`; unexported `writeVM`, `shellQuote`.
  - `install`: `AtCoveIdentity() (string, error)`, `HashTree(fs.FS) (string, error)`.
  - `assemble`: `AssembleContext(buildDir string, pub []byte, egress Egress, gitlabHost string, mcpServers map[string]kit.MCPServer, harness harnessinstall.Install) error`, `Egress{Policy, Infra []string}`, `Revision`, `HardeningFS()`.
  - `backend`: `State` (+consts), `Endpoint`, `BaseSpec`, `Instance{Backend, Container, Image, ImageDigest string}`, `DispatchOps{RunEphemeral, Dial, RemoveContainer, Pause, Unpause}`, `Mount`, `VolumeOps`, `RoleEgress`, `SSHCA`, `KitImageBuilder`, `LoggedKitBuilder`.
  - `colima`: `NewWithContext(r runner.Runner, context string) *Colima` (the only constructor) and its methods for the interfaces above, `HasSysboxRuntime`, `DockerArgs`.
  - `naming`: `WorkspaceVolume`, `AgentDataVolume`, `DockerVolume`, `CoveContainer`.
  - `usersecret`: `LoadFlat`, `Store{Minters, Global, Credentials}`, `(Store).PlanFlat`, `MintExpander`, `Source`, `Minter` & co.
  - `sshargs`: `Target`, `Base(Target) []string`.

- [ ] **Step 1: Record the dead set (the "failing test" for this task)**

```bash
go run golang.org/x/tools/cmd/deadcode@latest ./... 2>/dev/null \
  | grep -E '^internal/(kit|connect|install|naming|assemble|backend|awake|usersecret|sshargs)/' | tee /tmp/dead-before.txt | wc -l
```

Expected: a non-empty list including `internal/kit/config.go:…: unreachable func: ParseConfig`, `internal/connect/connect.go:…: unreachable func: Connect`, `internal/install/install.go:…: unreachable func: Compile`, `internal/backend/backend.go:…: unreachable func: Get`, `internal/naming/naming.go:…: unreachable func: Container`. Step 9 requires it to be empty.

- [ ] **Step 2: connect, awake, sshargs**

```bash
git rm -q internal/connect/connect.go internal/connect/connect_test.go internal/connect/teammate.go internal/connect/teammate_test.go \
  internal/connect/specargs.go internal/connect/specargs_test.go internal/connect/transport.go internal/connect/transport_test.go \
  internal/connect/integration_test.go
git rm -rq internal/awake
cat > internal/connect/vmwrite.go <<'EOF'
// Package connect launches cove-master inside a raised studio over ssh
// (LaunchCoveMaster), staging its prompt, context and env into tmpfs.
package connect

import (
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// writeVM writes data to vmPath in the VM over ssh stdin (mode 077, never on
// argv), so secrets never touch argv or persistent disk.
func writeVM(r runner.Runner, tgt sshargs.Target, data, vmPath string) error {
	args := append(sshargs.Base(tgt), "umask 077; cat > "+vmPath)
	return r.RunStdin(strings.NewReader(data), "ssh", args...)
}

// shellQuote single-quotes s for safe inclusion in a POSIX shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
EOF
```

`internal/connect/covemaster_test.go` used `credsVMPath` from the deleted `connect.go`; add after its import block:

```go
// credsVMPath is claude's subscription OAuth credentials file inside a studio
// (CLAUDE_CONFIG_DIR=/agent-data); LaunchCoveMaster must never write it.
const credsVMPath = "/agent-data/.credentials.json"
```

`/tmp/rmfunc.py internal/sshargs/sshargs.go Interactive InteractiveSendEnv` and `/tmp/rmfunc.py internal/sshargs/sshargs_test.go TestInteractive TestInteractiveSendEnv`.

Create `internal/sshargs/integration_test.go`:

```go
//go:build integration

// Integration tests that drive the real ssh client against a throwaway sshd on
// loopback — no Docker, no network egress, no VM. They validate what the
// hermetic argv tests cannot: that Base actually drives OpenSSH and that
// known_hosts TOFU (accept-new) pins the host key. Moved here from
// internal/connect when at-cove's connect flow was deleted (jam-only slice 2).
//
// Run with:
//
//	go test -tags integration ./internal/sshargs/ -v
//
// Requires `ssh`, `sshd`, and `ssh-keygen` on PATH; tests Skip if absent or if
// a non-root sshd cannot start in this environment.
package sshargs

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSSHD boots an unprivileged sshd on a free loopback port configured for
// key-only auth and returns a Target pointing the ssh client at it.
func startSSHD(t *testing.T) Target {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
	}
	if _, err := os.Stat(sshd); err != nil {
		t.Skipf("sshd not available: %v", err)
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh client not available: %v", err)
	}
	dir := t.TempDir()
	genKey(t, filepath.Join(dir, "hostkey"))
	genKey(t, filepath.Join(dir, "clientkey"))
	pub, err := os.ReadFile(filepath.Join(dir, "clientkey.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	port := freePort(t)
	cfg := filepath.Join(dir, "sshd_config")
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
PasswordAuthentication no
PubkeyAuthentication yes
UsePAM no
StrictModes no
`, port, filepath.Join(dir, "hostkey"), filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"))
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-D", "-f", cfg, "-e")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	waitPort(t, port)
	return Target{
		Host:           "127.0.0.1",
		User:           u.Username,
		Port:           port,
		IdentityFile:   filepath.Join(dir, "clientkey"),
		KnownHostsFile: filepath.Join(dir, "known_hosts"),
	}
}

func genKey(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-N", "", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen failed: %v\n%s", err, out)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sshd did not come up on %s", addr)
}

func TestRealSSHBaseConnects(t *testing.T) {
	tgt := startSSHD(t)
	out, err := exec.Command("ssh", append(Base(tgt), "echo", "cove-ok")...).Output()
	if err != nil {
		t.Fatalf("ssh connect via Base failed: %v", err)
	}
	if !strings.Contains(string(out), "cove-ok") {
		t.Fatalf("unexpected remote output: %q", out)
	}
}

func TestRealSSHTOFUWritesKnownHosts(t *testing.T) {
	tgt := startSSHD(t)
	if err := exec.Command("ssh", append(Base(tgt), "true")...).Run(); err != nil {
		t.Fatalf("ssh connect failed: %v", err)
	}
	b, err := os.ReadFile(tgt.KnownHostsFile)
	if err != nil {
		t.Fatalf("accept-new did not write known_hosts: %v", err)
	}
	if !strings.Contains(string(b), "ssh-ed25519") {
		t.Fatalf("known_hosts has no pinned host key:\n%s", b)
	}
}
```

`justfile`: the `integration` recipe → `# real-ssh integration tests of the ssh argv builders (needs ssh/sshd/ssh-keygen)` / `go test -tags integration ./internal/sshargs/ -v`.

Run: `gofmt -w internal/connect internal/sshargs && go vet ./internal/connect/ ./internal/sshargs/ && go test ./internal/connect/ && go test -tags integration ./internal/sshargs/ -v 2>&1 | grep -E '^(--- |ok)'`
Expected: PASS (both real-ssh tests PASS where sshd is available, SKIP otherwise).

- [ ] **Step 3: install keeps only the assembly identity**

```bash
git rm -q internal/install/install.go internal/install/install_test.go
git mv internal/install/currency.go internal/install/identity.go
git mv internal/install/currency_test.go internal/install/identity_test.go
/tmp/rmfunc.py internal/install/identity.go CurrencyHash HarnessIdentity KitSourceTree
/tmp/rmfunc.py internal/install/identity_test.go TestCurrencyHashDeterministicAndFieldSensitive TestCurrencyHashUnambiguousFieldBoundaries TestKitSourceTreeHashesConfigAndImageTree TestKitSourceTreeErrorsWithoutConfig TestAtCoveIdentityExcludesDefaultHarnessData
```

In `internal/install/identity.go` delete the `CurrencyInputs` type with its comment, put this package comment above `package install`:

```go
// Package install computes the assembly identity: the digest of the sealed
// hardening layer, harness payload and embedded cove-master that keys every
// studio image tag (AtCoveIdentity).
```

and replace `AtCoveIdentity`'s comment with

```go
// AtCoveIdentity hashes the binary's embedded build contributions: the sealed
// hardening layer (and assemble.Revision), the harness layer's payload (its
// scripts and the managed settings' sandbox policy) and its rendered baseline
// preferences — NOT any install's version/plugins, which are per-image (the
// studio build-digest) — and the embedded cove-master binaries. An upgrade that
// changes any of these flips the digest, so every studio image tag changes
// (internal/jam/launcher asm.go keys the tag on it).
```

- [ ] **Step 4: kit keeps only Jam's types**

```bash
git rm -q internal/kit/discover.go internal/kit/gitignore.go internal/kit/imagesource.go internal/kit/codec.go \
  internal/kit/codec_test.go internal/kit/discover_test.go internal/kit/gitignore_test.go internal/kit/imagesource_test.go \
  internal/kit/jam_alias_test.go internal/kit/modelspec_test.go internal/kit/protected_env_test.go
```

Replace `internal/kit/config.go` with:

```go
// Package kit holds the kit-facing types Jam shares across packages: a studio
// kit's secret demands (SecretConfig, IsReservedSecretName), its MCP servers
// (mcp.go), and the tracker blocks Jam's runtime.requisitioner reads
// (LinearTracker, GitHubTracker, StateMap).
package kit

// SecretConfig is a kit's *demand* for a secret (keyed by its env var name in a
// secrets map): a name plus a human description of its purpose. How the value is
// produced is a machine-side concern (see internal/usersecret) — a kit never
// carries a resolver command.
type SecretConfig struct {
	Description string `yaml:"description" json:"description"`
}

// LinearTracker wires the Requisitioner to one Linear team.
type LinearTracker struct {
	Team             string `yaml:"team"`
	PollInterval     string `yaml:"poll-interval"`
	ClassLabelPrefix string `yaml:"class-label-prefix"`
	// DispatchLabelPrefix gates which ready issues the Requisitioner
	// raises a cove for: only issues carrying a label with this prefix. Defaults
	// to "dispatch:" (presence-only; the value after the prefix is unused).
	DispatchLabelPrefix string   `yaml:"dispatch-label-prefix"`
	States              StateMap `yaml:"states"`
}

// GitHubTracker wires the Requisitioner to one GitHub repo's Issues. Unlike
// Linear, Done is not a lifecycle role — a closed issue IS done — so States
// reuses StateMap but only the five non-terminal roles (ready, in-progress,
// in-review, needs-input, blocked) are required; States.Done is ignored.
type GitHubTracker struct {
	Repo             string `yaml:"repo,omitempty"` // "owner/name"; required (githubissues.NewJam validates it)
	PollInterval     string `yaml:"poll-interval"`
	ClassLabelPrefix string `yaml:"class-label-prefix"`
	// DispatchLabelPrefix gates which ready issues the Requisitioner raises a
	// studio for (presence-only; default "dispatch:", applied by
	// githubissues.NewJam).
	DispatchLabelPrefix string   `yaml:"dispatch-label-prefix"`
	States              StateMap `yaml:"states"`
}

// StateMap binds the lifecycle roles to a tracker's real state names (Linear)
// or status labels (GitHub).
type StateMap struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in-progress"`
	InReview   string `yaml:"in-review"`
	Done       string `yaml:"done"`
	NeedsInput string `yaml:"needs-input"`
	Blocked    string `yaml:"blocked"`
}

// reservedSecretNames are well-known subsystem secret names a studio kit may
// not declare as a general secret demand (internal/studio rejects them).
var reservedSecretNames = map[string]bool{
	"AT_TASK_GIT_TOKEN":                   true,
	"AT_DISPATCH_TRACKER_TOKEN":           true,
	"AT_DISPATCH_WEBHOOK_SECRET":          true,
	"GOOGLE_APPLICATION_CREDENTIALS_JSON": true,
}

// IsReservedSecretName reports whether n is a reserved subsystem secret name.
// Exported so other packages (internal/studio build-arg validation) can reject
// collisions without duplicating the set.
func IsReservedSecretName(n string) bool { return reservedSecretNames[n] }
```

(The field names and YAML tags of `LinearTracker`, `GitHubTracker` and `StateMap` are byte-identical to today's — Review Focus 4. The reserved set is unchanged; whether those at-cove-era names should stay reserved is a slice-3 question, noted in Task 7's follow-ups.)

Replace `internal/kit/config_test.go` with the one surviving test, kept verbatim:

```bash
{ printf 'package kit\n\nimport "testing"\n\n'; sed -n '/^func TestIsReservedSecretName/,/^}/p' internal/kit/config_test.go; } > /tmp/kit_config_test.go && mv /tmp/kit_config_test.go internal/kit/config_test.go
```

- [ ] **Step 5: backend and colima keep the launcher's surfaces**

`internal/backend/backend.go`: delete `WorkspaceMode` (+ its `Isolated`/`Shared` consts), `WorkspaceMount`, `CreateContext`, `InstallContext`, `InstalledImage`, `VolumeSet`, the `Backend` interface, `SessionEgress`, `Factory`, `registry`, and (`/tmp/rmfunc.py internal/backend/backend.go Register Get`) `Register`/`Get`, each with its doc comment; in `Instance` delete the `Workspace` and `Volumes` fields; in `DispatchOps` delete the `ScavengeLabeled` method and its two comment lines. Replace these doc comments:

```go
// Package backend defines the docker-backend surfaces Jam's launcher drives:
// run, dial, pause and remove studio containers, their named volumes, their
// egress and SSH CA, and the managed-kit image build.
```

```go
// Instance identifies a studio container RunEphemeral started.
```

```go
// DispatchOps is the ephemeral-container surface Jam's launcher raises studios
// with: a labeled --rm container from a built image (pinned by digest when
// known), dialled over its published sshd, paused when idle, removed at
// teardown. There is no build op here — KitImageBuilder builds the image.
```

```go
// VolumeOps manages labeled named volumes — a standing session's state
// volumes (COV-249). Kept its own interface so DispatchOps doesn't grow it.
```

and in `KitImageBuilder`'s comment replace `it and the persistent Backend/DispatchOps surfaces don't grow build methods.\n//\n// Distinct from Backend.Install (the at-cove single gated build site, COV-38):\n// this builds an already-assembled context FROM a base the caller has already\n// resolved and gated (at-cove install's manifest BaseRef), so it reuses that\n// blessed base rather than running the provenance gate again.` with `it and DispatchOps doesn't grow build methods. It builds an\n// already-assembled context FROM a base the caller has already resolved and\n// gated (ResolveKitBase*), so it never runs the provenance gate again.`

`internal/backend/colima/`:

```bash
/tmp/rmfunc.py internal/backend/colima/colima.go init New Colima.Install Colima.Create Colima.Destroy Colima.RemoveImage Colima.dockerBuild shadowArgs nonEmpty
/tmp/rmfunc.py internal/backend/colima/dispatch.go Colima.ScavengeLabeled
/tmp/rmfunc.py internal/backend/colima/egress.go Colima.ApplySessionEgress
git rm -q internal/backend/backend_test.go
```

In `egress.go` delete `// Compile-time proof colima delivers session egress (COV-39 §5). One concrete\n// backend serves both the ephemeral (work/dispatch) and persistent (chat) paths.\nvar _ backend.SessionEgress = (*Colima)(nil)` and the `sessionDomainsHelper` const with its comment. In `colima.go` change the two comments and the error text that say `at-cove detects but never installs Sysbox` / `at-cove preflight` / `at-cove detects but does not install it` to `the launcher detects but never installs Sysbox` / `the launcher's preflight` / `Jam detects but does not install it`; in `dispatch.go` `(at-cove does not install it) — COV-117.` → `(Jam does not install it) — COV-117.`.

Colima's tests constructed through the deleted `New`; switch them to the real constructor and drop the tests of deleted methods:

```bash
cd internal/backend/colima
sed -i -E 's/\bNew\(([a-z0-9]+)\)\.\(\*Colima\)/NewWithContext(\1, "")/g; s/\bNew\(([a-z0-9]+)\)\.\(backend\.KitImageBuilder\)/NewWithContext(\1, "")/g; s/\bNew\(([a-z0-9]+|&runner\.Fake\{\}|mkFail\(\))\)\./NewWithContext(\1, "")./g' *_test.go
cd -
/tmp/rmfunc.py internal/backend/colima/colima_test.go TestInstallBuildsGatesTags TestInstallNoCacheThreadsToBuildArgv TestInstallCapturesBuiltImageDigest TestCreatePinsDigest TestInstallPreflightFailsActionably TestCreateIsolated TestCreateDNS TestCreateExtraHosts TestCreateDocker TestCreateNoDockerByteForByte TestCreateDockerPreflightRequiresSysbox TestDestroyPurgesDockerVolume TestCreateShared TestCreateRecordsVolumeNames TestCreateSharedShadowDirs TestCreateSharedNoShadowDirs TestDestroyRemovesShadowVolumes TestDestroyUsesRecordedVolumeNames TestDestroyFallsBackToContainerDerivedVolumes TestRegistered TestDestroyKeepsVolumes TestDestroyPurgesVolumes TestRemoveImage TestRemoveImagePreflightFailsActionably TestCreateRunsGivenImageContainerFromName
/tmp/rmfunc.py internal/backend/colima/baseimage_test.go TestInstallInjectsBaseBuildArg
/tmp/rmfunc.py internal/backend/colima/dispatch_test.go TestScavengeLabeledRemovesOldOnly
/tmp/rmfunc.py internal/backend/colima/egress_test.go TestApplySessionEgressExecsHelperWithStdin TestApplySessionEgressEmptyClears
grep -n '\bNew(\|\.Create(\|\.Destroy(\|\.Install(\|RemoveImage\|ScavengeLabeled\|ApplySessionEgress\|CreateContext\|InstallContext' internal/backend/colima/*_test.go | grep -v errors.New
```

The last grep must print exactly three lines — one in `TestPinsContext` (`….Create(backend.CreateContext{Name: "box", …`) and two in `TestPreflightFailsActionably` (its `Create` and `Destroy` calls); rewrite them onto surviving methods:
- `TestPinsContext`: the call becomes `if _, err := NewWithContext(f, "").RunEphemeral("img:tag", "", "box", "harbor.cove", nil, nil, false); err != nil {`;
- `TestPreflightFailsActionably`: replace the `// Create` block with

```go
	// RunEphemeral
	if _, err := NewWithContext(mkFail(), "").RunEphemeral("img:tag", "", "box", "harbor.cove", nil, nil, false); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("RunEphemeral should fail actionably; err=%v", err)
	}
```

and the `// Destroy` block with

```go
	// RemoveContainer
	if err := NewWithContext(mkFail(), "").RemoveContainer("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("RemoveContainer should fail actionably; err=%v", err)
	}
```

`internal/jam/launcher/launcher.go`: the `Backend` interface comment `backend.DispatchOps     // RunEphemeral, Dial, RemoveContainer, ScavengeLabeled` → `backend.DispatchOps     // RunEphemeral, Dial, RemoveContainer, Pause, Unpause`; `stateVolumes`' comment `the\n// at-cove create path's <container>-agent-data and <container>-workspace.` → `\n// <container>-agent-data and <container>-workspace.`. `internal/jam/launcher/launcher_test.go`: `/tmp/rmfunc.py internal/jam/launcher/launcher_test.go fakeOps.ScavengeLabeled`.

- [ ] **Step 6: assemble keeps only `AssembleContext`**

```bash
/tmp/rmfunc.py internal/assemble/assemble.go EgressFor HarnessFor Assemble
/tmp/rmfunc.py internal/assemble/assemble_test.go TestAssembleEnsuresGitignore TestAssemble_VertexDomainsBaked TestAssembleEgressUnionUnchanged
/tmp/rmfunc.py internal/assemble/harness_layer_test.go TestAssembleUsesKitModelSpecHarness
perl -0pi -e 's/\bAssemble\(([^,()]+(?:\([^()]*\))?), (buildDir|filepath\.Join\(kitDir, "\.build"\)), ([^\n]*?), (Egress\{[^\n]*?\}|eg), ("[^"]*"), harnessinstall\.Default\(\)\)/AssembleContext($2, $3, $4, $5, nil, harnessinstall.Default())/g' internal/assemble/*_test.go
grep -n '\bAssemble(' internal/assemble/*_test.go ; echo "grep-exit=$?"
```

Expected: `grep-exit=1` (every remaining call now goes through `AssembleContext`, the function Jam's launcher calls). Then:
- in `TestAssembleAllowedDomains` replace `img := kit.ImageConfig{AllowedDomains: []string{".example.com", "pkg.go.dev"}}` and the next line's `Egress{Policy: img.AllowedDomains}` with a single call taking `Egress{Policy: []string{".example.com", "pkg.go.dev"}}`;
- delete each `kitDir := t.TempDir()` that `go vet ./internal/assemble/` reports as `declared and not used` (two today);
- `Egress`'s comment: `// Infra is kit.InfraDomains (provider, self-hosted GitLab, Jam hosts): always\n// on, baked into allowed_domains.infra.txt.` → `// Infra is the always-on infrastructure list (Jam's launcher passes its\n// jam-host), baked into allowed_domains.infra.txt.`, and `Policy is image.allowed-domains:` → `Policy is the kit's egress list:`.

- [ ] **Step 7: naming and usersecret**

```bash
/tmp/rmfunc.py internal/naming/naming.go instance Image Container WorkerContainer ShadowVolume SanitizeShadowDir
/tmp/rmfunc.py internal/naming/naming_test.go TestImage TestContainer TestWorkerContainer TestShadowVolume TestSanitizeShadowDir TestVolumes
```

In `internal/naming/naming.go` replace the package comment with

```go
// Package naming is the single source of truth for the runtime docker
// resource names Jam's launcher derives: a studio's container
// (atcove-cove-<id>, CoveContainer) and the volumes named after it. Every name
// shares the product prefix `atcove-`, so it never collides with an unrelated
// docker object and every resource of one studio sorts together. Pure (no
// os/time), so it is hermetically testable.
```

In `internal/naming/naming_test.go` add, and rewrite `TestDockerVolume`'s body onto `CoveContainer`:

```go
func TestVolumes(t *testing.T) {
	c := CoveContainer("abc")
	if got := WorkspaceVolume(c); got != "atcove-cove-abc-workspace" {
		t.Fatalf("WorkspaceVolume = %q, want atcove-cove-abc-workspace", got)
	}
	if got := AgentDataVolume(c); got != "atcove-cove-abc-agent-data" {
		t.Fatalf("AgentDataVolume = %q, want atcove-cove-abc-agent-data", got)
	}
}
```

```go
	if got := DockerVolume(CoveContainer("abc")); got != "atcove-cove-abc-docker" {
		t.Fatalf("DockerVolume = %q, want atcove-cove-abc-docker", got)
	}
```

`internal/usersecret` — at-cove's two-file, per-kit supply (`secrets.yml`/`secrets.local.yml`, `Load`/`Plan`) goes; Jam's flat `credentials.yml` (`LoadFlat`/`PlanFlat`) stays:

```bash
/tmp/rmfunc.py internal/usersecret/usersecret.go Load Store.validate
/tmp/rmfunc.py internal/usersecret/plan.go Store.Plan Store.lookup
git rm -q internal/usersecret/usersecret_test.go internal/usersecret/plan_test.go
```

In `usersecret.go`: package comment → `// Package usersecret loads Jam's host-side credential supply file\n// (~/.config/at-jam/credentials.yml, LoadFlat): minters:/global: libraries plus\n// a flat credentials: map, resolved to secret.Specs by PlanFlat.`; `Store` →

```go
// Store is the parsed supply. Minters and Global are inert libraries: reachable
// only through an explicit mint:/global: reference from a credentials entry.
type Store struct {
	Minters     map[string]Minter
	Global      map[string]Source
	Credentials map[string]Source // credentials.yml: name -> source
}
```

`// file is the on-disk shape of each secrets file.` → `// file is the on-disk shape of the supply file. Kits is the retired at-cove\n// per-kit section, still accepted (and ignored) so an older file keeps loading.` (keep the `Kits` field: removing it would make `KnownFields` reject an existing file); in `LoadFlat`'s comment delete `Unlike Load there is no kit partitioning and no .local file.`; `checkSources`' comment ends at `resolves.` (drop `Shared by validateFlat and the kit-partitioned validate.`). Keep the global-delegation rule covered — append to `internal/usersecret/flat_test.go`:

```go
func TestPlanFlat_GlobalMustBeTerminal(t *testing.T) {
	// A global: delegation whose target is itself a global (or mint) is a hard error,
	// not a silent chain — the delegated supply must be a terminal value/command.
	st := Store{
		Global:      map[string]Source{"g1": {Global: "g2"}, "g2": {Command: []string{"x"}}},
		Credentials: map[string]Source{"T": {Global: "g1"}},
	}
	if _, _, err := st.PlanFlat([]string{"T"}, nil); err == nil {
		t.Fatal("a global pointing at another global must error")
	}
}
```

- [ ] **Step 8: Comments that name deleted symbols**

| File | Replace | With |
|---|---|---|
| `internal/harnessinstall/harnessinstall.go` | `// Default is the install of claude-default: the harness a full config.yml\n// kit without a model-spec: block gets (assemble.HarnessFor), and the one a\n// raise uses when its role delivers no spec.` | `// Default is the install of claude-default: the one a raise uses when its\n// role delivers no spec.` |
| `internal/jam/modelspec/modelspec.go` | `// A plain at-cove kit may omit it (kit.Config.EffectiveModelSpec defaults\n\t// it to DefaultClaudeVersion); Jam requires it.` | `// Jam requires it.` |
| `internal/jam/modelspec/validate.go` | `// variable a model-spec's provider-env must never set. One list, shared by Jam\n// and the kit loader (kit.ProtectedEnvKey delegates here).` | `// variable a model-spec's provider-env must never set.` |
| `internal/jam/modelspec/validate.go` | `// cove-side harness (internal/agentrun) and a plain at-cove session\n// (kit.Config.SessionEnv) both use:` | `// cove-side harness (internal/agentrun) uses:` |
| `internal/jam/modelspec/claudeargs.go` | `// launcher — Jam's headless harness (internal/agentrun), plain at-cove's\n// interactive chat (internal/connect) and its dispatched workers\n// (internal/dispatchrun) — so a spec means the same flags everywhere.` | `// launcher (today Jam's headless harness, internal/agentrun) — so a spec\n// means the same flags everywhere.` |
| `internal/studio/studiokit.go` | `It deliberately does NOT\n// reuse kit.Config: a studio kit carries only` | `A studio kit carries only` |
| `internal/studio/studiokit.go` | `// from a full kit.Config entry sharing the jam kit registry.` | `// in the jam kit registry.` |
| `internal/baseimage/resolve.go` | `(mutual exclusion of the first two is\n// enforced earlier, in kit.ValidateImageSource).` | `(the caller keeps the first two mutually\n// exclusive).` |
| `internal/dispatch/linear/linear_test.go` | `// which wraps a bare LinearTracker and bypasses kit.ParseConfig's defaulter.` | `// which hands linear.New a bare LinearTracker.` |

- [ ] **Step 9: Verify**

```bash
go run golang.org/x/tools/cmd/goimports@latest -w internal/kit internal/backend internal/assemble internal/naming internal/usersecret internal/install internal/connect internal/sshargs internal/jam/launcher
gofmt -w internal cmd
go build ./... && go vet ./... && go vet -tags integration ./...
go run golang.org/x/tools/cmd/deadcode@latest ./... 2>/dev/null | grep -E '^internal/(kit|connect|install|naming|assemble|backend|awake|usersecret|sshargs)/' ; echo "dead-exit=$?"
go test ./internal/... ./cmd/... 2>&1 | grep -v '^ok\|no test files'
go test ./cmd/at-jam/ -run 'TestRuntimeRequisitionerParsed|TestParseRequisitionerGitHubBlock|TestOpenTrackerPicksTheBlock' -v 2>&1 | grep -E '^(--- |ok)'
go test ./internal/assemble/ -run 'TestAssembleContextStagesOnlyCoveMaster|Embed|Nftables|Squid|Egress|Hardening|Ceiling|Harness|ManagedSettings' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
just test && just lint
```

Expected: build/vet clean; `dead-exit=1` (nothing unreachable left in the pruned packages); the full-suite grep prints nothing; the named tests PASS; `just test`/`just lint` exit 0.

- [ ] **Step 10: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "refactor: prune shared packages to what Jam reaches

deadcode-confirmed: kit.Config and its parser, connect's chat/teammate
flow, awake, install's manifest and currency, backend.Backend and the
colima Install/Create/Destroy/RemoveImage/ScavengeLabeled/session
egress, assemble.Assemble/EgressFor/HarnessFor, naming's container and
image helpers, usersecret's per-kit supply. The sealed hardening layer
is untouched; connect's real-ssh Base tests move to sshargs.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 7: Docs — delete the deleted things' docs; leave every remaining doc accurate

Rulings: `docs/orchestration/` is deleted whole — its workflow design is not true of Jam's Requisitioner (Jam ticket sessions report and open their own PRs; there is no at-task bracket or brokering scheduler), and what *is* true (dispatch label gate, class labels, `Depends on #N` blockers, lifecycle states) is already owned by [requisitioner.md](../../usage/jam/requisitioner.md). `docs/usage/observability.md` is deleted too: it documents at-cove/at-task's attended/unattended log modes and `run`-id correlation, none of which Jam has. The value/command/global/mint **source vocabulary** and the `minters:` schema, owned by the deleted `at-cove-secrets.md`, move to [jam/credentials.md](../../usage/jam/credentials.md) (one owner). That doc's `minters:` example was already invalid (`anthropic: audience:` instead of `anthropic: oidc: auth0: …`); a new doc-example test, replacing the deleted `runbook_example_test.go`, pins it.

**Files:**
- Delete: `docs/usage/{at-cove-config,at-cove-secrets,discord-teammate,workspace-visibility,at-task,at-task-inputs,at-task-output,observability}.md`, `docs/orchestration/`
- Create: `internal/usersecret/doc_example_test.go`
- Modify: `docs/INDEX.md`, `docs/usage/INDEX.md`, `docs/OVERVIEW.md`, `docs/DEVELOPMENT.md`, `docs/usage/at-mint.md`, `docs/usage/docker-in-sandbox.md`, `docs/usage/jam/{INDEX,connector,credentials,dispatch-runbook,kits,model-spec-harness,model-specs,renamed-from-harbor,roster,serve,session-context,session-events,standing-state,studio-kit-migration,vertex}.md`, `AGENTS.md`, `README.md`, `dev/jam.dev.yml.example`

**Interfaces:**
- Consumes: the code state after Task 6 (what the docs must describe).
- Produces: no doc links to a deleted doc or a deleted OVERVIEW section; `credentials.md` owns the source vocabulary (anchors `#sources`, `#minters`).

- [ ] **Step 1: Capture the audit baseline**

```bash
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs 2>&1 | grep '^ERROR' | grep -v '^ERROR superpowers/' | sort > /tmp/audit-before.txt
cat /tmp/audit-before.txt
```

Expected: the four pre-existing OVERVIEW.md anchor errors (`…sysbox-docker-in-sandbox-design.md#d-init-model--daemon-lifecycle`, `…monolithic-release-pipeline-design.md#4-blessing-the-low-watermark--the-registry`, and `DEVELOPMENT.md#ci--the-release-pipeline` twice).

- [ ] **Step 2: Write the credentials doc-example test (it must fail)**

Create `internal/usersecret/doc_example_test.go`:

```go
package usersecret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCredentialsDocExampleLoads guards against doc drift: every ```yaml block
// in docs/usage/jam/credentials.md that is a credentials.yml example (it has
// both minters: and credentials:) must parse through LoadFlat.
func TestCredentialsDocExampleLoads(t *testing.T) {
	data, err := os.ReadFile("../../docs/usage/jam/credentials.md")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, b := range yamlBlocks(string(data)) {
		if !strings.Contains(b, "minters:") || !strings.Contains(b, "credentials:") {
			continue // e.g. the serve config's name-only demand list
		}
		found++
		p := filepath.Join(t.TempDir(), "credentials.yml")
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFlat(p); err != nil {
			t.Errorf("credentials.md yaml block %d does not load: %v\n%s", i, err, b)
		}
	}
	if found == 0 {
		t.Fatal("no credentials.yml example (a yaml block with minters: and credentials:) in credentials.md")
	}
}

// yamlBlocks returns the bodies of the ```yaml fenced blocks in md.
func yamlBlocks(md string) []string {
	var out, cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```yaml":
			in, cur = true, nil
		case in && strings.TrimSpace(line) == "```":
			in = false
			out = append(out, strings.Join(cur, "\n")+"\n")
		case in:
			cur = append(cur, line)
		}
	}
	return out
}
```

Run: `go test ./internal/usersecret/ -run TestCredentialsDocExampleLoads`
Expected: FAIL — `credentials.md yaml block 0 does not load: … unmarshal errors` (`field audience not found in type usersecret.AnthropicMinter`).

- [ ] **Step 3: Delete the docs**

```bash
git rm -q docs/usage/at-cove-config.md docs/usage/at-cove-secrets.md docs/usage/discord-teammate.md \
  docs/usage/workspace-visibility.md docs/usage/at-task.md docs/usage/at-task-inputs.md docs/usage/at-task-output.md \
  docs/usage/observability.md
git rm -rq docs/orchestration
```

- [ ] **Step 4: `credentials.md` takes over the source vocabulary**

In `docs/usage/jam/credentials.md`:
- frontmatter `owns:` → `the at-jam credentials-file format and location, the value/command/global/mint source vocabulary and the minters: library, the `exchange: gcp` demand setting, the demand/supply split (serve config demands by name, the file supplies), which references must name a demanded credential, and fail-closed on an unsupplied demand`; `prereqs:` → `serve.md for the serve config that demands credentials`; `updated:` → `2026-10-26`;
- replace the intro paragraph (`Jam's real secrets live in one **protected file**, …` through `… single service).`) with

```markdown
Jam's real secrets live in one **protected file**, never in the serve config:
the serve config only **demands** credentials by name, and this file **supplies**
them, each from one of four [sources](#sources) — optionally through a
[minter](#minters).
```

- in the `## Format` example replace the `minters:` entry with a valid profile:

```yaml
minters:                       # inert library; reached only via a mint: reference
  anthropic-fed:
    anthropic:
      oidc:
        auth0:
          tenant: tenant.us.auth0.com
          client-id: jam-minter
          audience: "https://jam.example.com/anthropic"
          client-secret: { command: ["cat", "/run/secrets/auth0-client-secret"] }
      federation: { org: "<org-id>", rule: fdrl_example, service-account: svac_example }
```

- insert after the paragraph that ends `… until the serve\nconfig demands its name.`:

```markdown
## Sources

| Source | Form | Resolves to |
|---|---|---|
| `value:` | a literal string | that string, verbatim |
| `command:` | a host **argv array** | stdout of running it on the host, trailing newline trimmed |
| `global: <name>` | a name | `global[<name>]`, which must itself be a `value` or `command` (a chain is an error) |
| `mint: <name>` | a name | a token minted by `at-mint`, built from `minters[<name>]` ([below](#minters)) |

`global:` and `minters:` are **never matched by demand name**: a demand called
`anthropic-key` does not pick up a library entry of the same name — only a
`credentials:` entry you wrote reaches one.

## Minters

A `minters:` profile is a structured identity for [`at-mint`](../at-mint.md),
exactly one of:

- `github:` — `app-id`, `install-id`, `app-key` (a `value:` is a **path** to the
  PEM, passed as `--app-key-file`; a `command:`/`global:` supplies the key
  **contents**, passed as `AT_MINT_GITHUB_APP_KEY`).
- `anthropic:` — `oidc.auth0` (`tenant`, `client-id`, `audience`,
  `client-secret` — a source, passed as `AT_MINT_AUTH0_CLIENT_SECRET`) and
  `federation` (`org`, `rule`, `service-account`, optional `workspace`).

Resolving `{ mint: <profile> }` runs `at-mint <provider>` with the non-secret
fields as flags and the secret ones resolved on the host and passed as env —
never argv. Jam passes no `--repo`, which `at-mint github` requires, so a
`github` profile cannot mint through `mint:`: write the invocation as a
`command:` instead (`["at-mint", "github", "--app-id", "…", "--install-id", "…",
"--app-key-file", "/path/key.pem", "--repo", "owner/name"]`).
```

Run: `go test ./internal/usersecret/ -run TestCredentialsDocExampleLoads`
Expected: PASS.

- [ ] **Step 5: `at-mint.md` describes Jam's supply**

```text
In `docs/usage/at-mint.md`:
- frontmatter `prereqs:` → `jam/credentials.md — how a credential is supplied by a mint: profile or a command: that invokes at-mint`; `updated:` → `2026-10-26`;
- the intro's `credential and prints it to stdout, for use as a secret's `command:` resolver\n(see [at-cove-secrets.md](at-cove-secrets.md)). It never runs inside a\nsandbox — it runs on the host, invoked by `at-cove` when resolving a demand.` → `credential and prints it to stdout, for use as a credential's `command:`\nsource (see [jam/credentials.md](jam/credentials.md#sources)). It never runs\ninside a studio — it runs on the host, invoked by `at-jam` when resolving a\ndemand.`;
- `Every `--flag` value (App id, tenant, org, rule\n  id, a key *path*, …) is safe to write in `secrets.yml` or a shell history.` → `… is safe to write in `credentials.yml` or a shell history.`;
- `is required (the `mint:` expander supplies it from the kit's\n`source-control.github.project`; a bare `command:` passes it explicitly)` → `is required (Jam's `mint:` expander supplies none, so pass it in a\n`command:` — [jam/credentials.md](jam/credentials.md#minters))`;
- `When run as an at-cove worker secret supply (`COVE_RUN_TIMEOUT` is set), it also\n**fails closed if `expires_in` is shorter than the run timeout** — a bearer that\nwould expire mid-run is caught before any VM is built, rather than surfacing\nlater as an opaque agent `401`.` → `If `COVE_RUN_TIMEOUT` is set in its environment (nothing in Jam sets it), it\nalso **fails closed if `expires_in` is shorter than that timeout**.`;
- replace the whole `## Using it as a secret supply` section with
```

```markdown
## Using it as a credential supply

Normally you don't invoke `at-mint` yourself: Jam's
[credentials file](jam/credentials.md#minters) names a `minters:` profile once and
a `{ mint: <profile> }` credential runs `at-mint` from it on every resolve, or a
`command:` spells the invocation out. `AT_MINT_GITHUB_APP_KEY` /
`AT_MINT_AUTH0_CLIENT_SECRET` come from the resolving process's own environment
or from the profile's `command:`/`global:` field — never from argv.
```

- [ ] **Step 6: Indexes**

Replace the body of `docs/usage/INDEX.md` (keep the frontmatter, set `summary:` to `Section index for the usage/reference docs of the binaries this repo ships — Jam (at-jam), the at-mint token minter, and docker-in-sandbox.`, `read_when:` to `You are running at-jam or at-mint directly (not developing them) and need its command surface, configuration, or I/O contract.`, `updated: 2026-10-26`) with:

```markdown
# Usage reference

Operator-facing usage for the binaries this repo ships. For *why* it is built
this way, see [`../OVERVIEW.md`](../OVERVIEW.md).

| Doc | What it covers | Read when |
|-----|----------------|-----------|
| [jam/](jam/INDEX.md) | Operating the `at-jam` central service — running it (`serve`, the broker, destinations, TLS), signing operators in, the roster/RBAC, the kit registry, studios, the Requisitioner and the intercom. A section index. | You are running or administering a Jam. |
| [at-mint.md](at-mint.md) | The `at-mint` host-side token minter — the `github` and `anthropic` subcommands, their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract. | You are supplying a credential that mints a token — a GitHub App installation token or an Anthropic WIF bearer. |
| [docker-in-sandbox.md](docker-in-sandbox.md) | Docker inside a studio — `runtime.launcher.docker`, the one-time Sysbox setup in the colima VM (`at-jam colima setup-docker`), registry allow-list recipes, nested-container egress, limitations. | You are enabling Docker inside studios, installing Sysbox, allow-listing a registry, or debugging a nested container's network. |
```

```text
In `docs/INDEX.md`: delete the `orchestration/INDEX.md` row; the `OVERVIEW.md` row → `| [OVERVIEW.md](OVERVIEW.md) | You need what this repo builds (the studio image, its hardening and egress tiers), the package map, or how to build/test/run. |`; the `usage/INDEX.md` row → `| [usage/INDEX.md](usage/INDEX.md) | You are running one of the binaries (`at-jam`, `at-mint`) and need its flags, config, or I/O contract. |`; `updated: 2026-10-26`.
```

- [ ] **Step 7: OVERVIEW.md — remove the deleted CLI, keep what is true (minimal; slice 4 rewrites)**

Frontmatter: `summary:` → `What this repo builds and how it fits together — the studio image Jam assembles (harness + sealed hardening), its egress tiers and base gate, the package map, and how to build/test/run.`; `read_when:` → `You need what this repo builds, the studio image's security model, the package map, or how to build/test/run.`; `owns:` → `the project overview: studio image assembly, security model, package map`; `updated: 2026-10-26`.

Replace the title and intro (from `# at-cove — Project Overview` through the `- **Dependencies:** …` bullet) with:

```markdown
# cove — Project Overview

> **Interim (jam-only slice 2).** The standalone `at-cove` CLI, the in-sandbox
> `at-task` worker and the `at-switchboard` Discord conductor are deleted; Jam
> (`at-jam`) is the only way this repo runs agents. This overview keeps what is
> still true — the image every studio is built from, its hardening and egress
> tiers, the package map, building and testing — and still uses some
> at-cove-era words (a kit's `config.yml`, `image.allowed-domains`) where a
> studio kit now has `kit.yml` and `egress:`. Slice 4 of the
> [jam-only design](superpowers/specs/2026-10-26-jam-only-design.md) rewrites it.

This repo builds Jam and the **hardened Claude Code studios** it runs: Jam's
launcher assembles each studio image (a kit base, the harness layer, then the
sealed hardening layer), raises it in Colima, brokers its credentials, and
starts `cove-master`, the in-studio agent supervisor.

- **Module:** `github.com/aethons-tools/cove`
- **Binaries:** `at-jam` (the service and its CLI), `at-mint` (host-side token minter), `cove-master` (embedded in every studio image); dev tools `adopt-base`, `gen-blessed`, `gen-icons`.
- **Dependencies:** see `go.mod` — no CLI framework.
```

```text
In `## Why it exists`: item 1's `The allow-list is **additive across three tiers** — a sealed base, the kit's\n   baked root list, and a per-session, per-class delta applied at session start\n   (see …) —\n   so a kit can *widen* egress for one worker or collaborator class without ever\n   weakening the sealed base.` → `The allow-list is **additive** — a sealed base, an always-on infra list and the\n   kit's list, which a Jam role's policy may replace within the kit's ceiling (see\n   [Egress: four additive allow-lists and a ceiling](#egress-four-additive-allow-lists-and-a-ceiling)) —\n   so a kit can *widen* egress without ever weakening the sealed base.`; item 2 → `2. **Secrets never touch disk or the host process table** —\n   Jam's broker holds the real credentials and a studio holds only a scoped identity\n   token ([connector](usage/jam/connector.md)); what a raise stages into the studio\n   (cove-master's env and prompt) goes over SSH stdin into tmpfs, never argv or disk.`; the closing paragraph (`The governing design principle is **SSH as the universal interface**: …`) → `The launcher reaches every studio over SSH (`internal/sshargs`) with a key it\nbakes into the image.`
```

Delete these sections whole (heading through the line before the next heading named here):
- `## The kit: \`.at-cove/\`` … up to `## How the build context is assembled`;
- `## Workspace and state volumes` … up to `## Architecture`;
- `## Status and roadmap` … to the end of the file (replaced below).

```text
In `## How the build context is assembled`:
- its first paragraph (`\`install\` writes \`<kit>/.build/\` — the single build path. … The image is layered`) → `Jam's launcher assembles each studio kit's context (\`internal/assemble.AssembleContext\`) when the kit's image is not yet built. The context\nis **the harness layer and the sealed layer** plus a few generated files — there\nis no kit overlay. The image is layered`;
- item 0's `a full \`config.yml\` kit gets its\n   [\`model-spec:\`](usage/at-cove-config.md#model-spec) block's version and plugins,\n   or without one the default install — the one pinned\n   \`modelspec.DefaultClaudeVersion\` (bumped by Renovate) and its plugins; a Jam\n   studio kit gets its raising role's spec ([model-spec-harness.md](usage/jam/model-spec-harness.md)).` → `a studio gets its raising role's spec's version and plugins\n   ([model-spec-harness.md](usage/jam/model-spec-harness.md)), or with none the\n   default install — the pinned \`modelspec.DefaultClaudeVersion\` (bumped by Renovate) and its plugins.`;
- item 1: `(\`seed-agent-data.sh\` — not the seeded content, see [the state volume](#workspace-and-state-volumes))` → `(\`seed-agent-data.sh\` — not the seeded content)` and `the version-locked \`at-task\` binary` → `the version-locked \`cove-master\` binary`;
- item 2 → `2. **Generated** — the kit's egress list (baked into \`allowed_domains.kit.txt\` and the \`egress_ceiling.txt\` bound), the Jam host into \`allowed_domains.infra.txt\`, the kit's MCP servers, and the managed public key (see [Egress: four additive allow-lists and a ceiling](#egress-four-additive-allow-lists-and-a-ceiling)).`;
- in the identity paragraph `at-cove's build identity hashes only` → `the assembly identity hashes only`, and `while the install itself (type,\nversion, plugins) is in a full kit's currency inputs (so a \`model-spec:\` edit,\nor a \`DefaultClaudeVersion\` bump for a kit without one, makes the install stale) and in a studio\nkit's build-digest` → `while the install itself (type,\nversion, plugins) is in the studio kit's build-digest`;
- `After it,\n\`install\` writes the managed public key` → `After it,\nthe assembler writes the managed public key`.
```

In `### The base image and the provenance gate`: `(in \`Backend.Install\`, the single build+gate\npath)` → `(\`ResolveKitBase*\`, which the launcher calls before every kit build)`; replace `unless \`at-cove install --allow-unverified-base\` downgrades the\nrejection to a loud warning — the gate and that flag live only on \`install\`, the\none place a base is built.` with `— there is no override.`; delete the paragraph that starts `**Pinning the built image by digest (COV-78).**` (it describes at-cove's \`install.json\`).

In `### Egress: four additive allow-lists and a ceiling`: the table's `infra` row → `| \`allowed_domains.infra.txt\` | the Jam host (the launcher's \`jam-host\`) | baked at build | every studio; a role can't remove it |`; the `kit` row → `| \`allowed_domains.kit.txt\` | the **active policy list**: the studio kit's \`egress:\` by default | baked at build; replaced at raise for a Jam role with a policy | every studio |`; the `session` row → `| \`allowed_domains.session.txt\` | baked **empty** (its per-session tier went with at-cove) | baked | — |`; `is an immutable baked copy of\n\`image.allowed-domains\`` → `is an immutable baked copy of\nthe kit's egress list`; replace the paragraph starting `So a class's effective egress is the union` with `So a studio's effective egress is **\`base ∪ infra ∪ kit\`** (the kit list being\nthe role's policy when it has one). The session file stays empty, so its ACL never\ndangles.`; delete the two paragraphs starting `**Vertex kits auto-gain their GCP hosts.**` and `**GitLab kits reach their host too.**` (at-cove's config-derived infra hosts; Jam bakes only its own host) and the paragraph starting `**Applied at session start, privileged — not baked.**`.

In `## Architecture` replace the package-map code block and every paragraph after it up to `## Building, testing, running` with:

````markdown
```
cmd/at-jam/                    at-jam: `serve` (broker + control plane + launcher + Requisitioner) and the admin-API client verbs
cmd/at-mint/                   host-side token minter (github / anthropic)
cmd/cove-master/               the in-studio agent supervisor (embedded in every studio image)
cmd/{adopt-base,gen-blessed,gen-icons}/  dev tools: raise the blessed watermark, snapshot blessed bases, regenerate app icons
internal/jam/                  broker + control plane (store, roster, intercom, admin API/UI); internal/jam/launcher raises studios
internal/dispatcher/           the Requisitioner's poll → claim → raise loop
internal/dispatch/{linear,githubissues}/  Jam's trackers (Linear GraphQL, GitHub Issues REST) behind internal/tracker.Tracker
internal/dispatch/scheduler/   tracker vocabulary: Role, Issue, Comment, ClassifyLabels, AssembleBrief
internal/assemble/             the studio build context: harness stage + embedded sealed hardening layer + egress lists + key
internal/harnessinstall/       the harness layer: a model-spec's CLI install (exact version) + plugin seed
internal/install/              the assembly identity (AtCoveIdentity) that keys studio image tags
internal/covemasterbin/        the embedded linux cove-master binaries
internal/backend/{,colima/}    the docker surfaces the launcher drives (run/dial/pause/remove, volumes, egress, SSH CA, kit builds) and their Colima implementation
internal/{baseimage,basedigest}/  base resolution + the blessed-prefix provenance gate
internal/connect/              LaunchCoveMaster: stage cove-master's env/prompt into tmpfs over ssh and start it
internal/kit/                  shared kit types: secret demands, MCP servers, tracker blocks
internal/studio/               the StudioKit definition
internal/discord/              Discord REST client for Jam's relay
internal/keys/                 the launcher's SSH keypair (generated on first use)
internal/naming/               studio container and volume names
internal/{usersecret,mint,secret}/  Jam's credentials file, mint: expansion, host-side resolution
internal/sshargs/              pure argv builders for the ssh client
internal/runner/               Runner interface (OS impl + Fake)
```

Jam itself — `serve`, the broker, the roster, kits, studios, the Requisitioner,
the intercom — is documented under [usage/jam/](usage/jam/INDEX.md);
[renamed-from-harbor.md](usage/jam/renamed-from-harbor.md) lists its Harbor-era names.
````

```text
In `### Installing the binaries`: `fastest way to get \`at-cove\`, \`at-mint\` and \`at-jam\`:` → `fastest way to get \`at-jam\` and \`at-mint\`:`; `and installs all three binaries (a pinned release cut before \`at-jam\` shipped installs just the first two). \`at-task\` and \`at-switchboard\` ship **embedded** in\n\`at-cove\`, so neither is installed separately.` → `and installs both (the archive also holds an \`at-cove\` tombstone, kept only so an\nolder \`at-jam update\` can install it; this installer skips it). \`cove-master\`\nships **embedded** in \`at-jam\`.`; `**Updating in place.** Once installed, [\`at-cove update\`](#command-surface) (or the\nidentical \`at-jam update\`) upgrades all the binaries` → `**Updating in place.** Once installed, \`at-jam update\` upgrades both binaries`; replace the `just` code block with
```

```
just build           # build at-mint + at-jam into dist/<os>-<arch>/
just build-all       # cross-compile every supported target
just jam <args>      # run the last-built at-jam with <args>
just install         # install the host binaries onto your PATH (no sudo by default)
just test            # hermetic unit tests — no docker/network/ssh
just integration     # real-ssh tests of the ssh argv builders (needs ssh/sshd/ssh-keygen)
just integration-jam # jam's integration-tagged tests (real TLS, sshd, git)
just lint            # go vet + gofmt check + shellcheck/hadolint (if present)
just setup           # install dev/test tooling (podman, shellcheck, hadolint, jq)
```

and `The real-ssh integration suite (build tag \`integration\`) boots a throwaway \`sshd\` on loopback with a fake \`claude\`\nand exercises the transports and TOFU end-to-end without Docker.` → `The real-ssh suite (build tag \`integration\`) boots a throwaway \`sshd\` on loopback\nand exercises the ssh argv builders and TOFU end-to-end without Docker.`; `the blessed-base snapshot, and the at-cove binaries are all built` → `the blessed-base snapshot, and the host binaries are all built`.

Append the new ending:

```markdown
## Further reading

- [`usage/INDEX.md`](usage/INDEX.md) — running the binaries (Jam, `at-mint`, docker-in-sandbox).
- [`DEVELOPMENT.md`](DEVELOPMENT.md) — the dev sandbox, CI and the release pipeline.
- `superpowers/` — the design history (dated specs and plans); the
  [jam-only design](superpowers/specs/2026-10-26-jam-only-design.md) is the current direction.
```

Check: `grep -nE 'at-cove (create|chat|work|dispatch|install|teammate|view|update)|at-task|at-switchboard|usage/at-cove|orchestration/|observability|#(command-surface|state-vs-config|backends|authentication|workspace-and-state-volumes|the-chat-command|per-collaborator|secret-injection|the-kit-at-cove)' docs/OVERVIEW.md` prints only the interim banner's line naming the deleted binaries; fix anything else it prints the same way. Then, in the kept sections, replace each remaining mention of `at-cove` *as the actor* ("the base at-cove hardens", "at-cove reads the resolved image's …") with `the launcher` — leave path/identifier names (`~/.config/at-cove`, `atcove-`, `AtCoveIdentity`) for slice 3.

- [ ] **Step 8: AGENTS.md and README.md (minimal)**

`AGENTS.md`:
- line 3 → ``cove` — the Jam service (`at-jam`) and the hardened Claude Code studios it builds and runs.`;
- the **Start here** line's `what the project is, the kit format, the command surface, the security model, the architecture, and how to build/test/run.` → `what this repo builds, the studio image's security model, the package map, and how to build/test/run.`;
- replace the three **Quick orientation** bullets with

```markdown
- **Module:** `github.com/aethons-tools/cove` · **Binaries:** `at-jam`, `at-mint`, `cove-master` (embedded in studio images); dev tools `adopt-base`, `gen-blessed`, `gen-icons`
- **Entry points:** `cmd/at-jam/main.go` (the Jam service and its CLI — start at [`docs/usage/jam/INDEX.md`](docs/usage/jam/INDEX.md)), `cmd/cove-master/main.go` (the in-studio agent supervisor), `cmd/at-mint/main.go` (the token minter).
- **Packages:** see the package map in [the overview](docs/OVERVIEW.md#architecture).
```

- `a template tree copied into the sandbox VMs that \`at-cove\` builds,` → `a template tree copied into the studio images Jam's launcher builds,`;
- `\`just test\` (hermetic unit tests), \`just build\`, \`just run <args>\`, \`just lint\`, \`just integration\` (real-ssh, needs ssh/sshd).` → `\`just test\` (hermetic unit tests), \`just build\`, \`just jam <args>\`, \`just lint\`, \`just integration\` (real-ssh, needs ssh/sshd).`;
- in the **Secrets** bullet delete the sentences from `Structured logging preserves this by construction:` to the end of the bullet (the observability link);
- delete the whole **Diagnostics are structured; pure-UX output stays plain.** bullet;
- `Read \`specs/2026-06-26-cove-sandboxes-design.md\` for the current design before making structural changes.` → `Read \`specs/2026-10-26-jam-only-design.md\` for the current direction before making structural changes.`

Replace `README.md` with:

````markdown
# cove

**Jam** — a durable service that runs hardened Claude Code studios — and the
studio images it builds.

- **Egress is locked down** —
  a studio reaches only an allow-listed set of domains through an in-studio Squid proxy,
  with `nftables` dropping everything else.
- **Credentials stay in Jam** —
  a studio holds only a scoped identity token; Jam's broker injects the real ones.
- **The hardening is foolproof** —
  the security-critical files ship embedded in `at-jam` and are layered on *last*,
  so they always win over anything a kit supplies.

## Quickstart

Install `at-jam` and `at-mint` with the one-command installer. The repo is
private today, so it uses your GitHub CLI login (`gh auth login`) to pull the
latest release, verify its checksum, and drop the binaries on your PATH:

```bash
gh api -H "Accept: application/vnd.github.raw" \
  /repos/aethons-tools/cove/contents/install.sh | bash
```

Once the repo is public this collapses to the classic anonymous form — same script:

```bash
curl -fsSL https://raw.githubusercontent.com/aethons-tools/cove/main/install.sh | bash
```

`at-jam update` moves to the latest release. Prefer to build from source? Use
`just install`. Version pinning and the `BINDIR` / `COVE_SYSTEM` overrides are in
[the overview](docs/OVERVIEW.md#installing-the-binaries). To stand a Jam up, start
at [`docs/usage/jam/INDEX.md`](docs/usage/jam/INDEX.md).

## Requirements

- Go (to build) and [`just`](https://github.com/casey/just) (optional task runner — logic lives in `scripts/`).
- A [Colima](https://github.com/abiosoft/colima) host for Jam's launcher to run studios in (see [serve.md](docs/usage/jam/serve.md#the-launcher-runtimelauncher)).
- An `ssh` client.

## Documentation

- **[`docs/INDEX.md`](docs/INDEX.md)** — the docs map; start here.
- **[`docs/OVERVIEW.md`](docs/OVERVIEW.md)** — what this repo builds, the security model, the package map, build/test/run.
- **[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)** — building and testing, including the egress-locked-sandbox toolchain workarounds.
- **[`AGENTS.md`](AGENTS.md)** — orientation and working conventions for coding agents in this repo (the root `CLAUDE.md` just imports this).
- **[`docs/superpowers/`](docs/superpowers/)** — the design history: dated specs and implementation plans.

## Development

```bash
just test          # hermetic unit tests — no docker/network/ssh
just integration   # real-ssh integration tests (needs ssh/sshd/ssh-keygen)
just lint          # go vet + gofmt check + shellcheck/hadolint
just build         # build at-mint + at-jam into dist/<os>-<arch>/
```

Tests are hermetic by default — they drive a fake process runner, so no Docker,
network, or live VM is required.
````

- [ ] **Step 9: DEVELOPMENT.md**

```text
| Replace | With |
|---|---|
| `Operational notes for building and testing \`at-cove\`,` | `Operational notes for building and testing this repo,` |
| `` `image.allowed-domains` list in [`.at-cove/config.yml`](../.at-cove/config.yml). `` | `` `egress:` list in [`.at-jam/cove-ic/kit.yml`](../.at-jam/cove-ic/kit.yml), the repo's Jam studio kit. `` |
| `- **Not** gated: \`just integration\` (real-ssh), \`just test-browser\` (headless\n  Chrome) and \`just e2e\` (live infra).` | `- **Not** gated: \`just integration\` (real-ssh) and \`just test-browser\` (headless\n  Chrome).` |
| `single source of truth shared by CI job containers and the agent sandboxes\nat-cove hardens,` | `single source of truth shared by CI job containers and the studios\nJam's launcher hardens,` |
| `**Pure tools**: no language toolchains, chrome, or java — and **no \`at-task\` or\n  \`at-switchboard\`**. at-cove injects the version-locked \`at-task\` (COV-42) and\n  \`at-switchboard\` (COV-135) into its sealed hardening layer, so the base has no\n  Go build` | `**Pure tools**: no language toolchains, chrome, or java — and **no\n  \`cove-master\`**: the launcher injects the version-locked \`cove-master\` into its\n  sealed hardening layer, so the base has no Go build` |
| every `re-cut at-cove` / `the at-cove leg` / `→ at-cove (embeds at-task +\n  at-switchboard + list)` in `## CI — the release pipeline` | `re-cut the host binaries` / `the binaries leg` / `→ the host binaries (embed cove-master + list)` |
| `at-cove is built by\n  \`goreleaser --snapshot\` (one archive per os/arch carrying at-cove + at-mint +\n  at-jam, plus checksums,` | `the binaries are built by\n  \`goreleaser --snapshot\` (one archive per os/arch carrying at-mint + at-jam + the\n  at-cove tombstone, plus checksums,` |
| the whole `- **at-task and at-switchboard are embedded**, …` bullet | `- **cove-master is embedded**, not shipped standalone — re-cutting the binaries\n  re-cuts it (the [\`.goreleaser.yaml\`](../.goreleaser.yaml) before-hook runs\n  \`scripts/stage-covemaster.sh\`). The archive also carries\n  \`scripts/release/at-cove\`, a tombstone that keeps an older \`at-jam update\` working\n  (its embedded \`install.sh\` installs \`at-cove\` unconditionally); slice 3 of the\n  jam-only design drops it.` |
| `(\`cove-base-image\`, \`cove-image\`, \`at-cove\`) is tagged` | `(\`cove-base-image\`, \`cove-image\`, the binaries release) is tagged` |
| `differ — each reflects when it was last built. at-cove references the base by` | `differ — each reflects when it was last built. Jam references the base by` |
| `A dev \`at-cove\` run straight from \`dist/<os>-<arch>/at-cove\` invokes its` | `A dev \`at-jam\` run straight from \`dist/<os>-<arch>/at-jam\` invokes its` |
| the whole `### Adopting a new base` section body (through the `Design:` link line) | the text below |
| the whole `## Verified \`claude\` CLI facts` section | (delete — it described at-cove's `connect` login probe) |
| `(see [\`OVERVIEW.md\`](OVERVIEW.md#authentication) for the mechanism).\nA session whose kit does not declare that secret has no token,\nand git fails closed rather than prompting;\nthis repo's kit declares it under \`collaborators.<common>.secrets\`\n(see [at-cove-config.md](usage/at-cove-config.md#collaborators)).` | `— in a Jam studio, git authenticates through Jam's broker\n([connector.md](usage/jam/connector.md)).` |
| `the hardening rewrites GitHub remotes to HTTPS and feeds a \`GITHUB_TOKEN\` via a credential helper (see [\`OVERVIEW.md\`](OVERVIEW.md#authentication)).` | `the hardening rewrites GitHub remotes to HTTPS and git authenticates through Jam's broker ([connector.md](usage/jam/connector.md)).` |
```

New `### Adopting a new base` body:

````markdown
Publishing is automatic (above), and so is adopting a routine base: the release's
blessed-list snapshot makes the newest published base Jam's default. A **breaking**
base change (one that shifts the OCI layer-diff-id prefix, or one the sealed
hardening layer newly *depends on*) also needs the blessed watermark raised so older
bases stop being trusted. `just adopt-base` does that:

```
just adopt-base <tag>        # raise internal/basedigest/blessed/watermark.txt to <tag>'s base
just adopt-base <tag> --pr   # after editing, branch + commit + open a PR
```

It resolves `<tag>` (e.g. `527-0808`, or `latest`) to `cove-base-image`'s
multi-arch **index** digest via the GitHub packages API. It needs `GITHUB_TOKEN`
with `read:packages` (like `gen-blessed`) and cannot run offline; preview the
resulting set with `just gen-blessed`. Only raise the watermark for a genuinely
breaking base — doing it on a routine bump wrongly evicts still-valid older bases.
Design: [adopt-base-recipe](superpowers/specs/2026-08-09-adopt-base-recipe-design.md).
````

(The inner ``` fence is literal doc content.)

- [ ] **Step 10: docker-in-sandbox.md**

- frontmatter `prereqs:` → `../OVERVIEW.md for the image + egress model; jam/serve.md#the-launcher-runtimelauncher for the launcher's docker setting`; `updated: 2026-10-26`;
- `colima VM** (below); at-cove's preflight fails fast and points you here if missing.` → `colima VM** (below); the launcher's preflight fails fast and points you here if missing.`;
- replace `## Enabling it`'s body up to `A kit without the flag is **byte-for-byte unchanged**` with

````markdown
The switch is the launcher's `docker` setting in the serve config
([serve.md](jam/serve.md#the-launcher-runtimelauncher)):

```yaml
runtime:
  launcher:
    docker: true
```

When set, every studio the launcher raises runs so that:

- the **studio container itself** runs under `--runtime=sysbox-runc`, which lets a
  normal **rootful `dockerd`** run inside an *unprivileged* container;
- the inner daemon is exposed on the default socket, so the `agent` user runs
  `docker …` with **no `DOCKER_HOST` to set** (it is in the `docker` group);
- a **persistent, bounded cache volume** (`<container>-docker`) is mounted at the
  inner `/var/lib/docker`, so pulled layers and build cache survive a re-raise;
  teardown removes it ([standing-state.md](jam/standing-state.md)).
````

```text
  and `A kit without the flag` → `A launcher without the setting`;
- `Sysbox is a **VM-level runtime** — at-cove *detects* it but never installs it.` → `Sysbox is a **VM-level runtime** — Jam *detects* it but never installs it.`; `(behind the \`colima\` docker context at-cove\nuses)` → `(behind the \`colima\` docker context the launcher\nuses)`; `\`check-docker\` runs at-cove's preflight probe` → `\`check-docker\` runs the launcher's preflight probe`; `If \`docker: true\` and the runtime is absent, \`at-cove\` **fails the preflight**` → `If \`docker: true\` and the runtime is absent, a raise **fails the preflight**`;
- `in the kit's root [\`image.allowed-domains\`](at-cove-config.md#imageallowed-domains)` → `in the studio kit's \`egress:\` list ([kits.md](jam/kits.md))`.
```

Then `grep -n 'at-cove\|\.at-cove\|image.allowed-domains\|recreate\|destroy' docs/usage/docker-in-sandbox.md` and rewrite any remaining hit in the Limitations/egress sections the same way (a studio, the launcher, the kit's `egress:`, re-raise/teardown).

- [ ] **Step 11: Jam docs**

```text
| File | Replace | With |
|---|---|---|
| `jam/INDEX.md` | frontmatter `prereqs: ../../OVERVIEW.md for what at-cove/Jam is; ../at-cove-config.md#jam for the studio side of the connection` | `prereqs: ../../OVERVIEW.md for the studio image and its security model` |
| `jam/INDEX.md` | `` `at-jam` ships in the same release archive as `at-cove`: install it with the `` | `` `at-jam` ships in one release archive with `at-mint`: install it with the `` |
| `jam/INDEX.md` | `This is the *how to run and administer it* layer. For the studio side — making a\nsandbox use a Jam — see [\`../at-cove-config.md#jam\`](../at-cove-config.md).` | `This is the *how to run and administer it* layer.` |
| `jam/connector.md` | `- **Host-side clients** (at-cove connect, teammates, dispatch) call` | `- **Host-side clients** call` |
| `jam/roster.md` | `The studio gets the kit's\n  \`image.allowed-domains\` as before` | `The studio gets the kit's\n  \`egress:\` list as before` |
| `jam/roster.md` | `[\`image.allowed-domains\`](../at-cove-config.md#imageallowed-domains)` | `` `egress:` list ([kits.md](kits.md#the-egress-ceiling-cov-208)) `` |
| `jam/roster.md` | `- Hardened studios usually **auto-enroll** themselves at session start rather than\n  using a hand-run snippet — see [\`../at-cove-config.md#jam\`](../at-cove-config.md).` | (delete the bullet) |
| `jam/session-events.md` | `This is a\ndeliberate exception to [observability rule 3](../observability.md) ("raw\nagent/VM output stays VM-local"), which governs the structured log sink, not\nthis separate audit channel.` | `Raw agent output otherwise stays inside the\nstudio; this audit channel is the deliberate exception.` |
| `jam/studio-kit-migration.md` | `` `at-cove install` and its manifest are unaffected for interactive kits. `` | (delete the sentence) |
| `jam/session-context.md` | `A plain at-cove sandbox learns the sandbox rules from the image's \`SANDBOX.md\`,\nwhich the seeded \`CLAUDE.md\` imports ([OVERVIEW](../../OVERVIEW.md#workspace-and-state-volumes)).` | `Outside a Jam session an agent learns the sandbox rules from the image's\n\`SANDBOX.md\`, which the seeded \`CLAUDE.md\` imports.` |
| `jam/dispatch-runbook.md` | frontmatter `prereqs: serve.md, roster.md, requisitioner.md, coves.md, ../at-cove-config.md#jam — this runbook` | `prereqs: serve.md, roster.md, requisitioner.md, coves.md — this runbook` |
| `jam/dispatch-runbook.md` | `- Colima running; a studio image built via \`at-cove install\` (its \`install.json\`).` | `- Colima running (the launcher builds each studio kit's image on its first raise — [kits.md](kits.md)).` |
| `jam/dispatch-runbook.md` | `If a studio's tools 404, rebuild **both** the image (\`at-cove\n  install\`) and the host binary (\`just build\`) from the same commit.` | `If a studio's tools 404, rebuild the host binary (\`just build\`) and\n  re-raise the studio — its image rebuilds from the new binary's payload.` |
| `jam/standing-state.md` | `` (`atcove-cove-<session id>`) like an `at-cove create` sandbox's: `` | `` (`atcove-cove-<session id>`): `` |
| `jam/kits.md` | `references it by name. The registry is **studio-only**: it is not a place for a\nfull at-cove \`config.yml\` (those stay repo-committed \`.at-cove/\` kits; see\n[migration note](studio-kit-migration.md)).` | `references it by name. The registry is **studio-only**\n([migration note](studio-kit-migration.md)).` |
| `jam/kits.md` | `(hardening layer, at-task / at-switchboard / cove-master)` | `(hardening layer, cove-master)` |
| `jam/kits.md` | `running studios keep their image until re-raised.` | `running studios keep their image until re-raised. Removing at-task and at-switchboard from that payload (jam-only slice 2) changed every fingerprint once, so each kit rebuilds on its next raise after that upgrade.` |
| `jam/model-spec-harness.md` | `No\nspec delivered = claude-default's install. A plain at-cove kit's\n[\`model-spec:\`](../at-cove-config.md#model-spec) block drives the same layer at\n\`at-cove install\` (none = claude-default's install). **Enablement follows the spec, too:**` | `No\nspec delivered = claude-default's install. **Enablement follows the spec, too:**` |
| `jam/model-spec-harness.md` | `For **interactive** sessions (\`at-cove connect\`/\`chat\` run \`claude\` without the harness argv).` | `For **interactive** sessions (a \`claude\` run without the harness argv, e.g. over SSH into a studio).` |
| `jam/model-spec-harness.md` | the paragraph `**Plain at-cove kits** run no migration: … replacement.` | (delete) |
| `jam/model-specs.md` | `The same\nschema and validator load a plain at-cove kit's\n[\`model-spec:\` block](../at-cove-config.md#model-spec) (no \`principal\` there;\n\`version\` optional).` | (delete; the paragraph ends at `[model-spec-policy.md](model-spec-policy.md).`) |
| `jam/serve.md` | `For the studio side of the broker connection — the \`jam:\` kit config, auto-enroll,\nand reaching a host-run Jam — see [\`../at-cove-config.md#jam\`](../at-cove-config.md).\n` | (delete) |
| `jam/serve.md` | `Jam uses to reach the studio (defaults to \`~/.config/at-cove/id_ed25519\`).` | `Jam uses to reach the studio (defaults to \`~/.config/at-cove/id_ed25519\`; serve generates the pair on first use).` |
| `jam/vertex.md` | `Plain at-cove (no Jam) is\nseparate: it still seeds the operator's ADC into the VM\n([at-cove-secrets.md](../at-cove-secrets.md)).` | (delete) |
| `jam/renamed-from-harbor.md` | ``run under that name it works and warns. `at-cove` looks for `at-jam` first, then `at-harbor`.`` | `run under that name it works and warns.` |
| `jam/renamed-from-harbor.md` | `Writers (the enroll snippet, at-cove, the launcher)` | `Writers (the enroll snippet, the launcher)` |
| `jam/renamed-from-harbor.md` | the row `` | kit `config.yml` `harbor:` block | `jam:` | Yes, with a warning. Setting both is a validation error. | `` | `` | kit `config.yml` `harbor:` block | `jam:` | Removed with at-cove (jam-only slice 2): there is no kit `config.yml` any more. | `` |
```

Bump `updated:` to `2026-10-26` in every file you edit.

`dev/jam.dev.yml.example`: delete the line `    install-manifest: /path/to/cove/.at-cove/.state/install.json` (`install-manifest` was removed — serve.md — and the path named the deleted kit).

- [ ] **Step 12: Verify**

```bash
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs 2>&1 | grep '^ERROR' | grep -v '^ERROR superpowers/' | sort > /tmp/audit-after.txt
comm -13 /tmp/audit-before.txt /tmp/audit-after.txt
grep -rnE 'usage/(at-cove-config|at-cove-secrets|discord-teammate|workspace-visibility|at-task|at-task-inputs|at-task-output|observability)\.md|\.\./(at-cove-config|at-cove-secrets|discord-teammate|workspace-visibility|at-task[a-z-]*|observability)\.md|orchestration/|\(at-cove-(config|secrets)\.md' docs README.md AGENTS.md --include=*.md | grep -v '^docs/superpowers/' ; echo "grep-exit=$?"
go test ./internal/usersecret/
just test && just lint
```

Expected: `comm` prints nothing (no new non-superpowers audit error); `grep-exit=1`; tests PASS; `just test`/`just lint` exit 0.

- [ ] **Step 13: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "docs: delete at-cove/at-task/at-switchboard docs; keep the rest accurate

Deleted: at-cove-config, at-cove-secrets, discord-teammate,
workspace-visibility, at-task*, observability and orchestration/.
credentials.md now owns the source vocabulary and minters: schema (its
minters example was invalid; a doc-example test pins it). OVERVIEW,
AGENTS and README lose the deleted CLI; slice 4 rewrites them.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 8: Guard the deletion; sweep the stragglers; final verification

A permanent test keeps the deleted packages from coming back (as a directory or an import) and keeps tracked files from naming a deleted binary's invocations or plumbing. History (`docs/superpowers/`), the in-image agent docs (slice 4 owns `images/cove-base-image/image-files/home/agent/.init-agent-data/` — its `SANDBOX.md` and `board-*` skills still describe `at-cove recreate`/`at-task`, and editing them republishes the base image), and the frozen legacy installer are exempt. Product-name mentions of "at-cove" in comments and the slice-3-owned names (`~/.config/at-cove`, `AtCoveIdentity`, `atcove-` prefixes, `AT_COVE_*`) are deliberately not tokens.

**Files:**
- Create: `deleted_test.go` (module root, package `cove`)
- Modify (comment-only): `cmd/at-mint/main.go`, `internal/agentrun/workload.go`, `internal/assemble/embed_test.go`, `internal/cli/cli_test.go`, plus anything else the new test reports

**Interfaces:**
- Consumes: the tree after Tasks 1–7.
- Produces: `TestDeletedPackagesStayDeleted`, `TestNoMentionsOfDeletedBinaries` (run by `just test`).

- [ ] **Step 1: Write the guard test**

Create `deleted_test.go`:

```go
package cove

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// deletedPackages are the binaries and packages jam-only slice 2 removed
// (docs/superpowers/specs/2026-10-26-jam-only-design.md §2). None may come back
// as a directory or an import.
var deletedPackages = []string{
	"cmd/at-cove", "cmd/at-switchboard", "cmd/at-task",
	"internal/atswitchboard", "internal/attask", "internal/awake",
	"internal/dispatch/exec", "internal/dispatch/github", "internal/dispatch/gitlab",
	"internal/dispatch/worker", "internal/dispatchrun", "internal/dockere2e",
	"internal/sshconfig", "internal/state", "internal/switchboard",
}

// deletedMentions are tokens that only make sense while a deleted binary or
// its build plumbing exists: an invocation, a staging script, an install path.
var deletedMentions = []string{
	"cmd/at-cove", "cmd/at-task", "cmd/at-switchboard",
	"internal/attask", "internal/atswitchboard", "internal/switchboard",
	"internal/dispatchrun", "stage-attask",
	"bin/at-task", "bin/at-switchboard",
	"at-task prepare", "at-task complete", "at-task clone-workspace",
	"at-cove dispatch", "at-cove work", "at-cove teammate", "at-cove chat",
	"at-cove create", "at-cove install", "at-cove update", "at-cove view",
	"at-cove ssh-proxy",
}

// mentionExempt are paths whose mentions are history or owned by a later
// slice: the design history, the in-image agent docs (slice 4), the frozen
// legacy installer fixture, and this file.
func mentionExempt(rel string) bool {
	for _, p := range []string{
		"docs/superpowers/",
		"images/cove-base-image/image-files/home/agent/.init-agent-data/",
		"testdata/install-legacy.sh",
		"deleted_test.go",
	} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

func TestDeletedPackagesStayDeleted(t *testing.T) {
	for _, p := range deletedPackages {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (err=%v); slice 2 deleted it", p, err)
		}
	}
	const mod = "github.com/aethons-tools/cove/"
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != "." {
			switch d.Name() {
			case ".git", "node_modules", ".gopath", ".worktrees", ".claude", "dist":
				return filepath.SkipDir // vendored, generated, or other checkouts (gitignored)
			}
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			for _, p := range deletedPackages {
				if ip == mod+p || strings.HasPrefix(ip, mod+p+"/") {
					t.Errorf("%s imports deleted package %s", path, ip)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoMentionsOfDeletedBinaries(t *testing.T) {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" || mentionExempt(rel) {
			continue
		}
		b, err := os.ReadFile(rel)
		if err != nil {
			continue // listed but deleted in the working tree
		}
		s := string(b)
		for _, m := range deletedMentions {
			if strings.Contains(s, m) {
				t.Errorf("%s mentions %q (a deleted binary or its plumbing)", rel, m)
			}
		}
	}
}
```

- [ ] **Step 2: Run it (it must fail on the stragglers)**

Run: `go test -run 'TestDeletedPackagesStayDeleted|TestNoMentionsOfDeletedBinaries' . 2>&1 | grep 'mentions\|imports\|exists' | sort`
Expected: FAIL; `TestDeletedPackagesStayDeleted` passes and `TestNoMentionsOfDeletedBinaries` reports at least:

```
cmd/at-mint/main.go mentions "at-cove work" …
internal/agentrun/workload.go mentions "at-cove work" …
internal/assemble/embed_test.go mentions "at-task clone-workspace" …
internal/cli/cli_test.go mentions "cmd/at-cove" …
```

- [ ] **Step 3: Fix the stragglers**

| File | Replace | With |
|---|---|---|
| `cmd/at-mint/main.go` | `// Fail closed when the token would expire before the run finishes: at-cove work\n\t// passes COVE_RUN_TIMEOUT to the resolver, and a bearer whose TTL is below that\n\t// is guaranteed to 401 mid-run (build/prepare overhead makes it worse). Catch it\n\t// here, before any VM is built, instead of burning a run on a doomed token.` | `// Fail closed when the token would expire before the run finishes: a caller\n\t// that sets COVE_RUN_TIMEOUT (nothing in Jam does) declares how long the\n\t// bearer must live, and one whose TTL is below that would 401 mid-run.` |
| `internal/agentrun/workload.go` | `// No result file is read (worker-result.json is the at-cove work path's).` | `// No result file is read.` |
| `internal/assemble/embed_test.go` | `` // the fresh volume comes up root-owned and the first `chat` session's\n// `at-task clone-workspace` — which runs as the agent user — fails with `` | `` // the fresh volume comes up root-owned and the agent's first write to its\n// workspace — the agent runs as the agent user — fails with `` |
| `internal/cli/cli_test.go` | `` // dispatch path (cmd/at-cove's logging.EnvOr(g.LogLevel, "AT_LOG_LEVEL")): envOr only `` | `` // dispatch path (logging.EnvOr(g.LogLevel, "AT_LOG_LEVEL")): envOr only `` |

For any other file the test reports, rewrite the sentence so it no longer names a deleted command (comments: describe today's caller; docs: route to the Jam doc that owns the subject). Never add an exemption for live code or docs.

Run: `go test -run 'TestDeletedPackagesStayDeleted|TestNoMentionsOfDeletedBinaries' -v .`
Expected: PASS.

- [ ] **Step 4: Final verification**

```bash
export GOPROXY=https://proxy.golang.org,direct
go mod tidy && git diff --exit-code go.mod go.sum && echo "mod-clean"
go build ./... && go vet ./... && go vet -tags integration ./... && echo "build-vet-clean"
M=github.com/aethons-tools/cove
for c in at-jam at-mint cove-master adopt-base gen-blessed gen-icons; do
  go list -deps ./cmd/$c | grep -E "^$M/(cmd/at-(cove|task|switchboard)|internal/(attask|atswitchboard|switchboard|awake|dispatchrun|state|sshconfig|dockere2e|dispatch/(exec|github|gitlab|worker)))(/|$)" && echo "LEAK via $c"
done; echo "deps-checked"
go run golang.org/x/tools/cmd/deadcode@latest ./... 2>/dev/null | grep -E '^internal/(kit|connect|install|naming|assemble|backend|usersecret|sshargs|keys|discord|dispatch)/'
just test && just lint && bash install.test.sh | tail -1
go test -tags integration ./internal/sshargs/ ./internal/discord/ 2>&1 | tail -2
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs 2>&1 | grep '^ERROR' | grep -v '^ERROR superpowers/' | sort | comm -13 /tmp/audit-before.txt -
GOTOOLCHAIN=local go run github.com/goreleaser/goreleaser/v2@v2.12.7 check
git status --short
```

Expected: `mod-clean`; `build-vet-clean`; no `LEAK via …`, then `deps-checked`; the deadcode grep prints only the three test seams `internal/discord/discord.go:…: unreachable func: WithBaseURL` / `WithHTTPClient` / `WithSleep` (used by `discord_test.go`); `just test`/`just lint` exit 0 and `25 passed, 0 failed`; the integration packages `ok` (sshd-dependent tests may SKIP); the audit `comm` prints nothing; goreleaser `1 configuration file(s) validated`; `git status` shows only this task's files.

- [ ] **Step 5: Commit**

```bash
git add -A
git status --short   # review: only this task's files are staged (no dist/, no staged binaries)
git commit -m "test: guard the jam-only slice 2 deletion

deleted_test.go fails if a deleted package returns (dir or import) or a
tracked file names a deleted binary's invocation or plumbing; history,
the slice-4 in-image docs and the frozen legacy installer are exempt.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 6: The PR's release job (Review Focus 5)**

When the user asks for the PR: push the branch, open it, and confirm the `atcove` job (`Validate goreleaser config` + `Build binaries (PR — no publish)`) and `gate` are green before asking for review. File the follow-ups below in `/home/agent/workspace/issues/` (Linear is full).

**Follow-ups (not this slice):**
- The sealed `apply-session-domains.sh` helper and the baked-empty `allowed_domains.session.txt` have no caller left; removing them is a hardening-layer change (needs its own review).
- `internal/logging` keeps at-cove's attended/unattended logger API (`Into`, `LevelFrom`, `EnvOr`, `UserError`, …) that only tests reach now; Jam uses only `logging.Deprecated`.
- `kit.IsReservedSecretName` still reserves at-cove's `AT_TASK_GIT_TOKEN`/`AT_DISPATCH_*` names — decide in slice 3 whether they stay reserved.
- `internal/dispatch/githubissues` and `kit.GitHubTracker` did not move beside Jam's Linear tracker in slice 1 (spec §1); still open.
- `images/cove-base-image/Dockerfile`'s comment and the in-image `SANDBOX.md`/`reference/sandbox-kit-changes.md`/`board-*` skills still describe at-cove/at-task (slice 4).
- The hardening entrypoint still honours `COVE_SHADOW_DIRS`, which only at-cove's shared-workspace `create` set.

---

## Self-review

- **Spec coverage (§2):** binaries — Tasks 1–3; packages only they reach (`dispatchrun`, `state`, `sshconfig`, `awake`, `dispatch/{exec,worker,github,gitlab}`, `attask`, `atswitchboard`, scheduler `Engine`) — Tasks 1, 2, 4, 6 (`keys` deliberately kept, Task 3); dead code in `connect`/`install`/`kit`/`backend`+`colima`/`assemble`/`naming` — Task 6, deadcode-gated; `switchboard` → `internal/discord` — Task 1; payload + `stage-attask.sh`/`build.sh` + identity change — Tasks 1–3, documented in Task 7 (`kits.md`); kits and tests (`kits/reference-worker` + e2e, at-cove integration tests, `dockere2e`) — Tasks 3, 4, 6; docs (~2.8k lines) — Task 7; hardening/egress base/ceiling not weakened — Tasks 1–2 guard test plus the untouched hardening tests; spec §6 "a test asserts no remaining import of the deleted packages" — Task 8.
- **Inventory items that turned out live or different:** `internal/keys` (the only launcher-key generator — kept and wired into serve); `githubissues` stays but loses `New`/`#N` and the `jam` flag (Task 5); `scheduler.Tracker`/`Executor`/`InProgressIssue` and both `ListInProgress` die with the engine (not listed); `kit.Config` and its parser are entirely dead, not "keep kit.Config"; `backend.Instance` stays (RunEphemeral returns it), `GetStatus` stays (the launcher's `Backend` interface); `DispatchOps.ScavengeLabeled` is dead; `usersecret.Load`/`Plan` (at-cove's secrets.yml) are dead; `sshargs.Interactive*` dead; `naming.ShadowVolume`/`SanitizeShadowDir` dead too; `kits/pg-tunnel-collab` is an at-cove `chat` example (deleted); `internal/dockere2e` is wholly at-cove-driven (deleted, with `just integration-docker`); `adopt-base` read `.at-cove/config.yml` (narrowed to the watermark); `stage-attask.sh` also staged cove-master (renamed, not deleted); `docs/usage/observability.md` is at-cove-only (deleted); `credentials.md` depended on `at-cove-secrets.md` for its vocabulary (moved) and its example was invalid (fixed + test); `dev/jam.dev.yml.example` still set the removed `install-manifest`.
- **Placeholder scan:** every code step carries the code or an exact command; doc steps give exact replacement text; the one open-ended instruction (Task 8 Step 3, "any other file the test reports") is bounded by a test that names each file and token.
- **Type consistency:** `keys.EnsureFile(runner.Runner, string) ([]byte, error)` (Task 3) is the only key API used afterwards; `linear.New(kit.LinearTracker, …)` (Task 5) is what Task 6's `kit` rewrite relies on; `NewWithContext(r, "")` is the colima constructor every Task 6 test uses; `TestAssembleContextStagesOnlyCoveMaster` is created in Task 1 and extended in Task 2 under the same name.
- **Review Focus:** each of the five items names its pinning test and owning task (keys → 3; legacy installer → 3; hardening intact → 1–2; requisitioner blocks → 5–6; release workflow → 3 and 8).
