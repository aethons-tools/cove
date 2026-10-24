# Studio setup inspection — slice 1 (model, snapshots, image reader, API and CLI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator sees, from outside the container, everything an agent is set up with. `at-jam role inspect <project>/<role>` renders what a studio raised now would get. `at-jam studio inspect <studio> [--diff]` renders what a running studio got. That covers the session-context files at their real paths, egress, destinations, env, model spec, kit chain and image, the `/agent-data` seed, MCP servers and policy. `--diff` shows what the studio is missing against a raise now. The same data is served at `GET /admin/roles/{project}/{role}/setup` and `GET /admin/instances/{id}/setup`. Secret values never appear: not in the output, not in the stored snapshots.

**Architecture:**
- **One model, text bodies.** A `StudioSetup` is a list of `SetupSection{name, source, stale, at, reason, body, diff}`. Each body is plain text built by one pure builder per section (`contextBody`, `egressBody`, `connectorBody`, `kitRefsBody`, `destinationsBody`, …). The role render, the studio render and the snapshot writer all call the same builders. So the text that is stored, shown and diffed is the same text, and `linediff.Diff` compares bodies directly.
- **One redaction function.** `RedactEnv(key, value)` is the only place a value is masked. The builders call it for connector env, image `ENV`, build args, label values and every string in `settings.json`. Snapshots store builder output, which has already been through it, so nothing needs a second scrub before storage.
- **Snapshots are written at delivery, on change.** `RecordSetupPart` compares the part's fingerprint with the stored one and writes only on a difference. The fingerprints are the ones Jam already has: `Bundle.Fingerprint`, `EgressFingerprint`, `snippet.Fingerprint`, and kit ref + image tag. It is called from `Raise` (all four parts), `GET /context`, `GET /connector`, and a successful `ApplyEgress` (reconcile and resume). A failed write is logged and never fails the delivery.
- **Storage** is a new `SetupSnapshotStore` on `jam.Store`. The table `instance_setup` has an FK to `instances` with `ON DELETE CASCADE`. MemStore holds a map that `applyRemoveInstance` drops. Both are covered by `storetest` conformance. `ExportConfig` never reads the table.
- **Image facts come from the launcher host, never from a running container.** `internal/jam/imagefacts.Reader` runs `docker image inspect`, `docker create`, `docker cp <ctr>:/home/agent/.init-agent-data -` and `docker rm -f` as argv through `runner.Runner`. It uses the colima backend's own `--context` args (`(*colima.Colima).DockerArgs`). It never runs `start`, `run` or `exec`. The tar is capped (1 MiB, 200 entries, text ≤ 64 KiB). The parsed facts are cached per image id in a 16-entry LRU. Jam sees only the `jam.ImageFactsReader` interface, which is nil with the placeholder launcher.
- **The would-be raise is the real raise's resolution.** `Supervisor.wouldBeRaise` builds the actor `Enroll` would build (one grant on the role) and resolves the kit with `raiseKit`. It compiles context with `compileContext`, the function `Raise` itself uses, for an ephemeral session with no unit and no owner. A role render and a raise therefore cannot drift.
- **Routes** mount through an `AdminOption` (`WithSetupInspect`), inside the operator authenticator like every `/admin/*` route. Each call logs `setup inspected` with the operator, the subject and the sections, never the body.

**Tech Stack:** Go 1.26; `net/http` ServeMux patterns; `text/tabwriter`; `archive/tar`; `gopkg.in/yaml.v3`; the existing `jam` store (MemStore + Postgres via pgx), `sessionctx`, `snippet`, `linediff`, `runner.Fake`, `adminclient`.

**Spec:** [`docs/superpowers/specs/2026-10-24-studio-setup-inspect-design.md`](../specs/2026-10-24-studio-setup-inspect-design.md), all of it except §4's slice-2 UI.

## Spec deviations and interpretations

1. **Subject addressing.** `GET /admin/instances/{id}/setup` and `studio inspect <studio>` take a session id or the label of a live manual session, resolved with `ResolveSession`, the same way `DELETE /admin/coves/{id}` resolves. A gone or unknown studio is 404.
2. **The would-be session of a role render is ephemeral, with no unit and no owner.** Its actor is synthetic (`would-be`) and holds exactly the role's grant, as `Enroll` gives a raise. Message targets are computed for that grant. The personal and standing kinds differ from it only in the boilerplate layer and the owner's addressing. The identity section says so.
3. **The model spec at raise time is not recorded** (no such Instance field exists). The `model` section is always `config` as of now and never `stale`. No Instance field is added in this slice.
4. **Raise records all four snapshot parts:** context, egress, connector and kit. A raise delivers all four, and recording them means a studio that never refreshes still has snapshots. Later deliveries update the parts:
   - `GET /context` updates context.
   - `GET /connector` updates connector, only for an identity that has an instance.
   - A successful `ApplyEgress` in `reconcileEgress` and `Resume` updates egress.
5. **Sources.**
   - A role render's computed sections are `config`: they are computed from config now.
   - `recomputed` is only for a studio part with no snapshot. Its reason is `no snapshot (studio raised before setup recording); recomputed`.
   - `seed` is `image`. `env` and `kit` take the source of their Jam part and append the image part with its own `image …: unavailable (<reason>)` line.
   - A role render's `kit` and `seed` read the image when it is present on the launcher host. That is the meaning of "cached".
6. **"Stale" per section** (studios only):
   - `context`: the snapshot fingerprint differs from a refresh now (`ContextForActor`).
   - `egress`: the snapshot fingerprint differs from `EgressFingerprint(role policy now)`. A note line is added when it also differs from `Instance.Egress`.
   - `env`: the snapshot fingerprint differs from `ConnectorFor` now. A note is added when the cove's reported `Instance.Connector` differs.
   - `destinations`: the reported connector differs from the current one.
   - `kit`: `imageStatus` is `stale`.
   - `model`, `seed`, `mcp` and `policy` are never stale.
7. **Effective egress "kit ceiling ∩ role scope".** With a role policy, each role domain is listed and marked when the kit ceiling (`studio.Ceiling` of the merged kit egress) does not cover it (`role, outside the kit ceiling (refused in-box)`). With no policy, the ceiling is listed as `kit`.
   - **Sealed base entries** are the hardening layer's `allowed_domains.txt`, read from this binary's embedded layer (`assemble.SealedEgressBase`). They are appended at render time to both renders and never stored in a snapshot.
   - The infra list (the Jam host) is not shown.
8. **Env rendering.** The env is shown as the connector holds it.
   - `{base}` and `{host}` stay as placeholders: Jam does not know the broker URL as each cove sees it.
   - `{token}`, `AT_JAM_IDENTITY_TOKEN` and `AT_HARBOR_IDENTITY_TOKEN` read `«identity token»`.
   - "A template that renders only non-secret placeholders" is read strictly. A secret-named key is shown only when its value is placeholders plus at most a `Bearer ` or `token ` scheme word. Any literal text masks it.
9. **The one redaction function is `RedactEnv(key, value)`.**
   - `RedactJSON` applies it to every string in `settings.json`, keyed by its JSON key.
   - `RedactLabel` applies it to label values after re-parsing them. A label is `invalid` when it is non-UTF-8, has control characters or is longer than 256 bytes. A URL value is re-parsed and any userinfo dropped; an unparsable URL is `invalid`.
   - Build args go through it too.
   - Destination upstreams and kit source repos are re-parsed and their userinfo dropped (`stripUserinfo`).
10. **Seed rendering.**
    - Shown in full: `CLAUDE.md`, the files it imports (`@path` lines), `settings.json` (redacted), skills (name and description from `SKILL.md` frontmatter) and the `.refresh` list.
    - Every other seeded file, `.claude.json` included, is listed by path and size only. The reader captures text for every small UTF-8 file, but the renderer shows only these.
11. **Image reads.**
    - Each inspect runs one `docker image inspect` to learn the image id. The seed (`create`/`cp`/`rm`) is cached per id. A seed that failed with a docker error is not cached.
    - An inspect failure maps to `image not present on the launcher host`. `HasKitImage` treats a failed inspect as a miss the same way. A `create`/`cp` failure maps to `seed unreadable: docker error on the launcher host`.
    - `runner.Runner` has no context, so `ctx` is not passed to docker.
12. **Diff direction:** `- this studio, + a raise now`. `identity` is never diffed. When the studio's role is gone, the setup carries `note: the role no longer exists: no diff`.
13. **The JSON carries text bodies and `linediff.Line` diffs, not typed structures.** Slice 2's UI renders the same text, raw or as markdown.
14. **Write-on-change compares fingerprints only.** `at` is when the current fingerprint was first delivered.
15. **MCP.**
    - Kit servers are shown with their name, type, URL or command, an argument count and header names, never header values or args.
    - The intercom tools are a `jam.IntercomTools` constant, pinned to `cove-master mcp`'s registered tools by a test in `cmd/cove-master`.
16. **Alarms** show the name, schedule, next fire time and whether a gate is set. The gate command and the note are not shown.
17. **"Layer budget used"** needs per-layer sizes. `sessionctx.Bundle` gains `Budgets map[string]LayerBudget` (`json:"budgets,omitempty"`). The fingerprint is unchanged, and cove-master ignores the field.
18. **Timeout.** The admin client's setup calls use a 60 s timeout, because a first seed copy runs `docker create`/`cp`.
19. **The context section is the delivered text, unredacted.** The Studio layer of a session's context already carries each destination's upstream verbatim (`sessionctx.Studio`). A destination stored with userinfo in its upstream therefore shows it inside `context`. The agent receives that text either way, and Jam does not refuse such an upstream today (`ValidateDestination`). The `destinations` section strips it. Refusing userinfo upstreams is left out of this slice; it would be a follow-up issue in `issues/`.
20. **Docs.** The new leaf is `docs/usage/jam/inspect.md`, indexed from `docs/usage/jam/INDEX.md`, the section index that owns the jam rows (`docs/INDEX.md` routes only to `usage/INDEX.md`). `session-context.md` and `connector.md` point to it. `coves.md` (294 lines, over budget) is not touched.

## Global Constraints

- Spec §1, verbatim: `StudioSetup` is a list of sections, each `{name, source, stale, at, body}`: `source` is `snapshot` | `recomputed` | `image` | `config` | `unavailable`; `stale` (studios only) is true when the delivered fingerprint differs from what the role would deliver now; `at` is when a snapshot was taken.
- Sections, in this order: `identity`, `context`, `egress`, `destinations`, `env`, `model`, `kit`, `seed`, `mcp`, `policy`.
- **Redaction (verbatim):** "No secret value is rendered or stored. Credentials appear by name. The identity token in connector env is `«identity token»`. Any other env value whose key matches `(?i)token|secret|key|password|passwd|credential` is `«masked»` unless it is a template that renders only non-secret placeholders. Snapshots store the masked form only."
- Snapshot parts: `context`, `egress`, `connector`, `kit`, each `{at, fingerprint, body}`. Each body is capped at 64 KiB and truncated with a marker. A write happens only when the fingerprint differs. A failed write is logged and never fails the delivery. Snapshots are dropped with the instance and are not part of `at-jam export`.
- Image reader: `docker image inspect`, then `docker create` (never started), `docker cp <ctr>:/home/agent/.init-agent-data -`, `docker rm`. Caps are 1 MiB total and ≤ 200 entries; only regular files; text files are valid UTF-8 ≤ 64 KiB. Results are cached per image digest in a bounded LRU. Commands are argv through the existing runner, never a shell, and **never `docker start`/`run`/`exec`**.
- Routes: `GET /admin/roles/{project}/{role}/setup`, `GET /admin/instances/{id}/setup` (`?diff=role`), `?sections=a,b` on both. Admin auth applies as on every `/admin/*` route. Each call is logged `setup inspected` (operator, subject, sections), never the body.
- CLI: `at-jam role inspect <project>/<role> [--json] [--section …]`, `at-jam studio inspect <studio> [--diff] [--json] [--section …]`. Text blocks are headed `== <name> (<source>[, stale][, <at>])`.
- Errors: inspect never fails as a whole. An unknown role or instance is 404. Reasons are fixed strings and never echo a secret or unvalidated repo text.
- Tests are hermetic (`runner.Fake`, MemStore). Postgres tests sit behind the `integration` tag. Use `GOPROXY=https://proxy.golang.org,direct`.
- **Docs** change in the same change as the behaviour. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` must report no more than **722** errors: 720 at the spec commit plus 2 for this plan file.

## Review Focus

1. **A secret placed anywhere an operator or kit author can put one** must not appear in the API JSON, the text render or any stored snapshot. The places: a literal in a destination env template (`GH_TOKEN: ghp_…`), an image `ENV`, `settings.json` env, a build arg, userinfo in a destination upstream, a kit repo or an image label URL, and the raise's identity token. *(Task 1: `TestRedactEnv`, `TestRedactJSON`, `TestRedactLabel`; Task 3: `TestDestinationsBody`; Task 7: `TestKitDetailBody`; Task 8: `TestSetupNeverShowsSecrets`.)*
2. **A studio raised before this feature**, with no snapshots and no `ImageTag`, still renders every section. Its Jam parts are `recomputed` with the fixed reason, its seed is `unavailable`, and nothing errors. *(Task 8: `TestInstanceSetupWithoutSnapshots`.)*
3. **A cove refreshing its context on every wake while config is unchanged** causes no store write. An edit causes exactly one. *(Task 5: `TestContextRefreshWritesOnlyOnChange`.)*
4. **Docker failing, a pruned image, or the placeholder launcher** makes the seed `unavailable` with a fixed reason while the rest renders. A docker error is not cached, so the next inspect retries. *(Task 6: `TestImageFactsSeedDockerErrorNotCached`; Task 8: `TestSetupImageUnavailable`.)*
5. **A studio whose role was deleted** still renders. `--diff` returns a note instead of a 500, and `policy` shows the instance half. *(Task 8: `TestInstanceSetupRoleGone`; Task 9: `TestSetupRoutes`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/snippet/snippet.go` (modify) | `IdentityTokenVars` |
| `internal/jam/setup_redact.go` (create) | `RedactEnv`, `RedactJSON`, `RedactLabel`, `SafeText`, the marks |
| `internal/jam/sessionctx/sessionctx.go` (modify) | `LayerBudget`, `Bundle.Budgets` |
| `internal/jam/setup.go` (create) | `StudioSetup`, `SetupSection`, section/source names, `ParseSetupSections`, `RenderSetupText`, table/text helpers |
| `internal/jam/setup_parts.go` (create) | snapshot-part bodies: `contextBody`, `egressBody`, `sealedEgressBody`, `connectorBody`, `kitRefsBody`, `kitFingerprint`, `chainOrSelf` |
| `internal/jam/setup_config.go` (create) | config bodies: destinations, model, mcp, policy, identity; `IntercomTools` |
| `cmd/cove-master/mcp_test.go` (modify) | pins `jam.IntercomTools` |
| `internal/jam/setup_snapshot.go` (create), `memstate.go`, `memstore.go`, `store.go`, `pgstore.go`, `pgstore_testhelpers.go` (modify), `migrations/0018_instance_setup.sql` (create), `storetest/setup.go` (create), `storetest/conformance.go` (modify) | the snapshot store |
| `internal/jam/setup_record.go` (create), `supervisor.go`, `context_handler.go`, `connector_handler.go` (modify) | write-on-change recording at each delivery |
| `internal/jam/setup_image.go` (create) | `ImageFacts`, `SeedFile`, `ImageFactsReader`, errors |
| `internal/backend/colima/colima.go` (modify) | `(*Colima).DockerArgs` |
| `internal/jam/imagefacts/imagefacts.go` (create) | the docker-side reader, tar caps, LRU |
| `internal/jam/setup_image_bodies.go` (create) | `seedBody`, `imageEnvBody`, `imageLabelsBody`, `kitDetailBody` |
| `internal/jam/setup_render.go` (create), `internal/jam/testdata/setup/{role,studio}.golden` (create) | `SetupDeps`, `wouldBeRaise`, `RoleSetup`, `InstanceSetup`, diff |
| `internal/jam/setup_admin.go` (create), `internal/jam/adminclient/adminclient.go` (modify) | routes, client |
| `internal/assemble/embed.go` (modify) | `SealedEgressBase` |
| `cmd/at-jam/inspect.go` (create), `cmd/at-jam/main.go` (modify) | CLI verbs, wiring |
| `docs/usage/jam/inspect.md` (create); `docs/usage/jam/INDEX.md`, `session-context.md`, `connector.md` (modify) | docs |

---

### Task 1: Redaction

**Files:**
- Modify: `internal/jam/snippet/snippet.go` (after the `legacyTokenVar` const block)
- Create: `internal/jam/setup_redact.go`
- Test: `internal/jam/setup_redact_test.go`, `internal/jam/snippet/snippet_test.go`

**Interfaces:**
- Consumes: `snippet.tokenVar`, `snippet.legacyTokenVar` (unexported consts).
- Produces:
```go
// package snippet
func IdentityTokenVars() []string // {"AT_JAM_IDENTITY_TOKEN", "AT_HARBOR_IDENTITY_TOKEN"}

// package jam
const (
	MarkIdentityToken = "«identity token»"
	MarkMasked        = "«masked»"
	MarkInvalid       = "invalid"
)
func RedactEnv(key, value string) string
func RedactJSON(b []byte) (string, bool)            // indented, redacted; false = not JSON
func RedactLabel(key, value string) (string, string) // display key, display value
func SafeText(s string) string                       // s, or MarkInvalid
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/snippet/snippet_test.go`:

```go
func TestIdentityTokenVars(t *testing.T) {
	if got := IdentityTokenVars(); len(got) != 2 || got[0] != "AT_JAM_IDENTITY_TOKEN" || got[1] != "AT_HARBOR_IDENTITY_TOKEN" {
		t.Fatalf("IdentityTokenVars = %v", got)
	}
}
```

Create `internal/jam/setup_redact_test.go`:

```go
package jam

import (
	"strings"
	"testing"
)

func TestRedactEnv(t *testing.T) {
	for _, c := range []struct{ k, v, want string }{
		{"AT_JAM_IDENTITY_TOKEN", "tok-live-123", MarkIdentityToken},
		{"AT_HARBOR_IDENTITY_TOKEN", "{token}", MarkIdentityToken},
		{"ANTHROPIC_API_KEY", "{token}", MarkIdentityToken},
		{"GH_TOKEN", "Bearer {token}", "Bearer " + MarkIdentityToken},
		{"X_AUTH", "token {token}", "token " + MarkIdentityToken},
		{"ANTHROPIC_BASE_URL", "{base}/anthropic", "{base}/anthropic"},
		{"GH_HOST", "{host}", "{host}"},
		{"PATH", "/usr/local/bin:/usr/bin", "/usr/local/bin:/usr/bin"},
		{"GOPROXY", "https://proxy.golang.org,direct", "https://proxy.golang.org,direct"},
		{"GITHUB_TOKEN", "ghp_live0123456789", MarkMasked},
		{"db_password", "hunter2", MarkMasked},
		{"MY_SECRET_URL", "{base}/x", MarkMasked},       // literal text beside a placeholder
		{"Api-Key", "{base}sk-live", MarkMasked},
		{"KEY_WITH_UNKNOWN", "{sk-live}", MarkMasked}, // only known placeholders count
		{"credential_helper", "store", MarkMasked},
		{"PASSWD", "", MarkMasked},
	} {
		if got := RedactEnv(c.k, c.v); got != c.want {
			t.Errorf("RedactEnv(%q, %q) = %q, want %q", c.k, c.v, got, c.want)
		}
	}
}

func TestRedactJSON(t *testing.T) {
	got, ok := RedactJSON([]byte(`{"env":{"GH_TOKEN":"ghp_SETTINGS","EDITOR":"vi"},"apiKeyHelper":"echo sk-1","model":"opus","hooks":[{"command":"<x>"}]}`))
	if !ok {
		t.Fatal("valid JSON refused")
	}
	for _, bad := range []string{"ghp_SETTINGS", "sk-1"} {
		if strings.Contains(got, bad) {
			t.Errorf("secret %q leaked: %s", bad, got)
		}
	}
	for _, want := range []string{`"EDITOR": "vi"`, `"model": "opus"`, `"<x>"`, MarkMasked} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if _, ok := RedactJSON([]byte("{nope")); ok {
		t.Fatal("invalid JSON accepted")
	}
}

func TestSafeText(t *testing.T) {
	for in, want := range map[string]string{
		"ok": "ok", "a\nb": MarkInvalid, "\xff": MarkInvalid, strings.Repeat("a", 257): MarkInvalid, strings.Repeat("a", 256): strings.Repeat("a", 256),
	} {
		if got := SafeText(in); got != want {
			t.Errorf("SafeText(%q) = %q", in, got)
		}
	}
}

func TestRedactLabel(t *testing.T) {
	for _, c := range []struct{ k, v, wantK, wantV string }{
		{"org.opencontainers.image.source", "https://u:p@github.com/x/y", "org.opencontainers.image.source", "https://github.com/x/y"},
		{"org.opencontainers.image.source", "https://github.com/x/y", "org.opencontainers.image.source", "https://github.com/x/y"},
		{"cove.kit", "my-app@v1", "cove.kit", "my-app@v1"},
		{"evil", "a\nb", "evil", MarkInvalid},
		{"bad\x00key", "v", MarkInvalid, MarkInvalid},
		{"build.token", "abc", "build.token", MarkMasked},
		{"url", "http://[::1", "url", MarkInvalid},
	} {
		k, v := RedactLabel(c.k, c.v)
		if k != c.wantK || v != c.wantV {
			t.Errorf("RedactLabel(%q, %q) = %q, %q; want %q, %q", c.k, c.v, k, v, c.wantK, c.wantV)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/snippet/ -run TestIdentityTokenVars && GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestRedact|TestSafeText'`
Expected: FAIL to compile (`undefined: IdentityTokenVars`, `undefined: RedactEnv`).

- [ ] **Step 3: Implement**

In `internal/jam/snippet/snippet.go`, after the const block that defines `tokenVar` and `legacyTokenVar`:

```go
// IdentityTokenVars are the env vars a studio holds its identity token in,
// the current name first — for showing a connector's env without the token.
func IdentityTokenVars() []string { return []string{tokenVar, legacyTokenVar} }
```

Create `internal/jam/setup_redact.go`:

```go
package jam

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// The fixed marks a setup render shows in place of a value.
const (
	MarkIdentityToken = "«identity token»"
	MarkMasked        = "«masked»"
	MarkInvalid       = "invalid"
)

// secretKeyRE is the spec's secret-named key rule.
var secretKeyRE = regexp.MustCompile(`(?i)token|secret|key|password|passwd|credential`)

// knownPlaceholder is a connector template placeholder (ValidateEnv's set).
var knownPlaceholder = regexp.MustCompile(`\{(?:token|base|host|url)\}`)

// safeTemplateRest is what may remain of a secret-named template once its
// placeholders are removed: nothing, or one auth scheme word.
var safeTemplateRest = regexp.MustCompile(`^(?:(?:Bearer|token) )?$`)

// RedactEnv is THE redaction of one value for a setup render and for a stored
// snapshot (env, image ENV, build args, label values, settings.json strings).
// The identity-token vars and every {token} read MarkIdentityToken; a value
// under a secret-named key is MarkMasked unless it is only placeholders (plus
// at most a "Bearer "/"token " scheme); anything else is shown as is.
func RedactEnv(key, value string) string {
	for _, v := range snippet.IdentityTokenVars() {
		if key == v {
			return MarkIdentityToken
		}
	}
	shown := strings.ReplaceAll(value, "{token}", MarkIdentityToken)
	if !secretKeyRE.MatchString(key) {
		return shown
	}
	if knownPlaceholder.MatchString(value) && safeTemplateRest.MatchString(knownPlaceholder.ReplaceAllString(value, "")) {
		return shown
	}
	return MarkMasked
}

// RedactJSON re-renders a JSON document (settings.json) indented, every
// string value passed through RedactEnv under its nearest object key.
// ok=false when b is not JSON.
func RedactJSON(b []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", false
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(redactJSONValue("", v)); err != nil {
		return "", false
	}
	return strings.TrimRight(out.String(), "\n"), true
}

func redactJSONValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = redactJSONValue(k, x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = redactJSONValue(key, x)
		}
		return out
	case string:
		return RedactEnv(key, t)
	}
	return v
}

// SafeText is s when it is valid UTF-8, at most 256 bytes and free of control
// characters; MarkInvalid otherwise. For names and labels from outside Jam.
func SafeText(s string) string {
	if len(s) > 256 || !utf8.ValidString(s) {
		return MarkInvalid
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return MarkInvalid
		}
	}
	return s
}

// RedactLabel re-parses an image label for display: an unsafe key hides the
// whole label; a URL value loses its userinfo (unparsable: invalid); the
// value then goes through RedactEnv under the label key.
func RedactLabel(key, value string) (string, string) {
	if SafeText(key) == MarkInvalid {
		return MarkInvalid, MarkInvalid
	}
	v := SafeText(value)
	if v == MarkInvalid {
		return key, MarkInvalid
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return key, MarkInvalid
		}
		u.User = nil
		v = u.String()
	}
	return key, RedactEnv(key, v)
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/snippet/ ./internal/jam/ -run 'TestIdentityTokenVars|TestRedact|TestSafeText'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/snippet/snippet.go internal/jam/snippet/snippet_test.go internal/jam/setup_redact.go internal/jam/setup_redact_test.go
git commit -m "feat(jam): setup redaction — one RedactEnv for render and storage"
```

---

### Task 2: The setup model, the text render, and the snapshot-part bodies

**Files:**
- Modify: `internal/jam/sessionctx/sessionctx.go` (`Bundle`, `Compile`)
- Create: `internal/jam/setup.go`, `internal/jam/setup_parts.go`
- Test: `internal/jam/sessionctx/sessionctx_test.go`, `internal/jam/setup_test.go`

**Interfaces:**
- Consumes: Task 1's `RedactEnv`, `SafeText`; `linediff.Line`; `sessionctx.Bundle`, `sessionctx.Dir`, `sessionctx.LayerOrder`; `EgressPolicy`, `EgressCovers`; `snippet.Connector` (`Env`, `GitRoute`, `ModelSpec`, `RewriteTarget()`), `snippet.IdentityTokenVars`; `KitRef`.
- Produces:
```go
// package sessionctx
type LayerBudget struct{ Used, Budget int } // json "used", "budget"
// Bundle gains: Budgets map[string]LayerBudget `json:"budgets,omitempty"`

// package jam
const (SectionIdentity = "identity"; SectionContext = "context"; SectionEgress = "egress"; SectionDestinations = "destinations"
	SectionEnv = "env"; SectionModel = "model"; SectionKit = "kit"; SectionSeed = "seed"; SectionMCP = "mcp"; SectionPolicy = "policy")
const (SourceSnapshot = "snapshot"; SourceRecomputed = "recomputed"; SourceImage = "image"; SourceConfig = "config"; SourceUnavailable = "unavailable")
type SetupSection struct {
	Name, Source string; Stale bool; At time.Time; Reason, Body string
	Diffed bool; Diff []linediff.Line; DiffTooLarge bool
}
type StudioSetup struct { Subject, Note string; Sections []SetupSection }
func SetupSectionNames() []string
func ParseSetupSections(csv string) ([]string, error)
func RenderSetupText(w io.Writer, s StudioSetup)
func unavailable(name, reason string) SetupSection
// helpers (unexported): writeTable(w io.Writer, head []string, rows [][]string); flat(s string) string;
// ensureNL(s string) string; joinOr(ss []string, none string) string; shortDigest(d string) string
func contextBody(b sessionctx.Bundle) string
func egressBody(role *EgressPolicy, ceiling []string, ceilingKnown bool) string
func sealedEgressBody(sealed []string) string
func connectorBody(c snippet.Connector) string
func kitRefsBody(kit KitRef, chain []KitRef, imageTag string) string
func kitFingerprint(kit KitRef, imageTag string) string
func chainOrSelf(chain []KitRef, self KitRef) []KitRef
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/sessionctx/sessionctx_test.go`:

```go
func TestCompileRecordsLayerBudgets(t *testing.T) {
	b := Compile(Inputs{Session: SessionFacts{Kind: KindEphemeral}, Kit: Layer{Core: "abc"}})
	if got := b.Budgets[LayerKit]; got != (LayerBudget{Used: 3, Budget: BudgetKit}) {
		t.Fatalf("kit budget = %+v", got)
	}
	if got := b.Budgets[LayerBoilerplate]; got.Used == 0 || got.Budget != BudgetBoilerplate {
		t.Fatalf("boilerplate budget = %+v", got)
	}
	if _, ok := b.Budgets[LayerJam]; ok {
		t.Fatal("an empty layer has no budget row")
	}
}
```

Create `internal/jam/setup_test.go`:

```go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func TestParseSetupSections(t *testing.T) {
	all, err := ParseSetupSections("")
	if err != nil || strings.Join(all, ",") != "identity,context,egress,destinations,env,model,kit,seed,mcp,policy" {
		t.Fatalf("all = %v, %v", all, err)
	}
	got, err := ParseSetupSections(" policy, egress ,egress")
	if err != nil || strings.Join(got, ",") != "egress,policy" {
		t.Fatalf("got %v, %v (want render order, deduped)", got, err)
	}
	if _, err := ParseSetupSections("egress,bogus"); err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("unknown section err = %v", err)
	}
}

func TestRenderSetupText(t *testing.T) {
	s := StudioSetup{Subject: "studio s1", Note: "n", Sections: []SetupSection{
		{Name: SectionEgress, Source: SourceSnapshot, Stale: true, At: time.Unix(0, 0).UTC(), Body: "policy: kit default\n",
			Diffed: true, Diff: []linediff.Line{{Op: "-", Text: "policy: kit default"}, {Op: "+", Text: "policy: role (1 domain(s))"}, {Op: "…", Text: "4 unchanged line(s)"}}},
		{Name: SectionSeed, Source: SourceUnavailable, Reason: "image not present on the launcher host"},
		{Name: SectionModel, Source: SourceConfig, Body: "x", Diffed: true, Diff: []linediff.Line{{Op: " ", Text: "x"}}},
		{Name: SectionKit, Source: SourceConfig, Body: "k\n", Diffed: true, DiffTooLarge: true},
	}}
	var b strings.Builder
	RenderSetupText(&b, s)
	want := "# studio s1\n(n)\n" +
		"\n== egress (snapshot, stale, 1970-01-01T00:00:00Z)\npolicy: kit default\n-- diff (- this studio, + a raise now)\n- policy: kit default\n+ policy: role (1 domain(s))\n… 4 unchanged line(s)\n" +
		"\n== seed (unavailable)\n(image not present on the launcher host)\n" +
		"\n== model (config)\nx\n-- same as a raise now\n" +
		"\n== kit (config)\nk\n-- diff too large to compute\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestContextBody(t *testing.T) {
	b := sessionctx.Bundle{Core: "# Session context\n", Files: map[string]string{"INDEX.md": "idx\n", "kit/tools.md": "tools"},
		Budgets: map[string]sessionctx.LayerBudget{sessionctx.LayerKit: {Used: 3, Budget: 800}}, Warnings: []string{"kit core is long\nreally"}}
	got := contextBody(b)
	for _, want := range []string{
		"/agent-data/context/CORE.md", "18", "/agent-data/context/kit/tools.md", "5",
		"  kit 3/800 B\n", "warning: kit core is long\\nreally\n",
		"\n--- /agent-data/context/CORE.md\n# Session context\n", "\n--- /agent-data/context/kit/tools.md\ntools\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "INDEX.md\n") > strings.Index(got, "kit/tools.md\n") {
		t.Error("files must be listed CORE.md first, then sorted")
	}
}

func TestEgressBody(t *testing.T) {
	got := egressBody(&EgressPolicy{Domains: []string{"api.github.com", "evil.org"}}, []string{".github.com"}, true)
	for _, want := range []string{"policy: role (2 domain(s))\n", "kit ceiling: 1 domain(s)\n", "api.github.com  role\n", "evil.org        role, outside the kit ceiling (refused in-box)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	got = egressBody(nil, []string{".github.com", "pypi.org"}, true)
	if !strings.Contains(got, "policy: kit default\n") || !strings.Contains(got, "pypi.org     kit\n") {
		t.Errorf("kit default:\n%s", got)
	}
	if got := egressBody(nil, nil, false); !strings.Contains(got, "kit ceiling: unknown\n") {
		t.Errorf("unknown ceiling:\n%s", got)
	}
	if got := sealedEgressBody([]string{".anthropic.com"}); got != "\nsealed base (always on):\nHOST            FROM\n.anthropic.com  sealed\n" {
		t.Errorf("sealed = %q", got)
	}
}

func TestConnectorBodyMasks(t *testing.T) {
	c := snippet.Connector{Env: map[string]string{"GH_TOKEN": "ghp_live", "ANTHROPIC_API_KEY": "{token}", "ANTHROPIC_BASE_URL": "{base}/anthropic"},
		GitRoute: "/git/", ModelSpec: &modelspec.Spec{Name: "claude-default"}}
	got := connectorBody(c)
	want := "ANTHROPIC_API_KEY=«identity token»\nANTHROPIC_BASE_URL={base}/anthropic\nAT_HARBOR_IDENTITY_TOKEN=«identity token»\n" +
		"AT_JAM_IDENTITY_TOKEN=«identity token»\nGH_TOKEN=«masked»\ngit: https://github.com/ → {base}/git/\nmodel-spec: claude-default\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := connectorBody(snippet.Connector{}); !strings.Contains(got, "git: not routed\n") || !strings.Contains(got, "model-spec: none\n") {
		t.Fatalf("empty connector:\n%s", got)
	}
}

func TestKitRefsBody(t *testing.T) {
	kit := KitRef{ID: "my-app", Version: 2, Digest: "0123456789abcdef"}
	got := kitRefsBody(kit, []KitRef{{ID: "default", Version: 1}, {ID: "my-app", Version: 2}}, "cove-kit:0123-asm")
	want := "kit: my-app@v2 digest 0123456789ab\nchain (root first): default@v1 → my-app@v2\nimage tag: cove-kit:0123-asm\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := kitRefsBody(kit, nil, ""); !strings.Contains(got, "chain (root first): my-app@v2\n") || !strings.Contains(got, "image tag: unknown\n") {
		t.Fatalf("unchained: %q", got)
	}
	if got := kitRefsBody(KitRef{}, nil, ""); got != "kit: none (the launcher's static image)\n" {
		t.Fatalf("no kit: %q", got)
	}
	if kitFingerprint(kit, "t") == kitFingerprint(kit, "u") {
		t.Fatal("the image tag must key the kit fingerprint")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/sessionctx/ -run TestCompileRecordsLayerBudgets; GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestParseSetupSections|TestRenderSetupText|TestContextBody|TestEgressBody|TestConnectorBodyMasks|TestKitRefsBody'`
Expected: FAIL to compile (`b.Budgets undefined`, `undefined: StudioSetup`).

- [ ] **Step 3: Implement**

In `internal/jam/sessionctx/sessionctx.go`, add after `Bundle`, and the field to `Bundle`:

```go
// LayerBudget is one emitted layer's always-on core size against its budget.
type LayerBudget struct {
	Used   int `json:"used"`
	Budget int `json:"budget"`
}
```

```go
	Warnings    []string          `json:"warnings,omitempty"`
	// Budgets is each emitted layer's core size and budget (setup inspection);
	// not part of the fingerprint.
	Budgets map[string]LayerBudget `json:"budgets,omitempty"`
```

In `Compile`, make the bundle with `Budgets: map[string]LayerBudget{}`:

```go
	b := Bundle{Files: map[string]string{}, Layers: map[string]string{}, Budgets: map[string]LayerBudget{}}
```

and, right after the truncation `if len(c) > s.budget { … }` block:

```go
		b.Budgets[s.name] = LayerBudget{Used: len(c), Budget: s.budget}
```

Create `internal/jam/setup.go`:

```go
package jam

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aethons-tools/cove/internal/jam/linediff"
)

// Setup section names, in render order (spec §1).
const (
	SectionIdentity     = "identity"
	SectionContext      = "context"
	SectionEgress       = "egress"
	SectionDestinations = "destinations"
	SectionEnv          = "env"
	SectionModel        = "model"
	SectionKit          = "kit"
	SectionSeed         = "seed"
	SectionMCP          = "mcp"
	SectionPolicy       = "policy"
)

// Setup section sources.
const (
	SourceSnapshot    = "snapshot"    // what Jam recorded delivering
	SourceRecomputed  = "recomputed"  // a studio part with no snapshot, rendered from config now
	SourceImage       = "image"       // read from the built image on the launcher host
	SourceConfig      = "config"      // config as of now
	SourceUnavailable = "unavailable" // could not be produced; Reason says why
)

var setupSectionOrder = []string{SectionIdentity, SectionContext, SectionEgress, SectionDestinations, SectionEnv,
	SectionModel, SectionKit, SectionSeed, SectionMCP, SectionPolicy}

// SetupSection is one section of a StudioSetup. Body is plain text (already
// redacted); Diff, when Diffed, is the line diff of this studio's body (-)
// against a raise now (+).
type SetupSection struct {
	Name         string          `json:"name"`
	Source       string          `json:"source"`
	Stale        bool            `json:"stale,omitempty"`
	At           time.Time       `json:"at,omitzero"`
	Reason       string          `json:"reason,omitempty"`
	Body         string          `json:"body"`
	Diffed       bool            `json:"diffed,omitempty"`
	Diff         []linediff.Line `json:"diff,omitempty"`
	DiffTooLarge bool            `json:"diff_too_large,omitempty"`
}

// StudioSetup is what an agent is set up with: for a role ("role p/r", what a
// raise now would get) or a studio ("studio <id>", what it got).
type StudioSetup struct {
	Subject  string         `json:"subject"`
	Note     string         `json:"note,omitempty"`
	Sections []SetupSection `json:"sections"`
}

// SetupSectionNames lists every section, in render order.
func SetupSectionNames() []string { return slices.Clone(setupSectionOrder) }

// ParseSetupSections reads a ?sections= / --section list: comma-separated,
// deduped, returned in render order; empty = all. An unknown name is an error
// naming it.
func ParseSetupSections(csv string) ([]string, error) {
	want := map[string]bool{}
	for n := range strings.SplitSeq(csv, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !slices.Contains(setupSectionOrder, n) {
			return nil, fmt.Errorf("unknown section %q (want %s)", n, strings.Join(setupSectionOrder, ", "))
		}
		want[n] = true
	}
	if len(want) == 0 {
		return SetupSectionNames(), nil
	}
	var out []string
	for _, n := range setupSectionOrder {
		if want[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

// unavailable is a section that could not be produced, with a fixed reason.
func unavailable(name, reason string) SetupSection {
	return SetupSection{Name: name, Source: SourceUnavailable, Reason: reason}
}

// RenderSetupText writes the CLI's text form: one block per section headed
// "== <name> (<source>[, stale][, <at>])", the reason in parentheses, the
// body, then the diff when one was asked for.
func RenderSetupText(w io.Writer, s StudioSetup) {
	fmt.Fprintf(w, "# %s\n", s.Subject)
	if s.Note != "" {
		fmt.Fprintf(w, "(%s)\n", s.Note)
	}
	for _, sec := range s.Sections {
		head := sec.Source
		if sec.Stale {
			head += ", stale"
		}
		if !sec.At.IsZero() {
			head += ", " + sec.At.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "\n== %s (%s)\n", sec.Name, head)
		if sec.Reason != "" {
			fmt.Fprintf(w, "(%s)\n", sec.Reason)
		}
		if sec.Body != "" {
			io.WriteString(w, ensureNL(sec.Body))
		}
		switch {
		case !sec.Diffed:
		case sec.DiffTooLarge:
			fmt.Fprintln(w, "-- diff too large to compute")
		case !diffChanged(sec.Diff):
			fmt.Fprintln(w, "-- same as a raise now")
		default:
			fmt.Fprintln(w, "-- diff (- this studio, + a raise now)")
			for _, l := range sec.Diff {
				fmt.Fprintf(w, "%s %s\n", l.Op, l.Text)
			}
		}
	}
}

func diffChanged(lines []linediff.Line) bool {
	return slices.ContainsFunc(lines, func(l linediff.Line) bool { return l.Op == "+" || l.Op == "-" })
}

// writeTable writes an aligned table (two spaces between columns).
func writeTable(w io.Writer, head []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// flat keeps a value on one line, so it cannot fake a row or a header.
func flat(s string) string { return strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(s) }

func ensureNL(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func joinOr(ss []string, none string) string {
	if len(ss) == 0 {
		return none
	}
	return strings.Join(ss, ", ")
}

// shortDigest is the first 12 characters of a digest or id ("unknown" for none).
func shortDigest(d string) string {
	switch {
	case d == "":
		return "unknown"
	case len(d) > 12:
		return d[:12]
	}
	return d
}
```

Create `internal/jam/setup_parts.go`:

```go
package jam

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// contextBody renders a session-context bundle at its real paths: the file
// list with sizes, each layer's core budget used, the compile warnings, then
// every file in full (CORE.md first).
func contextBody(b sessionctx.Bundle) string {
	files := map[string]string{"CORE.md": b.Core}
	maps.Copy(files, b.Files)
	paths := append([]string{"CORE.md"}, slices.Sorted(maps.Keys(b.Files))...)
	var sb strings.Builder
	rows := make([][]string, 0, len(paths))
	for _, p := range paths {
		rows = append(rows, []string{sessionctx.Dir + "/" + p, fmt.Sprint(len(files[p]))})
	}
	writeTable(&sb, []string{"FILE", "BYTES"}, rows)
	if len(b.Budgets) > 0 {
		sb.WriteString("\ncore budget used:\n")
		for _, n := range sessionctx.LayerOrder() {
			if lb, ok := b.Budgets[n]; ok {
				fmt.Fprintf(&sb, "  %s %d/%d B\n", n, lb.Used, lb.Budget)
			}
		}
	}
	for _, w := range b.Warnings {
		fmt.Fprintf(&sb, "warning: %s\n", flat(w))
	}
	for _, p := range paths {
		fmt.Fprintf(&sb, "\n--- %s/%s\n%s", sessionctx.Dir, p, ensureNL(files[p]))
	}
	return sb.String()
}

// egressBody renders an egress policy as it applies in the box: a role list
// (each domain marked when the kit ceiling does not cover it — the in-box
// apply refuses those), or the kit default (the ceiling itself).
func egressBody(role *EgressPolicy, ceiling []string, ceilingKnown bool) string {
	var sb strings.Builder
	var rows [][]string
	if role == nil {
		sb.WriteString("policy: kit default\n")
		for _, h := range ceiling {
			rows = append(rows, []string{flat(h), "kit"})
		}
	} else {
		fmt.Fprintf(&sb, "policy: role (%d domain(s))\n", len(role.Domains))
		for _, h := range role.Domains {
			from := "role"
			if ceilingKnown && !slices.ContainsFunc(ceiling, func(c string) bool { return EgressCovers(c, h) }) {
				from = "role, outside the kit ceiling (refused in-box)"
			}
			rows = append(rows, []string{flat(h), from})
		}
	}
	if ceilingKnown {
		fmt.Fprintf(&sb, "kit ceiling: %d domain(s)\n", len(ceiling))
	} else {
		sb.WriteString("kit ceiling: unknown\n")
	}
	if len(rows) > 0 {
		writeTable(&sb, []string{"HOST", "FROM"}, rows)
	}
	return sb.String()
}

// sealedEgressBody is the sealed base list's rows, appended at render time
// (never stored in a snapshot): the hardening layer's always-on entries.
func sealedEgressBody(sealed []string) string {
	if len(sealed) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nsealed base (always on):\n")
	rows := make([][]string, 0, len(sealed))
	for _, h := range sealed {
		rows = append(rows, []string{h, "sealed"})
	}
	writeTable(&sb, []string{"HOST", "FROM"}, rows)
	return sb.String()
}

// connectorBody renders the env an agent spawn gets from its connector, every
// value through RedactEnv (the identity-token vars included), then git
// routing and the delivered model-spec's name.
func connectorBody(c snippet.Connector) string {
	env := map[string]string{}
	for _, k := range snippet.IdentityTokenVars() {
		env[k] = "{token}"
	}
	maps.Copy(env, c.Env)
	var sb strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&sb, "%s=%s\n", flat(k), flat(RedactEnv(k, env[k])))
	}
	if c.GitRoute != "" {
		fmt.Fprintf(&sb, "git: %s → {base}%s\n", flat(c.RewriteTarget()), flat(c.GitRoute))
	} else {
		sb.WriteString("git: not routed\n")
	}
	if c.ModelSpec != nil {
		fmt.Fprintf(&sb, "model-spec: %s\n", flat(c.ModelSpec.Name))
	} else {
		sb.WriteString("model-spec: none\n")
	}
	return sb.String()
}

// kitRefsBody is what a raise recorded of the kit it ran: the kit ref, its
// chain (root first), and the image tag.
func kitRefsBody(kit KitRef, chain []KitRef, imageTag string) string {
	if kit.ID == "" {
		return "kit: none (the launcher's static image)\n"
	}
	refs := chainOrSelf(chain, kit)
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.String()
	}
	if imageTag == "" {
		imageTag = "unknown"
	}
	return fmt.Sprintf("kit: %s digest %s\nchain (root first): %s\nimage tag: %s\n", kit, shortDigest(kit.Digest), strings.Join(names, " → "), imageTag)
}

// kitFingerprint keys the kit snapshot part.
func kitFingerprint(kit KitRef, imageTag string) string {
	return kit.String() + "|" + kit.Digest + "|" + imageTag
}

// chainOrSelf is a recorded chain, or the kit alone when unchained.
func chainOrSelf(chain []KitRef, self KitRef) []KitRef {
	if len(chain) > 0 {
		return chain
	}
	return []KitRef{{ID: self.ID, Version: self.Version}}
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/sessionctx/ ./internal/jam/ -run 'TestCompile|TestParseSetupSections|TestRenderSetupText|TestContextBody|TestEgressBody|TestConnectorBodyMasks|TestKitRefsBody'`
Expected: PASS. Then run the whole `./internal/jam/sessionctx/ ./internal/agentrun/ ./internal/connect/` packages: the new field must not break any bundle test.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionctx/sessionctx.go internal/jam/sessionctx/sessionctx_test.go internal/jam/setup.go internal/jam/setup_parts.go internal/jam/setup_test.go
git commit -m "feat(jam): setup model, text render and snapshot-part bodies"
```

---

### Task 3: Config section bodies and the intercom tool list

**Files:**
- Create: `internal/jam/setup_config.go`
- Modify: `cmd/cove-master/mcp_test.go`
- Test: `internal/jam/setup_config_test.go`

**Interfaces:**
- Consumes: Task 2's `writeTable`, `flat`, `joinOr`; Task 1's `SafeText`; `ScopesFor`, `Destination` (`ClientEnv`, `GitRouted`), `ApplyMethod`, `ApplyCustom`, `InboundSpec`, `OutboundSpec`, `snippet.RewriteTargetFor`, `ModelSpec`, `kit.MCPServer`, `Role`, `Instance`, `EscalationTier`, `ProjectName`, `orDefaultProject`, `SessionKindPersonal`, `SessionKindStanding`.
- Produces:
```go
var IntercomTools []string // sorted tool names of cove-master mcp
func destinationsBody(store Store, a Actor) string
func stripUserinfo(raw string) string
func modelBody(ms *ModelSpec) string
func mcpBody(servers map[string]kit.MCPServer, kitKnown bool) string
func rolePolicyBody(store Store, project string, r Role) string
func instancePolicyBody(inst Instance) string
func roleIdentityBody(store Store, project, role string) string
func instanceIdentityBody(store Store, inst Instance) string
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_config_test.go`:

```go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestDestinationsBody(t *testing.T) {
	st := NewMemStore()
	_ = st.AddDestination(Destination{Name: "gh", Route: "/gh/", Upstream: "https://user:pw@api.github.com", IdentityIn: ApplyBearer,
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "token {cred}"}, CredName: "gh-pat",
		Env: map[string]string{"GH_TOKEN": "ghp_SECRET", "GH_HOST": "{host}"}, Git: true, AllowPaths: []string{"/repos/*"}})
	if err := st.PutRole("default", Role{Name: "r", Scope: Scope{Destinations: []string{"gh", "ghost"}, Credentials: map[string]string{"gh": "gh-bot"}}}); err != nil {
		t.Fatal(err)
	}
	got := destinationsBody(st, Actor{Grants: []Grant{{Project: "default", Role: "r"}}})
	for _, want := range []string{"gh\n", "  route:       /gh/\n", "  upstream:    https://api.github.com\n", "  identity-in: bearer\n",
		"  apply:       custom (header Authorization, template \"token {cred}\")\n", "  credential:  gh-bot\n", "  git:         routed (https://api.github.com/)\n",
		"  allow-paths: /repos/*\n", "  env keys:    GH_HOST, GH_TOKEN\n", "ghost: not configured (skipped)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"ghp_SECRET", "user:pw"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked", bad)
		}
	}
	if got := destinationsBody(st, Actor{}); got != "no destinations in scope\n" {
		t.Errorf("empty scope = %q", got)
	}
}

func TestModelAndMCPBodies(t *testing.T) {
	if got := modelBody(nil); got != "model-spec: none (the harness's built-in defaults)\n" {
		t.Errorf("nil spec = %q", got)
	}
	ms := &ModelSpec{Name: "opus", Type: "claude", Version: "2.1.0"}
	ms.Principal.Credential = "anthropic-key"
	if got := modelBody(ms); !strings.Contains(got, "name: opus") || !strings.Contains(got, "credential: anthropic-key") {
		t.Errorf("spec = %q", got)
	}
	got := mcpBody(map[string]kit.MCPServer{
		"docs":   {Type: "stdio", Command: "docs-mcp", Args: []string{"--key", "sk-live"}},
		"search": {Type: "http", URL: "https://u:p@search.example/mcp", Headers: map[string]string{"Authorization": "Bearer ${SEARCH_TOKEN}"}},
	}, true)
	for _, want := range []string{"docs", "docs-mcp (+2 args)", "https://search.example/mcp", "Authorization", "intercom (cove-master mcp, always loaded): alarm_clear,"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"sk-live", "SEARCH_TOKEN", "u:p"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked:\n%s", bad, got)
		}
	}
	if got := mcpBody(nil, false); !strings.Contains(got, "kit servers: unknown (no kit resolved)\n") {
		t.Errorf("unknown kit = %q", got)
	}
}

func TestPolicyBodies(t *testing.T) {
	st := NewMemStore()
	if err := st.SetEscalationPolicy("default", "", []EscalationTier{{Targets: []string{"user:alice"}, Timeout: 30 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	r := Role{Name: "r", Allocation: RoleAllocation{MaxEphemeral: 2, Standing: []StandingSession{{Name: "bot"}}},
		TurnEnd: TurnEndPolicy{IdleTimeout: time.Hour, OnIdle: OnIdleTeardown}, Collaborate: CollaboratePolicy{SSH: "members"}}
	got := rolePolicyBody(st, "default", r)
	for _, want := range []string{
		"allocation: max-ephemeral 2, max-personal 0, max-personal-per-owner pool only\n",
		"personal idle: idle-after 4h0m0s, nag-every 24h0m0s, reclaim-after never\n",
		"standing: bot\n", "turn-end: idle-timeout 1h0m0s, on-idle teardown, time-zone UTC\n",
		"collaborate: preview members, ssh members\n", "escalation (default): 1. user:alice (after 30m0s)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	inst := Instance{IdleOverride: &IdleOverride{Duration: 45 * time.Minute, Scope: IdleScopeAlways},
		Alarms: []Alarm{{Name: "nightly", Schedule: "0 3 * * *", NextAt: time.Unix(0, 0).UTC(), Gate: "curl -H 'x: sk-live' …", Note: "private"}}}
	got = instancePolicyBody(inst)
	if !strings.Contains(got, "idle override: 45m0s (always)\n") || !strings.Contains(got, "  nightly  0 3 * * *  next 1970-01-01T00:00:00Z  gate yes\n") {
		t.Errorf("instance policy:\n%s", got)
	}
	if strings.Contains(got, "sk-live") || strings.Contains(got, "private") {
		t.Errorf("a gate command or note leaked:\n%s", got)
	}
}

func TestIdentityBodies(t *testing.T) {
	st := NewMemStore()
	if got := roleIdentityBody(st, "", "dev"); got != "project: default\nrole: dev\nsession kind: ephemeral (a would-be raise: no unit, no owner)\n" {
		t.Errorf("role identity = %q", got)
	}
	inst := Instance{ActorID: "s1", Project: "default", Role: "dev", Name: "bot", SessionKind: SessionKindStanding, Phase: PhaseLive, RaisedAt: time.Unix(0, 0).UTC()}
	want := "project: default\nrole: dev\nsession kind: standing (name bot)\nstudio: s1\nphase: live\nraised at: 1970-01-01T00:00:00Z\n"
	if got := instanceIdentityBody(st, inst); got != want {
		t.Errorf("instance identity = %q", got)
	}
}
```

Append to `cmd/cove-master/mcp_test.go` (add `"slices"` and `"github.com/aethons-tools/cove/internal/jam"` to its imports):

```go
// TestIntercomToolsMatchJam pins jam.IntercomTools — what Jam's setup
// inspection says every session is offered — to the tools this server
// registers.
func TestIntercomToolsMatchJam(t *testing.T) {
	env := map[string]string{"AT_JAM_RUNTIME_ADDR": "http://127.0.0.1:1", "AT_JAM_IDENTITY_TOKEN": "tok"}
	sess := connectMCP(t, func(k string) string { return env[k] })
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, jam.IntercomTools) {
		t.Fatalf("cove-master mcp tools = %v\njam.IntercomTools  = %v", got, jam.IntercomTools)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestDestinationsBody|TestModelAndMCPBodies|TestPolicyBodies|TestIdentityBodies'`
Expected: FAIL to compile (`undefined: destinationsBody`).

- [ ] **Step 3: Implement**

Create `internal/jam/setup_config.go`:

```go
package jam

import (
	"cmp"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// IntercomTools are the tools the guaranteed intercom MCP server
// (`cove-master mcp`) offers every session, sorted. cmd/cove-master pins it
// (TestIntercomToolsMatchJam).
var IntercomTools = []string{"alarm_clear", "alarm_list", "alarm_set", "amend", "ask", "asks", "board_apply", "board_create",
	"board_read", "board_snapshot", "boards", "call_in", "commit", "end", "escalate", "expose", "exposures", "fetch",
	"idle_timeout", "leave", "list_targets", "read", "report", "send", "unexpose", "withdraw"}

// destinationsBody renders each destination in a's scopes: how it is reached
// and authenticated, by credential NAME, and which env keys it sets.
func destinationsBody(store Store, a Actor) string {
	byName := map[string]Destination{}
	for _, d := range store.ListDestinations() {
		byName[d.Name] = d
	}
	inScope, creds := map[string]bool{}, map[string]string{}
	for _, s := range ScopesFor(store, a) {
		for _, n := range s.Destinations {
			inScope[n] = true
			if c := s.Credentials[n]; c != "" {
				creds[n] = c
			}
		}
	}
	if len(inScope) == 0 {
		return "no destinations in scope\n"
	}
	var sb strings.Builder
	for _, n := range slices.Sorted(maps.Keys(inScope)) {
		d, ok := byName[n]
		if !ok {
			fmt.Fprintf(&sb, "%s: not configured (skipped)\n", SafeText(n))
			continue
		}
		git := "no"
		if d.GitRouted() {
			git = "routed (" + flat(snippet.RewriteTargetFor(d.Upstream)) + ")"
		}
		fmt.Fprintf(&sb, "%s\n", SafeText(d.Name))
		fmt.Fprintf(&sb, "  route:       %s\n", SafeText(d.Route))
		fmt.Fprintf(&sb, "  upstream:    %s\n", stripUserinfo(d.Upstream))
		fmt.Fprintf(&sb, "  identity-in: %s\n", inboundText(d.IdentityIn, d.IdentityInSpec))
		fmt.Fprintf(&sb, "  apply:       %s\n", outboundText(d.Apply, d.ApplySpec))
		fmt.Fprintf(&sb, "  credential:  %s\n", cmp.Or(SafeText(cmp.Or(creds[n], d.CredName)), "none"))
		fmt.Fprintf(&sb, "  git:         %s\n", git)
		fmt.Fprintf(&sb, "  allow-paths: %s\n", joinOr(d.AllowPaths, "any"))
		fmt.Fprintf(&sb, "  env keys:    %s\n", joinOr(slices.Sorted(maps.Keys(d.ClientEnv())), "none"))
	}
	return sb.String()
}

func inboundText(m ApplyMethod, s *InboundSpec) string {
	if m == ApplyCustom && s != nil {
		return fmt.Sprintf("custom (header %s, encoding %s)", SafeText(s.Header), cmp.Or(string(s.Encoding), "raw"))
	}
	return cmp.Or(string(m), "none")
}

func outboundText(m ApplyMethod, s *OutboundSpec) string {
	if m == ApplyCustom && s != nil {
		return fmt.Sprintf("custom (header %s, template %q)", SafeText(s.Header), SafeText(s.Template))
	}
	return cmp.Or(string(m), "none")
}

// stripUserinfo re-parses a URL for display without its userinfo; an
// unparsable or unsafe one is MarkInvalid.
func stripUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return MarkInvalid
	}
	u.User = nil
	return SafeText(u.String())
}

// modelBody renders a resolved model-spec (names only — never a secret).
func modelBody(ms *ModelSpec) string {
	if ms == nil {
		return "model-spec: none (the harness's built-in defaults)\n"
	}
	b, err := yaml.Marshal(ms)
	if err != nil {
		return "model-spec: " + flat(ms.Name) + " (unrenderable)\n"
	}
	return string(b)
}

// mcpBody renders the kit's MCP servers (no args, no header values) and the
// intercom tools every session is offered.
func mcpBody(servers map[string]kit.MCPServer, kitKnown bool) string {
	var sb strings.Builder
	switch {
	case !kitKnown:
		sb.WriteString("kit servers: unknown (no kit resolved)\n")
	case len(servers) == 0:
		sb.WriteString("kit servers: none\n")
	default:
		sb.WriteString("kit servers:\n")
		rows := make([][]string, 0, len(servers))
		for _, n := range slices.Sorted(maps.Keys(servers)) {
			s := servers[n]
			where := stripUserinfo(s.URL)
			if s.Type == "stdio" {
				where = SafeText(s.Command)
				if len(s.Args) > 0 {
					where += fmt.Sprintf(" (+%d args)", len(s.Args))
				}
			}
			rows = append(rows, []string{"  " + SafeText(n), SafeText(s.Type), where, joinOr(slices.Sorted(maps.Keys(s.Headers)), "-")})
		}
		writeTable(&sb, []string{"  NAME", "TYPE", "URL/COMMAND", "HEADERS"}, rows)
	}
	fmt.Fprintf(&sb, "intercom (cove-master mcp, always loaded): %s\n", strings.Join(IntercomTools, ", "))
	return sb.String()
}

// rolePolicyBody renders a role's policy and its project's escalation chain.
func rolePolicyBody(store Store, project string, r Role) string {
	var sb strings.Builder
	a := r.Allocation
	maxEph, perOwner := "unset", "pool only"
	if a.MaxEphemeral > 0 {
		maxEph = fmt.Sprint(a.MaxEphemeral)
	}
	if a.MaxPersonalPerOwner > 0 {
		perOwner = fmt.Sprint(a.MaxPersonalPerOwner)
	}
	fmt.Fprintf(&sb, "allocation: max-ephemeral %s, max-personal %d, max-personal-per-owner %s\n", maxEph, a.MaxPersonal, perOwner)
	idle, nag, reclaim := a.PersonalIdle()
	fmt.Fprintf(&sb, "personal idle: idle-after %s, nag-every %s, reclaim-after %s\n", idle, nag, durOr(reclaim, "never"))
	names := make([]string, 0, len(a.Standing))
	for _, s := range a.Standing {
		names = append(names, SafeText(s.Name))
	}
	fmt.Fprintf(&sb, "standing: %s\n", joinOr(names, "none"))
	te := r.TurnEnd
	fmt.Fprintf(&sb, "turn-end: idle-timeout %s, on-idle %s, time-zone %s\n", durOr(te.IdleTimeout, "none"), te.Action(), cmp.Or(te.TimeZone, "UTC"))
	fmt.Fprintf(&sb, "collaborate: preview %s, ssh %s\n", cmp.Or(r.Collaborate.Preview, "members"), cmp.Or(r.Collaborate.SSH, "none"))
	p, _ := store.GetProject(orDefaultProject(project))
	fmt.Fprintf(&sb, "escalation (default): %s\n", tiersText(p.Escalation))
	for _, c := range slices.Sorted(maps.Keys(p.EscalationByCategory)) {
		fmt.Fprintf(&sb, "escalation (%s): %s\n", SafeText(c), tiersText(p.EscalationByCategory[c]))
	}
	return sb.String()
}

// instancePolicyBody renders a studio's own policy state: its idle override
// and its alarms (name, schedule, next fire, gated — never the gate or note).
func instancePolicyBody(inst Instance) string {
	var sb strings.Builder
	if o := inst.IdleOverride; o != nil {
		fmt.Fprintf(&sb, "idle override: %s (%s)\n", durOr(o.Duration, "off"), o.Scope)
	} else {
		sb.WriteString("idle override: none\n")
	}
	if len(inst.Alarms) == 0 {
		sb.WriteString("alarms: none\n")
		return sb.String()
	}
	sb.WriteString("alarms:\n")
	for _, al := range inst.Alarms {
		next, gate := "-", "no"
		if !al.NextAt.IsZero() {
			next = al.NextAt.UTC().Format(time.RFC3339)
		}
		if al.Gate != "" {
			gate = "yes"
		}
		fmt.Fprintf(&sb, "  %s  %s  next %s  gate %s\n", SafeText(al.Name), SafeText(al.Schedule), next, gate)
	}
	return sb.String()
}

func tiersText(ts []EscalationTier) string {
	if len(ts) == 0 {
		return "none"
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("%d. %s (after %s)", i+1, flat(strings.Join(t.Targets, ", ")), t.Timeout)
	}
	return strings.Join(parts, "; ")
}

func durOr(d time.Duration, zero string) string {
	if d <= 0 {
		return zero
	}
	return d.String()
}

func roleIdentityBody(store Store, project, role string) string {
	return fmt.Sprintf("project: %s\nrole: %s\nsession kind: ephemeral (a would-be raise: no unit, no owner)\n", ProjectName(store, orDefaultProject(project)), role)
}

func instanceIdentityBody(store Store, inst Instance) string {
	kind := cmp.Or(inst.SessionKind, "ephemeral")
	switch inst.SessionKind {
	case SessionKindStanding:
		kind += " (name " + SafeText(inst.Name) + ")"
	case SessionKindPersonal:
		kind += " (owner " + SafeText(inst.Owner) + ")"
	}
	if inst.Unit != "" {
		kind += ", unit " + SafeText(inst.Unit)
	}
	return fmt.Sprintf("project: %s\nrole: %s\nsession kind: %s\nstudio: %s\nphase: %s\nraised at: %s\n",
		ProjectName(store, inst.Project), inst.Role, kind, inst.ActorID, inst.Phase, inst.RaisedAt.UTC().Format(time.RFC3339))
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestDestinationsBody|TestModelAndMCPBodies|TestPolicyBodies|TestIdentityBodies' && GOPROXY=https://proxy.golang.org,direct go test ./cmd/cove-master/ -run TestIntercomToolsMatchJam`
Expected: PASS. If the cove-master test fails, the server's tool set changed: update `IntercomTools` (sorted), not the test.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_config.go internal/jam/setup_config_test.go cmd/cove-master/mcp_test.go
git commit -m "feat(jam): setup config sections (destinations, model, mcp, policy, identity)"
```

---

### Task 4: The snapshot store

**Files:**
- Create: `internal/jam/setup_snapshot.go`, `internal/jam/migrations/0018_instance_setup.sql`, `internal/jam/storetest/setup.go`
- Modify: `internal/jam/store.go` (`Store` embeds the new interface), `internal/jam/memstate.go` (field, init, `applyRemoveInstance`), `internal/jam/pgstore.go` (`PutSetupSnapshot`, load), `internal/jam/pgstore_testhelpers.go` (TRUNCATE + reset), `internal/jam/storetest/conformance.go` (register)
- Test: `internal/jam/storetest/setup.go` (run by `TestMemStoreConformance` and, under `integration`, `TestPostgresStoreConformance`)

**Interfaces:**
- Consumes: `memState.instances`, `ErrNotFound`, `PostgresStore.exec`.
- Produces:
```go
const (SetupPartContext = "context"; SetupPartEgress = "egress"; SetupPartConnector = "connector"; SetupPartKit = "kit")
const MaxSetupBody = 64 << 10
type SetupSnapshot struct { ActorID, Part string; At time.Time; Fingerprint, Body string }
type SetupSnapshotStore interface {
	PutSetupSnapshot(s SetupSnapshot) error   // upsert by (ActorID, Part); ErrNotFound without an instance
	SetupSnapshots(actorID string) []SetupSnapshot // sorted by Part
}
```

- [ ] **Step 1: Write the failing conformance test**

Create `internal/jam/storetest/setup.go`:

```go
package storetest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// runSetupConformance exercises the SetupSnapshotStore contract.
func runSetupConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	at := time.Unix(1_000_000, 0).UTC()
	withInstance := func(t *testing.T) jam.Store {
		t.Helper()
		s := newStore(t)
		if err := s.PutInstance(jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	snap := func(part, fp string) jam.SetupSnapshot {
		return jam.SetupSnapshot{ActorID: "cove-1", Part: part, At: at, Fingerprint: fp, Body: part + " body " + fp}
	}

	t.Run("setup_snapshot_put_list_replace", func(t *testing.T) {
		s := withInstance(t)
		for _, p := range []jam.SetupSnapshot{snap(jam.SetupPartKit, "k1"), snap(jam.SetupPartContext, "c1"), snap(jam.SetupPartContext, "c2")} {
			if err := s.PutSetupSnapshot(p); err != nil {
				t.Fatalf("Put %s: %v", p.Part, err)
			}
		}
		got := s.SetupSnapshots("cove-1")
		if len(got) != 2 || got[0] != snap(jam.SetupPartContext, "c2") || got[1] != snap(jam.SetupPartKit, "k1") {
			t.Fatalf("snapshots = %+v (want context c2, kit k1, sorted by part)", got)
		}
		if len(s.SetupSnapshots("nobody")) != 0 {
			t.Fatal("an unknown instance must list nothing")
		}
	})

	t.Run("setup_snapshot_refusals", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(jam.SetupSnapshot{ActorID: "ghost", Part: jam.SetupPartEgress, At: at}); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("no instance: err = %v, want ErrNotFound", err)
		}
		if err := s.PutSetupSnapshot(snap("bogus", "x")); err == nil {
			t.Fatal("an unknown part must be refused")
		}
		big := snap(jam.SetupPartContext, "c")
		big.Body = strings.Repeat("x", jam.MaxSetupBody+1)
		if err := s.PutSetupSnapshot(big); err == nil {
			t.Fatal("a body over MaxSetupBody must be refused (the caller caps it)")
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("a refused write must store nothing")
		}
	})

	t.Run("setup_snapshot_dropped_with_instance", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(snap(jam.SetupPartEgress, "d:a.com")); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveInstance("cove-1"); err != nil {
			t.Fatal(err)
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("snapshots must go with their instance")
		}
		if err := s.PutInstance(jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("a re-raised id must start without the old snapshots")
		}
	})

	t.Run("setup_snapshot_not_exported", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(snap(jam.SetupPartConnector, "UNIQUE-FP")); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(s.ExportConfig())
		if strings.Contains(string(b), "UNIQUE-FP") {
			t.Fatal("snapshots are runtime state: never in the config export")
		}
	})
}
```

In `internal/jam/storetest/conformance.go`, `RunConformance`, add after `runPushConformance(t, newStore)`:

```go
	runSetupConformance(t, newStore)
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestMemStoreConformance`
Expected: FAIL to compile (`undefined: jam.SetupSnapshot`).

- [ ] **Step 3: Implement**

Create `internal/jam/setup_snapshot.go`:

```go
package jam

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Setup snapshot parts: what Jam delivers to a studio and records, masked.
const (
	SetupPartConnector = "connector" // GET /connector (and the raise)
	SetupPartContext   = "context"   // GET /context (and the raise)
	SetupPartEgress    = "egress"    // a successful ApplyEgress (and the raise)
	SetupPartKit       = "kit"       // the raise: kit ref, chain, image tag
)

var setupParts = []string{SetupPartConnector, SetupPartContext, SetupPartEgress, SetupPartKit}

// MaxSetupBody caps a snapshot body (RecordSetupPart truncates to it).
const MaxSetupBody = 64 << 10

// SetupSnapshot is the latest delivery of one part to one studio: when, its
// fingerprint, and the already-masked rendered body. Runtime state: never
// exported, dropped with the instance.
type SetupSnapshot struct {
	ActorID     string    `json:"actor_id"`
	Part        string    `json:"part"`
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
	Body        string    `json:"body"`
}

// SetupSnapshotStore is the Store's per-instance setup snapshots.
type SetupSnapshotStore interface {
	// PutSetupSnapshot upserts by (ActorID, Part). ErrNotFound when the
	// instance does not exist; an unknown part or an over-cap body is refused.
	PutSetupSnapshot(s SetupSnapshot) error
	// SetupSnapshots lists an instance's snapshots, sorted by part.
	SetupSnapshots(actorID string) []SetupSnapshot
}

// prepareSetupSnapshot validates s. Caller holds mu.
func (m *memState) prepareSetupSnapshot(s SetupSnapshot) error {
	if !slices.Contains(setupParts, s.Part) {
		return fmt.Errorf("setup snapshot: unknown part %q", s.Part)
	}
	if _, ok := m.instances[s.ActorID]; !ok {
		return fmt.Errorf("setup snapshot for instance %q: %w", s.ActorID, ErrNotFound)
	}
	if len(s.Body) > MaxSetupBody {
		return fmt.Errorf("setup snapshot: body is %d bytes (max %d)", len(s.Body), MaxSetupBody)
	}
	return nil
}

func (m *memState) applyPutSetupSnapshot(s SetupSnapshot) {
	if m.setup[s.ActorID] == nil {
		m.setup[s.ActorID] = map[string]SetupSnapshot{}
	}
	m.setup[s.ActorID][s.Part] = s
}

func (m *memState) SetupSnapshots(actorID string) []SetupSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SetupSnapshot
	for _, s := range m.setup[actorID] {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b SetupSnapshot) int { return cmp.Compare(a.Part, b.Part) })
	return out
}

func (fs *MemStore) PutSetupSnapshot(s SetupSnapshot) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareSetupSnapshot(s); err != nil {
		return err
	}
	fs.applyPutSetupSnapshot(s)
	return nil
}
```

In `internal/jam/store.go`, add `SetupSnapshotStore` to the embedded interfaces of `Store`:

```go
	PushSubscriptionStore
	SetupSnapshotStore
```

In `internal/jam/memstate.go`:
- Add the field after `pushSubs`:
```go
	// setup is the per-instance setup snapshots: actor id → part → snapshot
	// (setup_snapshot.go). Dropped with the instance.
	setup map[string]map[string]SetupSnapshot
```
- In `newMemState`, add `setup: map[string]map[string]SetupSnapshot{},`.
- In `applyRemoveInstance`, after `delete(m.instances, actorID)`, add `delete(m.setup, actorID)`.

Create `internal/jam/migrations/0018_instance_setup.sql`:

```sql
-- Setup snapshots (docs/usage/jam/inspect.md): the masked form of what Jam
-- last delivered to a studio, one row per (instance, part). Runtime state:
-- never exported; dropped with the instance.
CREATE TABLE instance_setup (
    actor_id    text        NOT NULL REFERENCES instances(actor_id) ON DELETE CASCADE,
    part        text        NOT NULL CHECK (part IN ('connector', 'context', 'egress', 'kit')),
    at          timestamptz NOT NULL,
    fingerprint text        NOT NULL,
    body        text        NOT NULL,
    PRIMARY KEY (actor_id, part)
);
```

In `internal/jam/pgstore.go`, after `RemoveInstance` (whose `DELETE FROM instances` now cascades; its `applyRemoveInstance` drops the cache):

```go
func (s *PostgresStore) PutSetupSnapshot(p SetupSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareSetupSnapshot(p); err != nil {
		return err
	}
	if err := s.exec("PutSetupSnapshot",
		`INSERT INTO instance_setup (actor_id, part, at, fingerprint, body) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (actor_id, part) DO UPDATE SET at = EXCLUDED.at, fingerprint = EXCLUDED.fingerprint, body = EXCLUDED.body`,
		p.ActorID, p.Part, p.At, p.Fingerprint, p.Body); err != nil {
		return err
	}
	s.applyPutSetupSnapshot(p)
	return nil
}

// loadSetupSnapshots fills the setup-snapshot map. load's part, after the
// instances; no lock.
func (s *PostgresStore) loadSetupSnapshots(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT actor_id, part, at, fingerprint, body FROM instance_setup`)
	if err != nil {
		return fmt.Errorf("pgstore: load instance_setup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p SetupSnapshot
		if err := rows.Scan(&p.ActorID, &p.Part, &p.At, &p.Fingerprint, &p.Body); err != nil {
			return err
		}
		p.At = p.At.UTC()
		s.applyPutSetupSnapshot(p)
	}
	return rows.Err()
}
```

and in `load`, right after the `s.loadDocs(ctx, "instances", …)` block:

```go
	if err := s.loadSetupSnapshots(ctx); err != nil {
		return err
	}
```

In `internal/jam/pgstore_testhelpers.go`, `TruncateAllForTest`, add `instance_setup` to the `TRUNCATE` list (before `instances`, which it references), and reset the cache with the other maps:

```go
	s.setup = map[string]map[string]SetupSnapshot{}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestMemStoreConformance && GOPROXY=https://proxy.golang.org,direct go vet -tags integration ./internal/jam/`
Expected: PASS, and the integration build compiles. With a Postgres at hand: `JAM_TEST_POSTGRES_DSN=… go test -tags integration ./internal/jam/ -run TestPostgresStoreConformance` passes.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_snapshot.go internal/jam/store.go internal/jam/memstate.go internal/jam/pgstore.go internal/jam/pgstore_testhelpers.go internal/jam/migrations/0018_instance_setup.sql internal/jam/storetest/setup.go internal/jam/storetest/conformance.go
git commit -m "feat(jam): setup snapshot store (Mem + Postgres, dropped with the instance)"
```

---

### Task 5: Record each delivery, on change

**Files:**
- Create: `internal/jam/setup_record.go`
- Modify: `internal/jam/supervisor.go` (`Raise`, `reconcileEgress`, `Resume`), `internal/jam/context_handler.go`, `internal/jam/connector_handler.go`
- Test: `internal/jam/setup_record_test.go`

**Interfaces:**
- Consumes: Task 2's `contextBody`, `egressBody`, `connectorBody`, `kitRefsBody`, `kitFingerprint`; Task 4's store; `recordedChainKit`, `ResolveKitDefinition`, `harnessinstall.Default`, `studio.Ceiling`, `EgressFingerprint`, `snippet.Fingerprint`.
- Produces:
```go
func RecordSetupPart(store Store, log *slog.Logger, actorID, part, fingerprint, body string, at time.Time)
func capSetupBody(s string) string
func instanceKit(store Store, inst Instance) (studio.StudioKit, bool) // the kit the instance's image was built from
func (s *Supervisor) recordRaiseSetup(inst Instance, spec RaiseSpec, def *KitDefinition)
func (s *Supervisor) recordEgress(inst Instance, p *EgressPolicy)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_record_test.go`:

```go
package jam

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// countingSetupStore counts snapshot writes (and can fail them).
type countingSetupStore struct {
	*MemStore
	puts int
	fail error
}

func (c *countingSetupStore) PutSetupSnapshot(s SetupSnapshot) error {
	c.puts++
	if c.fail != nil {
		return c.fail
	}
	return c.MemStore.PutSetupSnapshot(s)
}

func countingSup(t *testing.T, fl *fakeLauncher) (*Supervisor, *countingSetupStore, *time.Time) {
	t.Helper()
	st := &countingSetupStore{MemStore: NewMemStore()}
	if err := st.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	clk := time.Unix(1000, 0).UTC()
	sup := NewSupervisor(st, fl, "holder-A", time.Minute, 30*time.Second, func() time.Time { return clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sup, st, &clk
}

func parts(st Store, id string) map[string]SetupSnapshot {
	out := map[string]SetupSnapshot{}
	for _, s := range st.SetupSnapshots(id) {
		out[s.Part] = s
	}
	return out
}

func TestRaiseRecordsSetupSnapshots(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	inst, err := raiseDev(t, sup, "w1")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(store, "w1")
	if len(p) != 4 {
		t.Fatalf("parts = %v, want context, egress, connector, kit", p)
	}
	if p[SetupPartContext].Fingerprint != fl.gotSpec.Context.Fingerprint || !strings.Contains(p[SetupPartContext].Body, "/agent-data/context/CORE.md") {
		t.Errorf("context part = %+v", p[SetupPartContext])
	}
	if p[SetupPartEgress].Fingerprint != "kit" || !strings.Contains(p[SetupPartEgress].Body, "npmjs.org") {
		t.Errorf("egress part = %+v (want the kit default with the merged ceiling)", p[SetupPartEgress])
	}
	if p[SetupPartKit].Fingerprint != kitFingerprint(inst.Kit, inst.ImageTag) || !strings.Contains(p[SetupPartKit].Body, "default@v1 → web-dev@v1 → my-app@v1") {
		t.Errorf("kit part = %+v", p[SetupPartKit])
	}
	if !p[SetupPartContext].At.Equal(time.Unix(1000, 0).UTC()) {
		t.Errorf("at = %v, want the supervisor clock", p[SetupPartContext].At)
	}
}

func TestContextRefreshWritesOnlyOnChange(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(st, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/context", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /context = %d", rec.Code)
		}
	}
	get()
	n := st.puts
	get()
	get()
	if st.puts != n {
		t.Fatalf("unchanged refreshes wrote %d snapshot(s)", st.puts-n)
	}
	if err := SetRoleContext(st, "default", "guest", sessionctx.Layer{Core: "A new rule."}); err != nil {
		t.Fatal(err)
	}
	get()
	if st.puts != n+1 || !strings.Contains(parts(st, "w1")[SetupPartContext].Body, "A new rule.") {
		t.Fatalf("an edit must write exactly once (puts %d → %d)", n, st.puts)
	}
}

func TestSetupSnapshotWriteFailureNeverFailsDelivery(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	st.fail = errors.New("disk full")
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatalf("a failed snapshot write must not fail the raise: %v", err)
	}
	for _, h := range []http.Handler{
		NewContextHandler(st, sup, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil))),
		NewConnectorHandler(st, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil))),
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery = %d, want 200 despite the failed write", rec.Code)
		}
	}
}

func TestConnectorHandlerRecordsSnapshot(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, clk := countingSup(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com",
		Env: map[string]string{"GH_TOKEN": "ghp_LITERAL", "ANTHROPIC_API_KEY": "{token}"}})
	h := NewConnectorHandler(st, func() time.Time { return *clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	serve := func(token string) {
		req := httptest.NewRequest(http.MethodGet, "/connector", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve(tok)
	body := parts(st, "w1")[SetupPartConnector].Body
	if !strings.Contains(body, "ANTHROPIC_API_KEY=«identity token»") || strings.Contains(body, "ghp_LITERAL") || strings.Contains(body, tok) {
		t.Fatalf("connector part = %q", body)
	}
	orphan, err := Enroll(st, "hand", "default", "guest", nil, *clk)
	if err != nil {
		t.Fatal(err)
	}
	n := st.puts
	serve(orphan)
	if st.puts != n {
		t.Fatal("an identity without an instance must not write a snapshot")
	}
}

func TestEgressReapplyRecordsSnapshot(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, _ := countingSup(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRoleEgress(st, "default", "guest", []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	fl.egressErr = errors.New("exec failed")
	_ = sup.Reconcile(context.Background())
	if got := parts(st, "w1")[SetupPartEgress].Fingerprint; got != "kit" {
		t.Fatalf("a failed apply recorded %q", got)
	}
	fl.egressErr = nil
	_ = sup.Reconcile(context.Background())
	if got := parts(st, "w1")[SetupPartEgress]; got.Fingerprint != "d:example.com" || !strings.Contains(got.Body, "example.com") {
		t.Fatalf("egress part after a landed apply = %+v", got)
	}
}

func TestCapSetupBody(t *testing.T) {
	s := strings.Repeat("é", MaxSetupBody) // 2 bytes a rune: over the cap
	got := capSetupBody(s)
	if len(got) > MaxSetupBody || !utf8.ValidString(got) || !strings.Contains(got, "truncated") {
		t.Fatalf("capped to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if capSetupBody("short") != "short" {
		t.Fatal("a short body must be kept")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestRaiseRecordsSetupSnapshots|TestContextRefreshWritesOnlyOnChange|TestSetupSnapshotWriteFailure|TestConnectorHandlerRecordsSnapshot|TestEgressReapplyRecordsSnapshot|TestCapSetupBody'`
Expected: FAIL to compile (`undefined: capSetupBody`). Once that is stubbed, the tests fail with `parts = map[]`.

- [ ] **Step 3: Implement**

Create `internal/jam/setup_record.go`:

```go
package jam

import (
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studio"
)

// RecordSetupPart records that part (already-masked body) was delivered to
// actorID — only when its fingerprint differs from the stored one, so a cove
// refreshing every wake does not write each time. A failure is logged and
// never returned: a snapshot must not fail a delivery.
func RecordSetupPart(store Store, log *slog.Logger, actorID, part, fingerprint, body string, at time.Time) {
	for _, cur := range store.SetupSnapshots(actorID) {
		if cur.Part == part && cur.Fingerprint == fingerprint {
			return
		}
	}
	err := store.PutSetupSnapshot(SetupSnapshot{ActorID: actorID, Part: part, At: at.UTC(), Fingerprint: fingerprint, Body: capSetupBody(body)})
	if err != nil && log != nil {
		log.Warn("setup snapshot write failed", "actor", actorID, "part", part, "err", err.Error())
	}
}

// capSetupBody truncates s to MaxSetupBody bytes (at a rune boundary) with a
// marker naming the full size.
func capSetupBody(s string) string {
	if len(s) <= MaxSetupBody {
		return s
	}
	marker := fmt.Sprintf("\n… truncated: %d bytes in all (cap %d)\n", len(s), MaxSetupBody)
	cut := s[:MaxSetupBody-len(marker)]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}

// instanceKit is the merged kit an instance's image was built from: its
// recorded chain at those exact versions, else its kit ref. ok=false without
// a kit or when the registry no longer has it.
func instanceKit(store Store, inst Instance) (studio.StudioKit, bool) {
	if len(inst.KitChain) > 0 {
		sk, err := recordedChainKit(store, inst.KitChain)
		return sk, err == nil
	}
	if inst.Kit.ID == "" {
		return studio.StudioKit{}, false
	}
	def, ok, err := ResolveKitDefinition(store, inst.Kit, harnessinstall.Default())
	return def.Kit, ok && err == nil
}

// recordRaiseSetup records the four parts a raise delivered.
func (s *Supervisor) recordRaiseSetup(inst Instance, spec RaiseSpec, def *KitDefinition) {
	now := s.now()
	if spec.Context != nil {
		RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartContext, spec.Context.Fingerprint, contextBody(*spec.Context), now)
	}
	var ceiling []string
	known := def != nil && def.Ref == spec.Kit
	if known {
		ceiling, _ = studio.Ceiling(def.Kit.Egress)
	}
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartEgress, EgressFingerprint(spec.Egress), egressBody(spec.Egress, ceiling, known), now)
	if spec.Connector != nil {
		RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartConnector, snippet.Fingerprint(*spec.Connector), connectorBody(*spec.Connector), now)
	}
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartKit, kitFingerprint(inst.Kit, inst.ImageTag), kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag), now)
}

// recordEgress records a landed egress apply of p to inst.
func (s *Supervisor) recordEgress(inst Instance, p *EgressPolicy) {
	sk, known := instanceKit(s.store, inst)
	var ceiling []string
	if known {
		ceiling, _ = studio.Ceiling(sk.Egress)
	}
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartEgress, EgressFingerprint(p), egressBody(p, ceiling, known), s.now())
}
```

In `internal/jam/supervisor.go`:
- `Raise`: right after the `if err := s.store.PutInstance(inst); err != nil { … }` block, add:
```go
	s.recordRaiseSetup(inst, spec, def)
```
- `reconcileEgress`: in the `if err == nil {` branch, after `s.logEgressApplied(inst, want)`, add `s.recordEgress(inst, want)`.
- `Resume`: after `s.logEgressApplied(inst, want)`, add `s.recordEgress(inst, want)`.

In `internal/jam/context_handler.go`, after the `switch` on `err` (before `writeJSON`):

```go
		RecordSetupPart(store, log, a.ID, SetupPartContext, b.Fingerprint, contextBody(b), now())
```

In `internal/jam/connector_handler.go`, before `writeJSON(w, http.StatusOK, c)` (import `github.com/aethons-tools/cove/internal/jam/snippet`):

```go
		if _, ok := store.GetInstance(a.ID); ok { // a hand-enrolled client has no studio to record
			RecordSetupPart(store, log, a.ID, SetupPartConnector, snippet.Fingerprint(c), connectorBody(c), now())
		}
```

Update both handlers' doc comments: "Records the delivered part as a setup snapshot (on change)."

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/`
Expected: PASS, the whole package included. Existing supervisor tests are unaffected because snapshot writes never fail a raise.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_record.go internal/jam/setup_record_test.go internal/jam/supervisor.go internal/jam/context_handler.go internal/jam/connector_handler.go
git commit -m "feat(jam): record setup snapshots at raise, /context, /connector and egress apply"
```

---

### Task 6: The image reader

**Files:**
- Create: `internal/jam/setup_image.go`, `internal/jam/imagefacts/imagefacts.go`
- Modify: `internal/backend/colima/colima.go` (after `dargs`)
- Test: `internal/jam/imagefacts/imagefacts_test.go`, `internal/backend/colima/colima_test.go`

**Interfaces:**
- Consumes: `runner.Runner` (`RunIO`), `runner.Fake` (`Calls`, `Outputs`), `colima.NewWithContext`.
- Produces:
```go
// package jam
type ImageFacts struct { ID string; Env []string; Labels map[string]string; Seed []SeedFile; SeedNote string }
type SeedFile struct { Path string; Size int64; Text string; HasText bool }
type ImageFactsReader interface { ImageFacts(ctx context.Context, image string) (ImageFacts, error) }
var ErrImageNotPresent, ErrImageUnreadable error

// package colima
func (c *Colima) DockerArgs(args ...string) []string

// package imagefacts
const SeedDir = "/home/agent/.init-agent-data"
const (MaxSeedBytes = 1 << 20; MaxSeedEntries = 200; MaxSeedText = 64 << 10)
type DockerArgs interface{ DockerArgs(args ...string) []string }
func New(r runner.Runner, d DockerArgs) *Reader
func (x *Reader) ImageFacts(ctx context.Context, image string) (jam.ImageFacts, error)
func parseSeedTar(r io.Reader) ([]jam.SeedFile, string)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/colima/colima_test.go`:

```go
func TestDockerArgsPinsTheContext(t *testing.T) {
	if got := NewWithContext(&runner.Fake{}, "colima-jam").DockerArgs("image", "inspect", "x"); strings.Join(got, " ") != "--context colima-jam image inspect x" {
		t.Fatalf("DockerArgs = %v", got)
	}
	if got := NewWithContext(&runner.Fake{}, "").DockerArgs("ps"); strings.Join(got, " ") != "--context colima ps" {
		t.Fatalf("default context = %v", got)
	}
}
```

Create `internal/jam/imagefacts/imagefacts_test.go`:

```go
package imagefacts

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

const inspectJSON = `{"Id":"sha256:aaaaaaaaaaaaaaaa","Config":{"Env":["PATH=/usr/bin","DB_PASSWORD=hunter2"],"Labels":{"cove.kit":"my-app@v1"}}}`

type entry struct {
	name string
	body []byte
	dir  bool
}

func seedTar(t *testing.T, entries ...entry) string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	return b.String()
}

func newReader(f *runner.Fake) *Reader { return New(f, colima.NewWithContext(f, "colima-jam")) }

func TestImageFactsArgvNeverStarts(t *testing.T) {
	tarball := seedTar(t, entry{name: ".init-agent-data/", dir: true}, entry{name: ".init-agent-data/CLAUDE.md", body: []byte("@SANDBOX.md\n")})
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef\n"}, {Stdout: tarball}, {}}}
	got, err := newReader(f).ImageFacts(context.Background(), "cove-kit:abc-asm")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"--context", "colima-jam", "image", "inspect", "--format", "{{json .}}", "cove-kit:abc-asm"},
		{"--context", "colima-jam", "create", "sha256:aaaaaaaaaaaaaaaa"},
		{"--context", "colima-jam", "cp", "0123456789abcdef:" + SeedDir, "-"},
		{"--context", "colima-jam", "rm", "-f", "0123456789abcdef"},
	}
	if len(f.Calls) != len(want) {
		t.Fatalf("calls = %+v", f.Calls)
	}
	for i, c := range f.Calls {
		if c.Name != "docker" || !slices.Equal(c.Args, want[i]) {
			t.Errorf("call %d = %s %v, want docker %v", i, c.Name, c.Args, want[i])
		}
		for _, verb := range []string{"start", "run", "exec"} {
			if slices.Contains(c.Args, verb) {
				t.Fatalf("call %d runs %q: %v", i, verb, c.Args)
			}
		}
	}
	if got.ID != "sha256:aaaaaaaaaaaaaaaa" || len(got.Env) != 2 || got.Labels["cove.kit"] != "my-app@v1" {
		t.Fatalf("facts = %+v", got)
	}
	if len(got.Seed) != 1 || got.Seed[0].Path != "CLAUDE.md" || !got.Seed[0].HasText || got.SeedNote != "" {
		t.Fatalf("seed = %+v note %q", got.Seed, got.SeedNote)
	}
}

func TestImageFactsCachedByID(t *testing.T) {
	tarball := seedTar(t, entry{name: ".init-agent-data/CLAUDE.md", body: []byte("x")})
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Stdout: "0123456789abcdef"}, {Stdout: tarball}, {}, {Stdout: inspectJSON}}}
	r := newReader(f)
	if _, err := r.ImageFacts(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ImageFacts(context.Background(), "t2-same-id"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 5 {
		t.Fatalf("calls = %d, want 4 + one inspect (the seed is cached per image id)", len(f.Calls))
	}
}

func TestImageFactsNotPresent(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: errors.New("exit status 1")}}}
	if _, err := newReader(f).ImageFacts(context.Background(), "cove-kit:gone"); !errors.Is(err, jam.ErrImageNotPresent) {
		t.Fatalf("err = %v, want ErrImageNotPresent", err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("a missing image must stop after inspect: %+v", f.Calls)
	}
	if _, err := newReader(&runner.Fake{}).ImageFacts(context.Background(), "--format"); !errors.Is(err, jam.ErrImageNotPresent) {
		t.Fatal("an image name that reads as a flag must be refused before docker runs")
	}
}

func TestImageFactsSeedDockerErrorNotCached(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: inspectJSON}, {Err: errors.New("daemon gone")}, {Stdout: inspectJSON}, {Err: errors.New("daemon gone")}}}
	r := newReader(f)
	got, err := r.ImageFacts(context.Background(), "t")
	if err != nil || got.Seed != nil || got.SeedNote != seedUnreadable || len(got.Env) != 2 {
		t.Fatalf("facts = %+v, %v (want env and labels with a seed note)", got, err)
	}
	if _, err := r.ImageFacts(context.Background(), "t"); err != nil {
		t.Fatal(err)
	}
	if f.Calls[3].Args[2] != "create" {
		t.Fatalf("a docker error must not be cached: calls = %+v", f.Calls)
	}
}

func TestParseSeedTarCaps(t *testing.T) {
	var many []entry
	for i := range MaxSeedEntries + 1 {
		many = append(many, entry{name: ".init-agent-data/f" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + ".md", body: []byte("x")})
	}
	if _, note := parseSeedTar(strings.NewReader(seedTar(t, many...))); !strings.Contains(note, "200 entries") {
		t.Errorf("entries note = %q", note)
	}
	big := bytes.Repeat([]byte("a"), MaxSeedBytes/2+1)
	files, note := parseSeedTar(strings.NewReader(seedTar(t, entry{name: ".init-agent-data/a", body: big}, entry{name: ".init-agent-data/b", body: big})))
	if len(files) != 1 || !strings.Contains(note, "1048576 bytes") || files[0].HasText {
		t.Errorf("size cap: files %+v note %q (a 512 KiB file is listed, not shown)", files, note)
	}
	files, _ = parseSeedTar(strings.NewReader(seedTar(t,
		entry{name: ".init-agent-data/bin", body: []byte{0xff, 0xfe}},
		entry{name: ".init-agent-data/../../etc/passwd", body: []byte("root")},
		entry{name: ".init-agent-data/skills/s/SKILL.md", body: []byte("---\nname: s\n---\n")})))
	if len(files) != 3 || files[0].Path != "bin" || files[0].HasText || files[1].Path != "passwd" || files[2].Path != "skills/s/SKILL.md" || !files[2].HasText {
		t.Errorf("files = %+v (binary unshown, .. cleaned under the seed, sorted)", files)
	}
	if _, note := parseSeedTar(strings.NewReader(seedTar(t, entry{name: ".init-agent-data/a", body: []byte("x")})[:700])); !strings.Contains(note, "incomplete") {
		t.Errorf("a cut archive = %q", note)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/backend/colima/ -run TestDockerArgsPinsTheContext; GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/imagefacts/`
Expected: FAIL to compile (`DockerArgs undefined`, `no Go files` / `undefined: New`).

- [ ] **Step 3: Implement**

In `internal/backend/colima/colima.go`, after `dargs`:

```go
// DockerArgs is dargs for other packages: a docker subcommand's argv pinned
// to this backend's context, so a reader (Jam's setup inspection) queries
// exactly the daemon the launcher builds and runs on.
func (c *Colima) DockerArgs(args ...string) []string { return c.dargs(args...) }
```

Create `internal/jam/setup_image.go`:

```go
package jam

import (
	"context"
	"errors"
)

// ImageFacts is what setup inspection reads from a built image on the
// launcher host, without starting it: its id, ENV, labels and the files it
// seeds into /agent-data (capped; SeedNote says why a listing is partial).
type ImageFacts struct {
	ID       string
	Env      []string // "KEY=VALUE" as baked; redacted at render
	Labels   map[string]string
	Seed     []SeedFile // sorted by Path, relative to the seed root
	SeedNote string     // "" = complete
}

// SeedFile is one regular file of the image's /agent-data seed. Text is set
// (HasText) for valid UTF-8 files within the text cap.
type SeedFile struct {
	Path    string
	Size    int64
	Text    string
	HasText bool
}

// ImageFactsReader reads an image's facts on the launcher host
// (internal/jam/imagefacts). nil (the placeholder launcher) leaves every
// image part unavailable.
type ImageFactsReader interface {
	ImageFacts(ctx context.Context, image string) (ImageFacts, error)
}

// Image-read failures, shown as their fixed text.
var (
	ErrImageNotPresent = errors.New("image not present on the launcher host")
	ErrImageUnreadable = errors.New("docker error on the launcher host")
)
```

Create `internal/jam/imagefacts/imagefacts.go`:

```go
// Package imagefacts reads a built studio image's facts on the launcher host
// for Jam's setup inspection: `docker image inspect` (env, labels, id) and the
// /agent-data seed via `docker create` + `docker cp … -` + `docker rm`. The
// container is never started. Every command is argv through runner.Runner,
// pinned to the launcher's docker context.
package imagefacts

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

// SeedDir is where an image keeps the seed copied into /agent-data at boot.
const SeedDir = "/home/agent/.init-agent-data"

// Caps on a seed listing.
const (
	MaxSeedBytes   = 1 << 20  // total regular-file bytes
	MaxSeedEntries = 200      // archive entries
	MaxSeedText    = 64 << 10 // a file shown as text
	cacheSize      = 16       // images whose facts are kept
	// maxTarBytes bounds the docker cp stream: the content cap plus a header
	// and padding block per entry.
	maxTarBytes = MaxSeedBytes + (MaxSeedEntries+2)*1024
)

const seedUnreadable = "seed unreadable: docker error on the launcher host"

// DockerArgs pins a docker subcommand to the launcher's context
// ((*colima.Colima).DockerArgs).
type DockerArgs interface {
	DockerArgs(args ...string) []string
}

// Reader reads image facts, caching them per image id (LRU).
type Reader struct {
	r     runner.Runner
	d     DockerArgs
	mu    sync.Mutex
	cache map[string]jam.ImageFacts
	order []string // least recently used first
}

var _ jam.ImageFactsReader = (*Reader)(nil)

// New returns a Reader running docker through r, pinned by d.
func New(r runner.Runner, d DockerArgs) *Reader {
	return &Reader{r: r, d: d, cache: map[string]jam.ImageFacts{}}
}

var containerIDRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

type inspectOut struct {
	ID     string `json:"Id"`
	Config struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// ImageFacts inspects image (one `docker image inspect` per call: it names the
// id the cache is keyed on) and reads its seed once per id. A failed inspect
// is ErrImageNotPresent; a failed seed read keeps env and labels, notes the
// failure, and is not cached. ctx is unused: runner.Runner has none.
func (x *Reader) ImageFacts(ctx context.Context, image string) (jam.ImageFacts, error) {
	if image == "" || strings.HasPrefix(image, "-") {
		return jam.ImageFacts{}, jam.ErrImageNotPresent
	}
	out, err := x.output("image", "inspect", "--format", "{{json .}}", image)
	if err != nil {
		return jam.ImageFacts{}, jam.ErrImageNotPresent
	}
	var in inspectOut
	if err := json.Unmarshal(out, &in); err != nil || in.ID == "" {
		return jam.ImageFacts{}, jam.ErrImageUnreadable
	}
	if f, ok := x.get(in.ID); ok {
		return f, nil
	}
	f := jam.ImageFacts{ID: in.ID, Env: in.Config.Env, Labels: in.Config.Labels}
	var cacheable bool
	f.Seed, f.SeedNote, cacheable = x.seed(in.ID)
	if cacheable {
		x.put(f)
	}
	return f, nil
}

// output runs one docker command, capturing stdout (stdin empty, stderr dropped).
func (x *Reader) output(args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := x.r.RunIO(strings.NewReader(""), &out, io.Discard, "docker", x.d.DockerArgs(args...)...)
	return out.Bytes(), err
}

// seed creates a stopped container from id, streams its seed dir out as a
// tar, and removes the container. cacheable=false on a docker failure.
func (x *Reader) seed(id string) ([]jam.SeedFile, string, bool) {
	out, err := x.output("create", id)
	ctr := strings.TrimSpace(string(out))
	if err != nil || !containerIDRE.MatchString(ctr) {
		return nil, seedUnreadable, false
	}
	defer func() { _, _ = x.output("rm", "-f", ctr) }()
	w := &capWriter{max: maxTarBytes}
	err = x.r.RunIO(strings.NewReader(""), w, io.Discard, "docker", x.d.DockerArgs("cp", ctr+":"+SeedDir, "-")...)
	if err != nil && !w.full && w.buf.Len() == 0 {
		return nil, seedUnreadable, false
	}
	files, note := parseSeedTar(bytes.NewReader(w.buf.Bytes()))
	if w.full && note == "" {
		note = fmt.Sprintf("seed listing truncated at %d bytes", MaxSeedBytes)
	}
	return files, note, err == nil || w.full
}

var errTarFull = errors.New("seed archive over its cap")

// capWriter keeps at most max bytes, then fails the write (docker cp stops).
type capWriter struct {
	buf  bytes.Buffer
	max  int
	full bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if len(p) <= room {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:max(room, 0)])
	w.full = true
	return max(room, 0), errTarFull
}

// parseSeedTar lists the regular files of a `docker cp <dir> -` archive,
// paths relative to the copied dir, within the caps; note says why a listing
// stopped early.
func parseSeedTar(r io.Reader) ([]jam.SeedFile, string) {
	tr := tar.NewReader(r)
	var files []jam.SeedFile
	var total int64
	note := ""
	for entries := 1; ; entries++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			note = "seed listing truncated (incomplete archive)"
			break
		}
		if entries > MaxSeedEntries {
			note = fmt.Sprintf("seed listing truncated at %d entries", MaxSeedEntries)
			break
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p, ok := seedPath(h.Name)
		if !ok {
			continue
		}
		if total+h.Size > MaxSeedBytes {
			note = fmt.Sprintf("seed listing truncated at %d bytes", MaxSeedBytes)
			break
		}
		total += h.Size
		f := jam.SeedFile{Path: p, Size: h.Size}
		if h.Size <= MaxSeedText {
			b, err := io.ReadAll(io.LimitReader(tr, MaxSeedText+1))
			if err == nil && int64(len(b)) == h.Size && utf8.Valid(b) {
				f.Text, f.HasText = string(b), true
			}
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b jam.SeedFile) int { return strings.Compare(a.Path, b.Path) })
	return files, note
}

// seedPath drops the archive's top directory (the copied dir's own name);
// ".." cannot climb out of it.
func seedPath(name string) (string, bool) {
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	_, rest, ok := strings.Cut(clean, "/")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

func (x *Reader) get(id string) (jam.ImageFacts, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f, ok := x.cache[id]
	if ok {
		x.order = append(slices.DeleteFunc(x.order, func(s string) bool { return s == id }), id)
	}
	return f, ok
}

func (x *Reader) put(f jam.ImageFacts) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.cache[f.ID]; !ok {
		x.order = append(x.order, f.ID)
		if len(x.order) > cacheSize {
			delete(x.cache, x.order[0])
			x.order = x.order[1:]
		}
	}
	x.cache[f.ID] = f
}
```

Note on the `..` case in `TestParseSeedTarCaps`: `.init-agent-data/../../etc/passwd` cleans to `etc/passwd`, whose top directory `etc` is dropped, leaving `passwd`. The archive comes from the image's own seed dir, so this only proves that no path can climb out.

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/backend/colima/ ./internal/jam/imagefacts/ ./internal/jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/colima/colima.go internal/backend/colima/colima_test.go internal/jam/setup_image.go internal/jam/imagefacts/
git commit -m "feat(jam): image facts reader (inspect + create/cp/rm, never started; capped, cached)"
```

---

### Task 7: Image and kit-detail bodies

**Files:**
- Create: `internal/jam/setup_image_bodies.go`
- Test: `internal/jam/setup_image_bodies_test.go`

**Interfaces:**
- Consumes: Task 6's `ImageFacts`, `SeedFile`; Task 1's `RedactEnv`, `RedactJSON`, `RedactLabel`, `SafeText`; Task 2's `writeTable`, `flat`, `ensureNL`, `joinOr`, `shortDigest`; Task 3's `stripUserinfo`; `Kit.Meta`, `KitVersionMeta`, `KitSource`.
- Produces:
```go
func seedBody(f ImageFacts) string
func imageEnvBody(f ImageFacts) string
func imageLabelsBody(f ImageFacts) string
func kitDetailBody(store Store, chain []KitRef, buildArgs map[string]string) string
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_image_bodies_test.go`:

```go
package jam

import (
	"strings"
	"testing"
	"time"
)

func seedFacts() ImageFacts {
	text := func(p, s string) SeedFile { return SeedFile{Path: p, Size: int64(len(s)), Text: s, HasText: true} }
	return ImageFacts{ID: "sha256:0123456789abcdef", SeedNote: "seed listing truncated at 200 entries",
		Env: []string{"PATH=/usr/bin", "DB_PASSWORD=hunter2", "COVE_SSHENV=GOPATH:GOPROXY"},
		Labels: map[string]string{"org.opencontainers.image.source": "https://u:p@github.com/x/y", "evil": "a\nb"},
		Seed: []SeedFile{
			{Path: ".claude.json", Size: 210, Text: `{"oauthAccount":"secret"}`, HasText: true},
			text(".refresh", "# comment\nCLAUDE.md\nskills\n"),
			text("CLAUDE.md", "Read these:\n@SANDBOX.md\n@../escape.md\n"),
			text("SANDBOX.md", "sandbox rules\n"),
			text("reference/x.md", "ref"),
			text("settings.json", `{"env":{"GH_TOKEN":"ghp_SETTINGS"},"model":"opus"}`),
			text("skills/docs-audit/SKILL.md", "---\nname: docs-audit\ndescription: Check the docs\n---\nbody"),
		}}
}

func TestSeedBody(t *testing.T) {
	got := seedBody(seedFacts())
	for _, want := range []string{
		"image: sha256:01234\n", "\n--- /agent-data/CLAUDE.md\nRead these:\n", "\n--- /agent-data/SANDBOX.md (imported)\nsandbox rules\n",
		"docs-audit  Check the docs\n", "\n--- /agent-data/settings.json (secret-named values masked)\n", `"model": "opus"`,
		"re-copied every boot (.refresh): CLAUDE.md, skills\n", "/agent-data/.claude.json", "210 B", "/agent-data/reference/x.md",
		"(seed listing truncated at 200 entries)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"ghp_SETTINGS", "oauthAccount", "escape.md (imported)"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q shown:\n%s", bad, got)
		}
	}
}

func TestImageEnvAndLabelBodies(t *testing.T) {
	got := imageEnvBody(seedFacts())
	if !strings.Contains(got, "  PATH=/usr/bin\n") || !strings.Contains(got, "  DB_PASSWORD=«masked»\n") || !strings.Contains(got, "transferred into sessions (COVE_SSHENV): GOPATH, GOPROXY\n") {
		t.Errorf("env:\n%s", got)
	}
	got = imageLabelsBody(seedFacts())
	if !strings.Contains(got, "  org.opencontainers.image.source=https://github.com/x/y\n") || !strings.Contains(got, "  evil=invalid\n") || strings.Contains(got, "u:p") {
		t.Errorf("labels:\n%s", got)
	}
}

func TestKitDetailBody(t *testing.T) {
	st := NewMemStore()
	ref := mustEnsure(t, st, "base", chainKit("", "b"))
	k, _ := st.GetKit("base")
	k.Meta = map[int]KitVersionMeta{ref.Version: {PushedAt: time.Unix(0, 0).UTC(), Source: &KitSource{Repo: "https://x:y@github.com/o/kits", Dir: "base", Ref: "main", Commit: "abcdef0123456789"}}}
	if err := st.PutKitDocs([]Kit{k}); err != nil {
		t.Fatal(err)
	}
	got := kitDetailBody(st, []KitRef{ref, {ID: "ghost", Version: 3}}, map[string]string{"GO_VERSION": "1.27", "NPM_TOKEN": "npm_live"})
	for _, want := range []string{"base@v1", "https://github.com/o/kits//base@main (commit abcdef012345), pushed 1970-01-01T00:00:00Z",
		"ghost@v3", "unknown (not in the registry)", "  GO_VERSION=1.27\n", "  NPM_TOKEN=«masked»\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x:y") || strings.Contains(got, "npm_live") {
		t.Errorf("leak:\n%s", got)
	}
}
```

(`chainKit` and `mustEnsure` are the existing helpers in `kitchain_test.go`.)

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestSeedBody|TestImageEnvAndLabelBodies|TestKitDetailBody'`
Expected: FAIL to compile (`undefined: seedBody`).

- [ ] **Step 3: Implement**

Create `internal/jam/setup_image_bodies.go`:

```go
package jam

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
)

// seedBody renders what an image seeds into /agent-data: CLAUDE.md and the
// files it imports in full, skills by name and description, settings.json
// redacted, the .refresh list, every other file by path and size.
func seedBody(f ImageFacts) string {
	byPath := map[string]SeedFile{}
	for _, s := range f.Seed {
		byPath[s.Path] = s
	}
	shown := map[string]bool{}
	var sb strings.Builder
	fmt.Fprintf(&sb, "image: %s\n", shortDigest(f.ID))
	if c, ok := byPath["CLAUDE.md"]; ok && c.HasText {
		shown["CLAUDE.md"] = true
		fmt.Fprintf(&sb, "\n--- /agent-data/CLAUDE.md\n%s", ensureNL(c.Text))
		for _, imp := range claudeImports(c.Text) {
			if s, ok := byPath[imp]; ok && s.HasText && !shown[imp] {
				shown[imp] = true
				fmt.Fprintf(&sb, "\n--- /agent-data/%s (imported)\n%s", imp, ensureNL(s.Text))
			}
		}
	} else {
		sb.WriteString("\nCLAUDE.md: none\n")
	}
	var skills [][]string
	for _, s := range f.Seed {
		if dir, ok := skillDir(s.Path); ok && s.HasText {
			shown[s.Path] = true
			name, desc := skillMeta(s.Text)
			skills = append(skills, []string{"  " + SafeText(cmp.Or(name, dir)), flat(desc)})
		}
	}
	if len(skills) == 0 {
		sb.WriteString("\nskills: none\n")
	} else {
		sb.WriteString("\nskills:\n")
		writeTable(&sb, []string{"  NAME", "DESCRIPTION"}, skills)
	}
	if s, ok := byPath["settings.json"]; ok {
		shown["settings.json"] = true
		if js, ok := RedactJSON([]byte(s.Text)); s.HasText && ok {
			fmt.Fprintf(&sb, "\n--- /agent-data/settings.json (secret-named values masked)\n%s\n", js)
		} else {
			fmt.Fprintf(&sb, "\nsettings.json: not valid JSON (%d B)\n", s.Size)
		}
	}
	if s, ok := byPath[".refresh"]; ok && s.HasText {
		shown[".refresh"] = true
		fmt.Fprintf(&sb, "\nre-copied every boot (.refresh): %s\n", joinOr(refreshEntries(s.Text), "none"))
	}
	var rest [][]string
	for _, s := range f.Seed {
		if !shown[s.Path] {
			rest = append(rest, []string{"  /agent-data/" + SafeText(s.Path), fmt.Sprintf("%d B", s.Size)})
		}
	}
	if len(rest) > 0 {
		sb.WriteString("\nother files:\n")
		writeTable(&sb, []string{"  PATH", "SIZE"}, rest)
	}
	if f.SeedNote != "" {
		fmt.Fprintf(&sb, "\n(%s)\n", f.SeedNote)
	}
	return sb.String()
}

// claudeImports are CLAUDE.md's "@path" lines that stay inside the seed.
func claudeImports(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		p, ok := strings.CutPrefix(strings.TrimSpace(line), "@")
		if !ok || p == "" || strings.HasPrefix(p, "/") {
			continue
		}
		if c := path.Clean(p); !strings.HasPrefix(c, "..") {
			out = append(out, c)
		}
	}
	return out
}

func skillDir(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" {
		return parts[1], true
	}
	return "", false
}

// skillMeta reads name and description from a SKILL.md frontmatter.
func skillMeta(text string) (name, desc string) {
	body, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", ""
	}
	front, _, _ := strings.Cut(body, "\n---")
	for line := range strings.Lines(front) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc
}

func refreshEntries(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, SafeText(l))
		}
	}
	return out
}

// imageEnvBody renders an image's baked ENV (redacted) and the names its
// COVE_SSHENV transfers into sessions.
func imageEnvBody(f ImageFacts) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "image env (%s):\n", shortDigest(f.ID))
	var transfer string
	for _, kv := range f.Env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "COVE_SSHENV" {
			transfer = v
		}
		fmt.Fprintf(&sb, "  %s=%s\n", SafeText(k), flat(RedactEnv(k, v)))
	}
	names := strings.FieldsFunc(transfer, func(r rune) bool { return r == ':' })
	for i, n := range names {
		names[i] = SafeText(n)
	}
	fmt.Fprintf(&sb, "transferred into sessions (COVE_SSHENV): %s\n", joinOr(names, "none"))
	return sb.String()
}

// imageLabelsBody renders an image's id and labels, each re-parsed.
func imageLabelsBody(f ImageFacts) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "image id: %s\n", SafeText(f.ID))
	if len(f.Labels) == 0 {
		sb.WriteString("labels: none\n")
		return sb.String()
	}
	sb.WriteString("labels:\n")
	for _, k := range slices.Sorted(maps.Keys(f.Labels)) {
		dk, dv := RedactLabel(k, f.Labels[k])
		fmt.Fprintf(&sb, "  %s=%s\n", dk, dv)
	}
	return sb.String()
}

// kitDetailBody renders each chain link's provenance (from the registry) and
// the merged build args (redacted).
func kitDetailBody(store Store, chain []KitRef, buildArgs map[string]string) string {
	var sb strings.Builder
	sb.WriteString("provenance (root first):\n")
	rows := make([][]string, 0, len(chain))
	for _, r := range chain {
		src := "unknown (not in the registry)"
		if k, ok := store.GetKit(r.ID); ok {
			if m, ok := k.Meta[r.Version]; ok {
				src = "local file"
				if m.Source != nil {
					src = sourceText(*m.Source)
				}
				src += ", pushed " + m.PushedAt.UTC().Format(time.RFC3339)
			} else if _, ok := k.Versions[r.Version]; ok {
				src = "legacy version (no provenance)"
			}
		}
		rows = append(rows, []string{"  " + r.String(), src})
	}
	writeTable(&sb, []string{"  KIT", "SOURCE"}, rows)
	if len(buildArgs) == 0 {
		sb.WriteString("build args: none\n")
		return sb.String()
	}
	sb.WriteString("build args:\n")
	for _, k := range slices.Sorted(maps.Keys(buildArgs)) {
		fmt.Fprintf(&sb, "  %s=%s\n", SafeText(k), flat(RedactEnv(k, buildArgs[k])))
	}
	return sb.String()
}

func sourceText(s KitSource) string {
	out := stripUserinfo(s.Repo)
	if s.Dir != "" {
		out += "//" + SafeText(s.Dir)
	}
	out += "@" + SafeText(s.Ref)
	if s.Commit != "" {
		out += " (commit " + shortDigest(SafeText(s.Commit)) + ")"
	}
	return out
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestSeedBody|TestImageEnvAndLabelBodies|TestKitDetailBody'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_image_bodies.go internal/jam/setup_image_bodies_test.go
git commit -m "feat(jam): seed, image env/label and kit provenance bodies"
```

---

### Task 8: Assemble a role's and a studio's setup, with the diff

**Files:**
- Create: `internal/jam/setup_render.go`, `internal/jam/testdata/setup/role.golden`, `internal/jam/testdata/setup/studio.golden`
- Test: `internal/jam/setup_render_test.go`

**Interfaces:**
- Consumes: everything above; `Supervisor.raiseKit`, `compileContext`, `imageTag`, `ContextForActor`, `CurrentImage` (via `imageStatus`); `ConnectorFor`, `ModelSpecFor`, `ResolveSession`, `ProjectIDOf`, `linediff.Diff`, `studio.Ceiling`.
- Produces:
```go
type SetupDeps struct {
	Store        Store
	Sup          *Supervisor      // nil: context and kit resolution unavailable
	Images       ImageFactsReader // nil: image parts unavailable
	SealedEgress []string
}
func RoleSetup(ctx context.Context, d SetupDeps, project, role string, sections []string) (StudioSetup, error)          // ErrNotFound
func InstanceSetup(ctx context.Context, d SetupDeps, ref string, sections []string, diffRole bool) (StudioSetup, error) // ErrNotFound
func (s *Supervisor) wouldBeRaise(project string, role Role) (wouldBe, error)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_render_test.go`:

```go
package jam

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var updateSetupGolden = flag.Bool("update-setup", false, "rewrite internal/jam/testdata/setup/*.golden")

type fakeImages struct {
	facts map[string]ImageFacts
	err   error
	calls []string
}

func (f *fakeImages) ImageFacts(_ context.Context, tag string) (ImageFacts, error) {
	f.calls = append(f.calls, tag)
	if f.err != nil {
		return ImageFacts{}, f.err
	}
	x, ok := f.facts[tag]
	if !ok {
		return ImageFacts{}, ErrImageNotPresent
	}
	return x, nil
}

// setupFixture raises studio w1 of role dev (the my-app → web-dev → default
// chain) with two destinations — one holding a literal secret in its env —
// and an image whose ENV, labels and settings.json hold secrets too.
func setupFixture(t *testing.T) (SetupDeps, Instance, string, *fakeImages) {
	t.Helper()
	sup, store, fl := chainRaiseKit(t)
	fl.asm = "asm1"
	_ = store.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyXAPIKey, Apply: ApplyXAPIKey, CredName: "anthropic-key"})
	// (Upstream userinfo is TestDestinationsBody's: the Studio context layer
	// delivers upstreams verbatim, which predates this slice.)
	_ = store.AddDestination(Destination{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer,
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "token {cred}"}, CredName: "gh-pat",
		Env: map[string]string{"GH_TOKEN": "ghp_SECRETLITERAL", "GH_HOST": "{host}"}, Git: true})
	if err := store.PutRole("default", Role{Name: "dev", Kit: "my-app", Scope: Scope{Destinations: []string{"anthropic", "gh"}, Credentials: map[string]string{"gh": "gh-bot"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	inst, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	facts := seedFacts() // Task 7's fixture: DB_PASSWORD, u:p@ label, ghp_SETTINGS
	facts.Env = append(facts.Env, "API_KEY=hunter2-SECRET")
	img := &fakeImages{facts: map[string]ImageFacts{inst.ImageTag: facts}}
	return SetupDeps{Store: store, Sup: sup, Images: img, SealedEgress: []string{".anthropic.com", ".github.com"}}, inst, tok, img
}

var hexRun = regexp.MustCompile(`[0-9a-f]{12,}`)

func checkGolden(t *testing.T, name string, s StudioSetup) {
	t.Helper()
	var b strings.Builder
	RenderSetupText(&b, s)
	got := hexRun.ReplaceAllString(b.String(), "<hex>") // digests move with the harness default; the shape must not
	path := filepath.Join("testdata", "setup", name+".golden")
	if *updateSetupGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-setup once, then review the file)", err)
	}
	if got != string(want) {
		t.Fatalf("%s render changed (run with -update-setup and review the diff):\n%s", name, got)
	}
}

func TestRoleSetupGolden(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	s, err := RoleSetup(context.Background(), d, "default", "dev", SetupSectionNames())
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "role", s)
}

func TestInstanceSetupGolden(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	s, err := InstanceSetup(context.Background(), d, "w1", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "studio", s)
}

func TestSetupNeverShowsSecrets(t *testing.T) {
	d, _, tok, _ := setupFixture(t)
	role, _ := RoleSetup(context.Background(), d, "default", "dev", SetupSectionNames())
	inst, _ := InstanceSetup(context.Background(), d, "w1", SetupSectionNames(), true)
	var text strings.Builder
	RenderSetupText(&text, role)
	RenderSetupText(&text, inst)
	js, _ := json.Marshal([]StudioSetup{role, inst})
	stored, _ := json.Marshal(d.Store.SetupSnapshots("w1"))
	for _, secret := range []string{"ghp_SECRETLITERAL", "hunter2", "ghp_SETTINGS", "u:p@", "oauthAccount", tok} {
		for where, s := range map[string]string{"text": text.String(), "json": string(js), "snapshots": string(stored)} {
			if strings.Contains(s, secret) {
				t.Errorf("%q in the %s", secret, where)
			}
		}
	}
	if !strings.Contains(text.String(), "GH_TOKEN=«masked»") || !strings.Contains(text.String(), "credential:  gh-bot") {
		t.Error("credentials must appear by name and secrets as marks")
	}
}

func TestInstanceSetupWithoutSnapshots(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	old := inst
	old.ActorID, old.ImageTag = "old", "" // raised before setup recording and image tags
	if err := d.Store.PutInstance(old); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "old", SetupSectionNames(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SetupSection{}
	for _, sec := range s.Sections {
		got[sec.Name] = sec
	}
	for _, n := range []string{SectionContext, SectionEgress, SectionEnv, SectionKit} {
		if got[n].Source != SourceRecomputed || got[n].Reason != reasonNoSnapshot || got[n].Body == "" {
			t.Errorf("%s = %+v, want recomputed with the fixed reason", n, got[n])
		}
	}
	if got[SectionSeed].Source != SourceUnavailable || got[SectionSeed].Reason != reasonImageUnknown {
		t.Errorf("seed = %+v", got[SectionSeed])
	}
	if len(s.Sections) != len(SetupSectionNames()) {
		t.Fatalf("every section must render: %d", len(s.Sections))
	}
}

func TestInstanceSetupStaleAndDiff(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	if _, err := SetRoleEgress(d.Store, "default", "dev", []string{"github.com", "npmjs.org"}); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "w1", []string{SectionIdentity, SectionEgress}, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sections[0].Diffed {
		t.Error("identity is never diffed")
	}
	eg := s.Sections[1]
	if !eg.Stale || eg.Source != SourceSnapshot || !eg.Diffed {
		t.Fatalf("egress = %+v, want a stale, diffed snapshot", eg)
	}
	has := func(op, text string) bool {
		for _, l := range eg.Diff {
			if l.Op == op && strings.Contains(l.Text, text) {
				return true
			}
		}
		return false
	}
	if !has("-", "policy: kit default") || !has("+", "policy: role (2 domain(s))") {
		t.Fatalf("diff = %+v", eg.Diff)
	}
}

func TestSetupImageUnavailable(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	for _, c := range []struct {
		images ImageFactsReader
		reason string
	}{{nil, reasonNoLauncher}, {&fakeImages{err: ErrImageUnreadable}, reasonImageError}, {&fakeImages{}, reasonNoImage}} {
		d.Images = c.images
		s, err := InstanceSetup(context.Background(), d, "w1", []string{SectionEnv, SectionSeed}, false)
		if err != nil {
			t.Fatal(err)
		}
		if seed := s.Sections[1]; seed.Source != SourceUnavailable || seed.Reason != c.reason {
			t.Errorf("seed = %+v, want unavailable: %s", seed, c.reason)
		}
		if env := s.Sections[0]; env.Source != SourceSnapshot || !strings.Contains(env.Body, "image env: unavailable ("+c.reason+")") {
			t.Errorf("env = %+v", env)
		}
	}
}

func TestInstanceSetupRoleGone(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	ghost := inst
	ghost.ActorID, ghost.Role = "g1", "ghost"
	if err := d.Store.PutInstance(ghost); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "g1", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Note != "the role no longer exists: no diff" {
		t.Errorf("note = %q", s.Note)
	}
	for _, sec := range s.Sections {
		if sec.Diffed {
			t.Errorf("%s diffed against a missing role", sec.Name)
		}
		if sec.Name == SectionPolicy && (sec.Source != SourceUnavailable || sec.Reason != reasonRoleGone || !strings.Contains(sec.Body, "idle override:")) {
			t.Errorf("policy = %+v", sec)
		}
	}
}

func TestSetupSectionsFilterAndNotFound(t *testing.T) {
	d, _, _, img := setupFixture(t)
	img.calls = nil
	s, err := RoleSetup(context.Background(), d, "default", "dev", []string{SectionEgress})
	if err != nil || len(s.Sections) != 1 || s.Sections[0].Name != SectionEgress || !strings.Contains(s.Sections[0].Body, ".anthropic.com  sealed") {
		t.Fatalf("filtered = %+v, %v", s, err)
	}
	if len(img.calls) != 0 {
		t.Fatalf("an egress-only inspect read the image: %v", img.calls)
	}
	if _, err := RoleSetup(context.Background(), d, "default", "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role err = %v", err)
	}
	if _, err := InstanceSetup(context.Background(), d, "nope", nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown studio err = %v", err)
	}
	d.Sup = nil
	s, err = RoleSetup(context.Background(), d, "default", "dev", []string{SectionContext, SectionKit})
	if err != nil || s.Sections[0].Reason != reasonNoSupervisor || s.Sections[1].Reason != reasonNoSupervisor {
		t.Fatalf("no supervisor = %+v, %v", s, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'Setup'`
Expected: FAIL to compile (`undefined: RoleSetup`, `undefined: SetupDeps`).

- [ ] **Step 3: Implement**

Create `internal/jam/setup_render.go`:

```go
package jam

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studio"
)

// SetupDeps are what a setup render reads beyond the store.
type SetupDeps struct {
	Store        Store
	Sup          *Supervisor      // nil: context and kit resolution unavailable
	Images       ImageFactsReader // nil: image parts unavailable (placeholder launcher)
	SealedEgress []string         // the hardening layer's always-on egress list
}

// Fixed section reasons: never an error text, never repo text.
const (
	reasonNoSnapshot        = "no snapshot (studio raised before setup recording); recomputed"
	reasonNoSupervisor      = "no supervisor on this Jam"
	reasonNoLauncher        = "no image reader (placeholder launcher)"
	reasonNoImage           = "image not present on the launcher host"
	reasonImageError        = "docker error on the launcher host"
	reasonImageUnknown      = "image tag unknown (raised before it was recorded, or the launcher cannot name images)"
	reasonNoKit             = "the role raises no kit (the launcher's static image)"
	reasonKitUnresolvable   = "the role's kit does not resolve; a raise would fail (see at-jam kit show)"
	reasonModelSpec         = "the role's model-spec does not resolve; a raise would fail"
	reasonConnectorConflict = "the role's destinations conflict; a raise would fail"
	reasonRoleGone          = "the role no longer exists"
)

// wouldBeActorID names the synthetic actor of a role render.
const wouldBeActorID = "would-be"

// wouldBe is what a raise of a role would deliver now.
type wouldBe struct {
	actor     Actor
	egress    *EgressPolicy
	def       KitDefinition
	haveKit   bool
	kitErr    error
	ceiling   []string
	ceilingOK bool
	context   sessionctx.Bundle
	imageTag  string
}

var errNoSupervisor = errors.New(reasonNoSupervisor)

// wouldBeRaise resolves a raise of role in project the way Raise does: the
// actor Enroll would make (one grant), the role's egress, its kit under its
// model-spec harness (raiseKit), and the session context compileContext
// would build for an ephemeral session with no unit and no owner.
func (s *Supervisor) wouldBeRaise(project string, role Role) (wouldBe, error) {
	if s == nil {
		return wouldBe{}, errNoSupervisor
	}
	pid := orDefaultProject(project)
	if id, ok := ProjectIDOf(s.store, pid); ok {
		pid = string(id)
	}
	wb := wouldBe{actor: Actor{ID: wouldBeActorID, Grants: []Grant{{Project: pid, Role: role.Name}}}}
	if role.Scope.Egress != nil {
		wb.egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	spec := RaiseSpec{ActorID: wouldBeActorID, Project: pid, Role: role.Name, Egress: wb.egress}
	var def *KitDefinition
	if d, have, err := s.raiseKit(pid, role); err != nil {
		wb.kitErr = err
	} else if have {
		wb.def, wb.haveKit, def = d, true, &d
		spec.Kit = d.Ref
		wb.ceiling, _ = studio.Ceiling(d.Kit.Egress)
		wb.ceilingOK = true
		wb.imageTag = s.imageTag(d.Ref)
	}
	wb.context = s.compileContext(spec, wb.actor, spec.Kit, def, nil)
	return wb, nil
}

// imageMemo reads an image at most once per render.
type imageMemo struct {
	done   bool
	facts  ImageFacts
	reason string
}

// readImage returns tag's facts, or the fixed reason they are unavailable;
// noKit, when set, is the reason (there is no image to read).
func (d SetupDeps) readImage(ctx context.Context, m *imageMemo, tag, noKit string) (ImageFacts, string) {
	if m.done {
		return m.facts, m.reason
	}
	m.done = true
	switch {
	case noKit != "":
		m.reason = noKit
	case d.Images == nil:
		m.reason = reasonNoLauncher
	case tag == "":
		m.reason = reasonImageUnknown
	default:
		f, err := d.Images.ImageFacts(ctx, tag)
		switch {
		case errors.Is(err, ErrImageNotPresent):
			m.reason = reasonNoImage
		case err != nil:
			m.reason = reasonImageError
		default:
			m.facts = f
		}
	}
	return m.facts, m.reason
}

func (d SetupDeps) imageEnvPart(ctx context.Context, m *imageMemo, tag, noKit string) string {
	f, reason := d.readImage(ctx, m, tag, noKit)
	if reason != "" {
		return "image env: unavailable (" + reason + ")\n"
	}
	return imageEnvBody(f)
}

func (d SetupDeps) imageLabelsPart(ctx context.Context, m *imageMemo, tag, noKit string) string {
	f, reason := d.readImage(ctx, m, tag, noKit)
	if reason != "" {
		return "image: unavailable (" + reason + ")\n"
	}
	return imageLabelsBody(f)
}

func (d SetupDeps) seedSection(ctx context.Context, m *imageMemo, tag, noKit string) SetupSection {
	f, reason := d.readImage(ctx, m, tag, noKit)
	if reason != "" {
		return unavailable(SectionSeed, reason)
	}
	return SetupSection{Name: SectionSeed, Source: SourceImage, Body: seedBody(f)}
}

// RoleSetup renders what a studio of project/role raised now would get.
// ErrNotFound for an unknown role; every other failure is a section's.
func RoleSetup(ctx context.Context, d SetupDeps, project, roleName string, sections []string) (StudioSetup, error) {
	r, ok := d.Store.GetRole(project, roleName)
	if !ok {
		return StudioSetup{}, fmt.Errorf("role %s/%s: %w", orDefaultProject(project), roleName, ErrNotFound)
	}
	out := StudioSetup{Subject: "role " + ProjectName(d.Store, orDefaultProject(project)) + "/" + r.Name}
	wb, wbErr := d.Sup.wouldBeRaise(project, r)
	actor := wb.actor
	if wbErr != nil {
		actor = Actor{ID: wouldBeActorID, Grants: []Grant{{Project: orDefaultProject(project), Role: r.Name}}}
	}
	noKit := ""
	switch {
	case wbErr != nil:
		noKit = reasonNoSupervisor
	case wb.kitErr != nil:
		noKit = reasonKitUnresolvable
	case !wb.haveKit:
		noKit = reasonNoKit
	}
	var img imageMemo
	for _, name := range sections {
		sec := SetupSection{Name: name, Source: SourceConfig}
		switch name {
		case SectionIdentity:
			sec.Body = roleIdentityBody(d.Store, project, r.Name)
		case SectionContext:
			if wbErr != nil {
				sec = unavailable(name, reasonNoSupervisor)
			} else {
				sec.Body = contextBody(wb.context)
			}
		case SectionEgress:
			egress := wb.egress
			if wbErr != nil && r.Scope.Egress != nil {
				egress = &EgressPolicy{Domains: slices.Clone(r.Scope.Egress.Domains)}
			}
			sec.Body = egressBody(egress, wb.ceiling, wb.ceilingOK) + sealedEgressBody(d.SealedEgress)
		case SectionDestinations:
			sec.Body = destinationsBody(d.Store, actor)
		case SectionEnv:
			if conn, err := ConnectorFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonConnectorConflict)
			} else {
				sec.Body = connectorBody(conn) + "\n"
			}
			sec.Body += d.imageEnvPart(ctx, &img, wb.imageTag, noKit)
		case SectionModel:
			if ms, err := ModelSpecFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonModelSpec)
			} else {
				sec.Body = modelBody(ms)
			}
		case SectionKit:
			switch {
			case wbErr != nil:
				sec = unavailable(name, reasonNoSupervisor)
			case wb.kitErr != nil:
				sec = unavailable(name, reasonKitUnresolvable)
			case !wb.haveKit:
				sec.Body = kitRefsBody(KitRef{}, nil, "")
			default:
				chain := chainOrSelf(chainRefs(wb.def.Chain), wb.def.Ref)
				sec.Body = kitRefsBody(wb.def.Ref, chainRefs(wb.def.Chain), wb.imageTag) + kitDetailBody(d.Store, chain, wb.def.Kit.BuildArgs) +
					d.imageLabelsPart(ctx, &img, wb.imageTag, "")
			}
		case SectionSeed:
			sec = d.seedSection(ctx, &img, wb.imageTag, noKit)
		case SectionMCP:
			sec.Body = mcpBody(wb.def.Kit.MCPServers, wb.haveKit)
		case SectionPolicy:
			sec.Body = rolePolicyBody(d.Store, project, r)
		}
		out.Sections = append(out.Sections, sec)
	}
	return out, nil
}

// InstanceSetup renders what studio ref (a session id or a live manual
// label) got: its snapshots, else a labelled recompute; config sections as
// of now; image facts for the image it runs. diffRole adds, per section, a
// line diff against RoleSetup of its role. ErrNotFound for an unknown or
// gone studio.
func InstanceSetup(ctx context.Context, d SetupDeps, ref string, sections []string, diffRole bool) (StudioSetup, error) {
	id := ResolveSession(d.Store, ref)
	inst, ok := d.Store.GetInstance(id)
	if !ok || inst.Phase == PhaseGone {
		return StudioSetup{}, fmt.Errorf("studio %s: %w", ref, ErrNotFound)
	}
	snaps := map[string]SetupSnapshot{}
	for _, s := range d.Store.SetupSnapshots(id) {
		snaps[s.Part] = s
	}
	actor := actorOf(d.Store, inst)
	role, roleOK := d.Store.GetRole(inst.Project, inst.Role)
	noKit := ""
	if inst.Kit.ID == "" && inst.ImageTag == "" {
		noKit = reasonNoKit
		if len(snaps) == 0 {
			noKit = "" // raised before kits were recorded: the tag is simply unknown
		}
	}
	out := StudioSetup{Subject: "studio " + id}
	var img imageMemo
	for _, name := range sections {
		var sec SetupSection
		switch name {
		case SectionIdentity:
			sec = SetupSection{Name: name, Source: SourceConfig, Body: instanceIdentityBody(d.Store, inst)}
		case SectionContext:
			sec = d.instanceContext(inst, actor, snaps)
		case SectionEgress:
			sec = d.instanceEgress(inst, role, roleOK, snaps)
		case SectionDestinations:
			sec = SetupSection{Name: name, Source: SourceConfig, Body: destinationsBody(d.Store, actor), Stale: connectorStale(d.Store, actor, inst.Connector)}
		case SectionEnv:
			sec = d.instanceEnv(inst, actor, snaps)
			sec.Body += d.imageEnvPart(ctx, &img, inst.ImageTag, noKit)
		case SectionModel:
			if ms, err := ModelSpecFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonModelSpec)
			} else {
				sec = SetupSection{Name: name, Source: SourceConfig, Body: modelBody(ms)}
			}
		case SectionKit:
			sec = d.instanceKitSection(ctx, &img, inst, snaps, noKit)
		case SectionSeed:
			sec = d.seedSection(ctx, &img, inst.ImageTag, noKit)
		case SectionMCP:
			sk, known := instanceKit(d.Store, inst)
			sec = SetupSection{Name: name, Source: SourceConfig, Body: mcpBody(sk.MCPServers, known)}
		case SectionPolicy:
			if roleOK {
				sec = SetupSection{Name: name, Source: SourceConfig, Body: rolePolicyBody(d.Store, inst.Project, role) + instancePolicyBody(inst)}
			} else {
				sec = SetupSection{Name: name, Source: SourceUnavailable, Reason: reasonRoleGone, Body: instancePolicyBody(inst)}
			}
		}
		out.Sections = append(out.Sections, sec)
	}
	if !diffRole {
		return out, nil
	}
	if !roleOK {
		out.Note = reasonRoleGone + ": no diff"
		return out, nil
	}
	rs, err := RoleSetup(ctx, d, inst.Project, inst.Role, sections)
	if err != nil {
		out.Note = "the role could not be rendered: no diff"
		return out, nil
	}
	applyDiff(&out, rs)
	return out, nil
}

func (d SetupDeps) instanceContext(inst Instance, actor Actor, snaps map[string]SetupSnapshot) SetupSection {
	var cur sessionctx.Bundle
	curOK := false
	if d.Sup != nil {
		if b, err := d.Sup.ContextForActor(actor); err == nil {
			cur, curOK = b, true
		}
	}
	if s, ok := snaps[SetupPartContext]; ok {
		return SetupSection{Name: SectionContext, Source: SourceSnapshot, At: s.At, Body: s.Body, Stale: curOK && cur.Fingerprint != s.Fingerprint}
	}
	if !curOK {
		return unavailable(SectionContext, reasonNoSupervisor)
	}
	return SetupSection{Name: SectionContext, Source: SourceRecomputed, Reason: reasonNoSnapshot, Body: contextBody(cur)}
}

func (d SetupDeps) instanceEgress(inst Instance, role Role, roleOK bool, snaps map[string]SetupSnapshot) SetupSection {
	var want *EgressPolicy
	if roleOK && role.Scope.Egress != nil {
		want = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	sec := SetupSection{Name: SectionEgress}
	if s, ok := snaps[SetupPartEgress]; ok {
		sec.Source, sec.At, sec.Body = SourceSnapshot, s.At, s.Body
		sec.Stale = roleOK && EgressFingerprint(want) != s.Fingerprint
		if inst.Egress != "" && inst.Egress != s.Fingerprint {
			sec.Body += "note: the supervisor's recorded policy differs from this snapshot (a re-apply is pending)\n"
		}
	} else {
		sk, known := instanceKit(d.Store, inst)
		var ceiling []string
		if known {
			ceiling, _ = studio.Ceiling(sk.Egress)
		}
		sec.Source, sec.Reason, sec.Body = SourceRecomputed, reasonNoSnapshot, egressBody(want, ceiling, known)
	}
	sec.Body += sealedEgressBody(d.SealedEgress)
	return sec
}

func (d SetupDeps) instanceEnv(inst Instance, actor Actor, snaps map[string]SetupSnapshot) SetupSection {
	cur, curErr := ConnectorFor(d.Store, actor)
	sec := SetupSection{Name: SectionEnv}
	switch s, ok := snaps[SetupPartConnector]; {
	case ok:
		sec.Source, sec.At, sec.Body = SourceSnapshot, s.At, s.Body
		sec.Stale = curErr == nil && snippet.Fingerprint(cur) != s.Fingerprint
		if inst.Connector != "" && inst.Connector != s.Fingerprint {
			sec.Body += "note: the studio last reported applying a different connector\n"
		}
	case curErr != nil:
		sec.Source, sec.Reason = SourceUnavailable, reasonConnectorConflict
	default:
		sec.Source, sec.Reason, sec.Body = SourceRecomputed, reasonNoSnapshot, connectorBody(cur)
	}
	sec.Body += "\n"
	return sec
}

func (d SetupDeps) instanceKitSection(ctx context.Context, m *imageMemo, inst Instance, snaps map[string]SetupSnapshot, noKit string) SetupSection {
	sec := SetupSection{Name: SectionKit, Source: SourceRecomputed, Reason: reasonNoSnapshot, Body: kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag)}
	if s, ok := snaps[SetupPartKit]; ok {
		sec.Source, sec.Reason, sec.At, sec.Body = SourceSnapshot, "", s.At, s.Body
	}
	sec.Stale = d.Sup != nil && imageStatus(d.Sup, map[[2]string]currentImage{}, inst) == "stale"
	if inst.Kit.ID != "" {
		sk, _ := instanceKit(d.Store, inst)
		sec.Body += kitDetailBody(d.Store, chainOrSelf(inst.KitChain, inst.Kit), sk.BuildArgs)
	}
	sec.Body += d.imageLabelsPart(ctx, m, inst.ImageTag, noKit)
	return sec
}

// actorOf is the instance's identity, or (revoked) the one grant it was raised with.
func actorOf(store Store, inst Instance) Actor {
	for _, a := range store.ListActors() {
		if a.ID == inst.ActorID {
			return a
		}
	}
	return Actor{ID: inst.ActorID, Grants: []Grant{{Project: inst.Project, Role: inst.Role}}}
}

// connectorStale: the connector the cove reported applying is not its current one.
func connectorStale(store Store, actor Actor, reported string) bool {
	want, err := ConnectorFor(store, actor)
	return err == nil && reported != "" && snippet.Fingerprint(want) != reported
}

// applyDiff sets each section's diff against the role render's same section
// (this studio −, a raise now +); identity is never diffed.
func applyDiff(out *StudioSetup, role StudioSetup) {
	byName := map[string]SetupSection{}
	for _, s := range role.Sections {
		byName[s.Name] = s
	}
	for i := range out.Sections {
		s := &out.Sections[i]
		r, ok := byName[s.Name]
		if !ok || s.Name == SectionIdentity {
			continue
		}
		s.Diff, s.DiffTooLarge = linediff.Diff(s.Body, r.Body)
		s.Diffed = true
	}
}
```

- [ ] **Step 4: Generate the goldens, review them, and run everything**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestRoleSetupGolden|TestInstanceSetupGolden' -update-setup`
Then **read both files** in `internal/jam/testdata/setup/` and confirm:
- every section is present in render order with the expected source;
- context shows `/agent-data/context/CORE.md` first, with `core budget used`;
- egress ends with `.anthropic.com  sealed`;
- env shows `ANTHROPIC_API_KEY=«identity token»`, `GH_TOKEN=«masked»` and `API_KEY=«masked»`;
- kit shows the three-link chain;
- the studio golden carries `-- same as a raise now` on the unchanged sections.

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_render.go internal/jam/setup_render_test.go internal/jam/testdata/setup/
git commit -m "feat(jam): RoleSetup / InstanceSetup with snapshots, recompute fallback and diff"
```

---

### Task 9: Admin routes and the admin client

**Files:**
- Create: `internal/jam/setup_admin.go`
- Modify: `internal/jam/adminclient/adminclient.go`
- Test: `internal/jam/setup_admin_test.go`, `internal/jam/adminclient/setup_test.go`

**Interfaces:**
- Consumes: Task 8's `SetupDeps`, `RoleSetup`, `InstanceSetup`; `ParseSetupSections`, `OperatorID`, `writeJSON`, `AdminOption`, `adminclient.Client.doWith`.
- Produces:
```go
func WithSetupInspect(d SetupDeps, log *slog.Logger) AdminOption
// adminclient
func (c *Client) RoleSetup(project, role string, sections []string) (jam.StudioSetup, error)
func (c *Client) StudioSetup(studio string, sections []string, diff bool) (jam.StudioSetup, error)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_admin_test.go`:

```go
package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupRoutes(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	ghost := inst
	ghost.ActorID, ghost.Role = "g1", "ghost"
	_ = d.Store.PutInstance(ghost)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := NewAdminHandler(d.Store, d.Sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, WithSetupInspect(d, log))
	ts := httptest.NewServer(h)
	defer ts.Close()
	get := func(path string) (int, StudioSetup, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var s StudioSetup
		_ = json.Unmarshal(b, &s)
		return resp.StatusCode, s, string(b)
	}
	if code, s, _ := get("/admin/roles/default/dev/setup?sections=egress,policy"); code != 200 || len(s.Sections) != 2 || s.Subject != "role default/dev" {
		t.Fatalf("role setup = %d %+v", code, s)
	}
	if code, s, _ := get("/admin/instances/w1/setup?diff=role"); code != 200 || len(s.Sections) != 10 || !s.Sections[1].Diffed {
		t.Fatalf("studio setup = %d %+v", code, s)
	}
	if code, s, _ := get("/admin/instances/g1/setup?diff=role"); code != 200 || s.Note == "" {
		t.Fatalf("role-gone studio = %d %+v", code, s)
	}
	for path, want := range map[string]int{
		"/admin/roles/default/dev/setup?sections=bogus": 400,
		"/admin/instances/w1/setup?diff=yes":            400,
		"/admin/roles/default/nope/setup":               404,
		"/admin/instances/nope/setup":                   404,
	} {
		if code, _, body := get(path); code != want {
			t.Errorf("%s = %d %s, want %d", path, code, body, want)
		}
	}
	if !strings.Contains(logs.String(), "setup inspected") || !strings.Contains(logs.String(), "sections=egress,policy") {
		t.Errorf("audit log missing: %s", logs.String())
	}
	if strings.Contains(logs.String(), "ANTHROPIC_BASE_URL") || strings.Contains(logs.String(), "--- /agent-data") {
		t.Errorf("a body reached the log: %s", logs.String())
	}
}

type denyAuth struct{}

func (denyAuth) Authenticate(*http.Request) (Operator, error) { return Operator{}, io.EOF }

func TestSetupRoutesNeedOperatorAuth(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(d.Store, d.Sup, nil, denyAuth{}, func(string) bool { return true }, nil, log, nil, WithSetupInspect(d, log)))
	defer ts.Close()
	for _, p := range []string{"/admin/roles/default/dev/setup", "/admin/instances/w1/setup"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", p, resp.StatusCode)
		}
	}
}
```

If `OperatorAuthenticator` or `Operator` are shaped differently, adapt `denyAuth` to the interface in `operator.go` (`Authenticate(*http.Request) (Operator, error)` is what `authMiddleware` calls).

Create `internal/jam/adminclient/setup_test.go`:

```go
package adminclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSetup(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"subject":"x","sections":[{"name":"egress","source":"config","body":"b"}]}`)
	}))
	defer ts.Close()
	c := New(ts.URL, "")
	s, err := c.RoleSetup("my proj", "dev", []string{"egress", "kit"})
	if err != nil || s.Subject != "x" || s.Sections[0].Body != "b" {
		t.Fatalf("RoleSetup = %+v, %v", s, err)
	}
	if _, err := c.StudioSetup("w1", nil, true); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/admin/roles/my%20proj/dev/setup?sections=egress%2Ckit" || got[1] != "/admin/instances/w1/setup?diff=role" {
		t.Fatalf("paths = %v", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestSetupRoutes; GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminclient/ -run TestClientSetup`
Expected: FAIL to compile (`undefined: WithSetupInspect`, `c.RoleSetup undefined`).

- [ ] **Step 3: Implement**

Create `internal/jam/setup_admin.go`:

```go
package jam

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// WithSetupInspect mounts the setup inspection routes (inside the operator
// authenticator, like every /admin/* route):
//
//	GET /admin/roles/{project}/{role}/setup   — what a raise now would get
//	GET /admin/instances/{id}/setup           — what a studio got (?diff=role)
//
// both taking ?sections=a,b. Each call is logged "setup inspected" with the
// operator, subject and sections — never a body.
func WithSetupInspect(d SetupDeps, log *slog.Logger) AdminOption {
	sectionsOf := func(w http.ResponseWriter, r *http.Request) ([]string, bool) {
		s, err := ParseSetupSections(r.URL.Query().Get("sections"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil, false
		}
		return s, true
	}
	audit := func(r *http.Request, subject string, sections []string) {
		log.Info("setup inspected", "operator", OperatorID(r), "subject", subject, "sections", strings.Join(sections, ","))
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("GET /admin/roles/{project}/{role}/setup", func(w http.ResponseWriter, r *http.Request) {
			sections, ok := sectionsOf(w, r)
			if !ok {
				return
			}
			project, role := r.PathValue("project"), r.PathValue("role")
			s, err := RoleSetup(r.Context(), d, project, role, sections)
			if errors.Is(err, ErrNotFound) {
				http.Error(w, fmt.Sprintf("role %s/%s does not exist", project, role), http.StatusNotFound)
				return
			}
			if err != nil {
				log.Warn("setup inspect failed", "subject", "role "+project+"/"+role, "err", err.Error())
				http.Error(w, "setup unavailable", http.StatusInternalServerError)
				return
			}
			audit(r, s.Subject, sections)
			writeJSON(w, http.StatusOK, s)
		})
		mux.HandleFunc("GET /admin/instances/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
			sections, ok := sectionsOf(w, r)
			if !ok {
				return
			}
			diff := r.URL.Query().Get("diff")
			if diff != "" && diff != "role" {
				http.Error(w, `diff: want "role"`, http.StatusBadRequest)
				return
			}
			id := r.PathValue("id")
			s, err := InstanceSetup(r.Context(), d, id, sections, diff == "role")
			if errors.Is(err, ErrNotFound) {
				http.Error(w, fmt.Sprintf("studio %s not found", id), http.StatusNotFound)
				return
			}
			if err != nil {
				log.Warn("setup inspect failed", "subject", "studio "+id, "err", err.Error())
				http.Error(w, "setup unavailable", http.StatusInternalServerError)
				return
			}
			audit(r, s.Subject, sections)
			writeJSON(w, http.StatusOK, s)
		})
	}
}
```

In `internal/jam/adminclient/adminclient.go`, after `ClearEgress`:

```go
// setupTimeout bounds a setup inspect: a first read of an image's seed runs
// docker create/cp on the launcher host.
const setupTimeout = 60 * time.Second

// setupPath is a setup route with its sections and diff query.
func setupPath(path string, sections []string, diff bool) string {
	q := url.Values{}
	if len(sections) > 0 {
		q.Set("sections", strings.Join(sections, ","))
	}
	if diff {
		q.Set("diff", "role")
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}

func (c *Client) doSetup(path string, out *jam.StudioSetup) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = setupTimeout
	return c.doWith(&hc, "GET", path, nil, out)
}

// RoleSetup is what a studio of project/role raised now would get (sections
// nil = all).
func (c *Client) RoleSetup(project, role string, sections []string) (jam.StudioSetup, error) {
	var out jam.StudioSetup
	err := c.doSetup(setupPath("/admin/roles/"+url.PathEscape(project)+"/"+url.PathEscape(role)+"/setup", sections, false), &out)
	return out, err
}

// StudioSetup is what studio (an id or a live manual label) got; diff adds
// each section's line diff against its role's render.
func (c *Client) StudioSetup(studio string, sections []string, diff bool) (jam.StudioSetup, error) {
	var out jam.StudioSetup
	err := c.doSetup(setupPath("/admin/instances/"+url.PathEscape(studio)+"/setup", sections, diff), &out)
	return out, err
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./internal/jam/adminclient/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup_admin.go internal/jam/setup_admin_test.go internal/jam/adminclient/adminclient.go internal/jam/adminclient/setup_test.go
git commit -m "feat(jam): GET /admin/roles/…/setup and /admin/instances/{id}/setup, admin client"
```

---

### Task 10: CLI verbs and serve wiring

**Files:**
- Create: `cmd/at-jam/inspect.go`
- Modify: `cmd/at-jam/main.go` (`cmdRole`, `cmdStudio` dispatch + usage; serve wiring around the launcher block and `NewAdminHandler`), `internal/assemble/embed.go`
- Test: `cmd/at-jam/inspect_test.go`, `internal/assemble/embed_test.go`

**Interfaces:**
- Consumes: Task 9's client methods and `WithSetupInspect`; `jam.RenderSetupText`, `jam.ParseSetupSections`; `imagefacts.New`; `colima.NewWithContext`; `multiFlag`, `cli.ParseFlags`, `validateApp`, `loadSettings`, `resolveToken`, `adminTokenEnv`, `firstNonEmpty`, `defaultApp`, `defaultAdminURL`.
- Produces:
```go
// package assemble
func SealedEgressBase() ([]string, error)
// package main
func cmdRoleInspect(args []string, stdout, stderr io.Writer) int
func cmdStudioInspect(args []string, stdout, stderr io.Writer) int
```

- [ ] **Step 1: Write the failing tests**

Create `internal/assemble/embed_test.go` (or append to an existing test file in the package):

```go
package assemble

import (
	"slices"
	"strings"
	"testing"
)

func TestSealedEgressBase(t *testing.T) {
	got, err := SealedEgressBase()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, ".github.com") || slices.ContainsFunc(got, func(s string) bool { return s == "" || strings.HasPrefix(s, "#") }) {
		t.Fatalf("sealed base = %v", got)
	}
}
```

Create `cmd/at-jam/inspect_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func inspectServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.PutRole(jam.DefaultProject, jam.Role{Name: "dev", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(st, placeholderLauncher{}, "h", time.Minute, 30*time.Second, nil, log)
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "s1", Project: jam.DefaultProject, Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	d := jam.SetupDeps{Store: st, Sup: sup, SealedEgress: []string{".github.com"}}
	ts := httptest.NewServer(jam.NewAdminHandler(st, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, jam.WithSetupInspect(d, log)))
	t.Cleanup(ts.Close)
	return ts
}

func TestRoleAndStudioInspect(t *testing.T) {
	ts := inspectServer(t)
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	run1 := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(args, getenv, &out, &errb)
	}
	if code := run1("role", "inspect", "default/dev", "--admin-url", ts.URL, "--section", "egress", "--section", "policy"); code != 0 {
		t.Fatalf("role inspect = %d: %s", code, errb.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "# role default/dev\n") || !strings.Contains(got, "\n== egress (config)\n") || !strings.Contains(got, "\n== policy (config)\n") || strings.Contains(got, "== kit") {
		t.Fatalf("role inspect output:\n%s", got)
	}
	if code := run1("studio", "inspect", "s1", "--admin-url", ts.URL, "--diff", "--json", "--section", "egress"); code != 0 {
		t.Fatalf("studio inspect = %d: %s", code, errb.String())
	}
	var s jam.StudioSetup
	if err := json.Unmarshal(out.Bytes(), &s); err != nil || s.Subject != "studio s1" || len(s.Sections) != 1 || !s.Sections[0].Diffed {
		t.Fatalf("--json = %+v, %v\n%s", s, err, out.String())
	}
	if code := run1("role", "inspect", "dev", "--admin-url", ts.URL, "--section", "egress"); code != 0 || !strings.HasPrefix(out.String(), "# role default/dev") {
		t.Fatalf("a bare role means the default project: %d %s", code, out.String())
	}
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"role", "inspect", "--admin-url", ts.URL}, 2},
		{[]string{"role", "inspect", "default/dev", "--admin-url", ts.URL, "--section", "bogus"}, 2},
		{[]string{"studio", "inspect", "--admin-url", ts.URL}, 2},
		{[]string{"studio", "inspect", "nope", "--admin-url", ts.URL}, 1},
		{[]string{"role", "inspect", "default/nope", "--admin-url", ts.URL}, 1},
	} {
		if code := run1(c.args...); code != c.code {
			t.Errorf("%v = %d (%s), want %d", c.args, code, errb.String(), c.code)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/assemble/ -run TestSealedEgressBase; GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run TestRoleAndStudioInspect`
Expected: FAIL (`undefined: SealedEgressBase`; `at-jam role: unknown subcommand inspect`).

- [ ] **Step 3: Implement**

Append to `internal/assemble/embed.go` (imports `strings`):

```go
// SealedEgressBase is the sealed hardening layer's always-on egress list
// (/etc/squid/allowed_domains.txt) as baked into every image this binary
// assembles — the "sealed" rows of Jam's setup inspection.
func SealedEgressBase() ([]string, error) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, nil
}
```

Create `cmd/at-jam/inspect.go`:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
)

// inspectFlags are the flags role inspect and studio inspect share.
type inspectFlags struct {
	app, adminURL, token *string
	json                 *bool
	sections             multiFlag
}

func addInspectFlags(fs *flag.FlagSet, stderr io.Writer) *inspectFlags {
	f := &inspectFlags{
		app:      fs.String("app", defaultApp, "settings/token profile"),
		adminURL: fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)"),
		token:    fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)"),
		json:     fs.Bool("json", false, "print the setup as JSON"),
	}
	fs.Var(&f.sections, "section", "only these sections (repeatable or comma-separated): "+strings.Join(jam.SetupSectionNames(), ", "))
	return f
}

// client validates the shared flags and returns the admin client and the
// section list (nil = all); code != 0 means stop with it.
func (f *inspectFlags) client(verb string, stderr io.Writer) (*adminclient.Client, []string, int) {
	if err := validateApp(*f.app); err != nil {
		fmt.Fprintf(stderr, "at-jam %s: %v\n", verb, err)
		return nil, nil, 2
	}
	var sections []string
	if len(f.sections) > 0 {
		s, err := jam.ParseSetupSections(strings.Join(f.sections, ","))
		if err != nil {
			fmt.Fprintf(stderr, "at-jam %s: %v\n", verb, err)
			return nil, nil, 2
		}
		sections = s
	}
	adminURL := firstNonEmpty(*f.adminURL, loadSettings(*f.app).AdminURL, defaultAdminURL)
	return adminclient.New(adminURL, resolveToken(*f.app, *f.token, stderr)), sections, 0
}

func (f *inspectFlags) print(s jam.StudioSetup, stdout io.Writer) {
	if *f.json {
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return
	}
	jam.RenderSetupText(stdout, s)
}

// cmdRoleInspect is `at-jam role inspect <project>/<role>`: what a studio of
// the role raised now would get (a bare <role> is in the default project).
func cmdRoleInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("role inspect", flag.ContinueOnError)
	f := addInspectFlags(fs, stderr)
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 || pos[0] == "" {
		fmt.Fprintln(stderr, "usage: at-jam role inspect <project>/<role> [--json] [--section name]…")
		return 2
	}
	project, role, found := strings.Cut(pos[0], "/")
	if !found {
		project, role = jam.DefaultProject, pos[0]
	}
	c, sections, code := f.client("role inspect", stderr)
	if code != 0 {
		return code
	}
	s, err := c.RoleSetup(project, role, sections)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	f.print(s, stdout)
	return 0
}

// cmdStudioInspect is `at-jam studio inspect <studio> [--diff]`: what the
// studio got, optionally diffed against a raise of its role now.
func cmdStudioInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("studio inspect", flag.ContinueOnError)
	f := addInspectFlags(fs, stderr)
	diff := fs.Bool("diff", false, "add each section's line diff against what a raise of its role would get now")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 || pos[0] == "" {
		fmt.Fprintln(stderr, "usage: at-jam studio inspect <studio> [--diff] [--json] [--section name]…")
		return 2
	}
	c, sections, code := f.client("studio inspect", stderr)
	if code != 0 {
		return code
	}
	s, err := c.StudioSetup(pos[0], sections, *diff)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	f.print(s, stdout)
	return 0
}
```

In `cmd/at-jam/main.go`:
- `cmdRole`: change the usage line to `"at-jam role: expected add|list|rm|inspect"`. Right after `sub, rest := args[0], args[1:]`, add:
```go
	if sub == "inspect" {
		return cmdRoleInspect(rest, stdout, stderr)
	}
```
- `cmdStudio`: change the usage line to `"at-jam studio: expected raise|list|status|teardown|inspect|share|unshare|access"`, and add `case "inspect": return cmdStudioInspect(rest, stdout, stderr)` to the early `switch sub` (beside `share`/`unshare`/`access`).
- The command table briefs (`{Name: "role", Brief: …}`, `{Name: "studio", Brief: …}`): add `inspect` to each list.
- Serve wiring (imports `github.com/aethons-tools/cove/internal/assemble`, `github.com/aethons-tools/cove/internal/jam/imagefacts`). Replace the backend construction in the launcher block:
```go
	var images jam.ImageFactsReader // nil with the placeholder launcher: image parts unavailable
	if lc := cfg.Runtime.Launcher; lc != nil {
		// docker-context picks the colima instance (profile) studios run in.
		col := colima.NewWithContext(runner.OS{}, lc.DockerContext)
		var be launcher.Backend = col
		images = imagefacts.New(runner.OS{}, col) // setup inspection reads images on the same daemon
```
(the rest of the block is unchanged). Before `admin := jam.NewAdminHandler(…)`:
```go
		sealed, err := assemble.SealedEgressBase()
		if err != nil {
			log.Warn("setup inspect: sealed egress base unreadable; egress shows no sealed rows", "err", err.Error())
		}
		setupDeps := jam.SetupDeps{Store: st, Sup: sup, Images: images, SealedEgress: sealed}
```
and add `jam.WithSetupInspect(setupDeps, log),` to the `NewAdminHandler` options after `jam.WithConfig(…)`.

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/assemble/ ./cmd/at-jam/ && GOPROXY=https://proxy.golang.org,direct go build ./... && scripts/lint.sh`
Expected: PASS, build and lint clean.

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/embed.go internal/assemble/embed_test.go cmd/at-jam/inspect.go cmd/at-jam/inspect_test.go cmd/at-jam/main.go
git commit -m "feat(at-jam): role inspect / studio inspect; wire setup inspection into serve"
```

---

### Task 11: Docs

**Files:**
- Create: `docs/usage/jam/inspect.md`
- Modify: `docs/usage/jam/INDEX.md` (a row after `coves.md`), `docs/usage/jam/session-context.md` (Refresh), `docs/usage/jam/connector.md` (one pointer line)

**Interfaces:**
- Consumes: the behaviour of Tasks 1–10.
- Produces: the doc that owns setup inspection.

- [ ] **Step 1: Record the audit baseline**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: `… 722 error(s) …`: 720 at the spec commit plus 2 for this plan file. Note the number.

- [ ] **Step 2: Write the leaf**

Create `docs/usage/jam/inspect.md`:

```markdown
---
summary: Seeing everything a studio's agent is set up with from outside the container — `at-jam role inspect` / `studio inspect [--diff]` and their admin routes, the sections and sources, snapshots, image facts, and redaction.
read_when: You want to know what a studio (or a raise of a role now) gets — its session-context files, egress, destinations, env, model-spec, kit and image, /agent-data seed, MCP servers or policy — or why a studio differs from its role.
owns: setup inspection (role/studio inspect, GET …/setup routes), setup snapshots, the image-facts reader, setup redaction rules
prereqs: coves.md, session-context.md, connector.md
tier: leaf
updated: 2026-10-24
---

# Inspecting a studio's setup

`at-jam role inspect <project>/<role>` shows what a studio of the role raised
**now** would get. `at-jam studio inspect <studio>` shows what a running studio
**got**; `--diff` adds, per section, a line diff (`-` this studio, `+` a raise
now). Both take `--json` and `--section <name>` (repeatable or comma-separated).
A bare `<role>` is in the default project; a studio is its session id or a live
manual label.

Routes (operator auth, like every `/admin/*` route; each call is logged
`setup inspected` with operator, subject and sections — never the body):

- `GET /admin/roles/{project}/{role}/setup?sections=a,b`
- `GET /admin/instances/{id}/setup?sections=a,b&diff=role`

## Sections

| Section | Shows |
|---|---|
| `identity` | project, role, session kind, studio id, phase, raised at (a role render is a would-be ephemeral session: no unit, no owner) |
| `context` | every [session-context](session-context.md) file at its real path under `/agent-data/context/`, sizes, each layer's core budget used, compile warnings |
| `egress` | the effective allow-list — the role's list (marked when outside the kit ceiling) or the kit default — then the sealed base rows |
| `destinations` | per destination in scope: route, upstream, identity-in/apply, credential **name**, git routing, allow-paths, env keys |
| `env` | the [connector](connector.md) env an agent spawn gets (redacted), then the image `ENV` and its `COVE_SSHENV` transfer set |
| `model` | the resolved model-spec, as of now |
| `kit` | kit ref, chain root first with provenance, build args, image tag, image id and labels |
| `seed` | what the image seeds into `/agent-data`: `CLAUDE.md` and its `@` imports, skills, `settings.json`, the `.refresh` list; other files by path and size |
| `mcp` | the kit's MCP servers (no args, no header values) and the intercom tools |
| `policy` | allocation, standing, turn-end, collaborate, escalation; for a studio its idle override and alarms |

Each section is headed `== <name> (<source>[, stale][, <at>])`. Sources:
`snapshot` (what Jam recorded delivering), `recomputed` (no snapshot — a studio
raised before recording; rendered from config now), `config` (as of now),
`image`, `unavailable` (with a fixed reason). `stale` marks a studio part whose
delivered fingerprint differs from what its role would deliver now.

## Snapshots

Jam records the masked form of four parts **when it delivers them**, only when
the fingerprint changes: `context` (raise, `GET /context`), `connector` (raise,
`GET /connector`), `egress` (raise, each successful re-apply), `kit` (raise).
Only the latest per part is kept, capped at 64 KiB; they go with the studio and
are not part of `at-jam export`. A failed write is logged, never fatal.

## Image facts

Read on the launcher host through the launcher's docker context:
`docker image inspect` (env, labels, id) and, for the seed, `docker create`
(never started), `docker cp <ctr>:/home/agent/.init-agent-data -`,
`docker rm -f`. Caps: 1 MiB, 200 entries, text ≤ 64 KiB. The seed is cached per
image id. Image gone, docker failing, or no launcher (placeholder): the image
parts are `unavailable` and the rest renders.

## Redaction

No secret value is shown or stored. Credentials appear by name. The identity
token reads `«identity token»`. A value under a key matching
`token|secret|key|password|passwd|credential` (any case) reads `«masked»`
unless it is only placeholders (`{token}`, `{base}`, `{host}`, `{url}`, with at
most a `Bearer `/`token ` scheme). `{base}`/`{host}` stay as placeholders.
The same rule masks image `ENV`, build args, label values and `settings.json`.
URLs lose their userinfo; a label that is not clean UTF-8 text reads `invalid`.

Design: [the spec](../../superpowers/specs/2026-10-24-studio-setup-inspect-design.md).
```

- [ ] **Step 3: Index and point to it**

In `docs/usage/jam/INDEX.md`, add after the `coves.md` row:

```markdown
| [inspect.md](inspect.md) | You want to see everything a studio (or a raise of a role now) is set up with — context files, egress, destinations, env, model-spec, kit/image, seed, MCP, policy — or why a studio differs from its role: `role inspect`, `studio inspect --diff`. |
```

In `docs/usage/jam/session-context.md`, at the end of `## Refresh` (before "Studio rendering:"), add:

```markdown
To see the files a studio was last delivered — or what a raise would deliver now, and the difference — use [`studio inspect` / `role inspect`](inspect.md).
```

In `docs/usage/jam/connector.md`, after the paragraph describing `GET /connector`, add:

```markdown
`at-jam studio inspect <studio> --section env` shows the connector a studio was last served (redacted) — see [inspect.md](inspect.md).
```

Bump `updated:` to `2026-10-24` in the frontmatter of the three modified docs.

- [ ] **Step 4: Audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: errors ≤ the Step 1 number (722). `inspect.md` must not appear as an orphan, a dangling link or oversize. Fix anything it reports about the new or touched files.

- [ ] **Step 5: Commit**

```bash
git add docs/usage/jam/inspect.md docs/usage/jam/INDEX.md docs/usage/jam/session-context.md docs/usage/jam/connector.md
git commit -m "docs(jam): setup inspection (inspect.md) and pointers"
```

---

## Self-review

**Spec coverage**

| Spec requirement | Task |
|---|---|
| §1 every section | `identity`, `destinations`, `model`, `mcp`, `policy`: 3. `context`, `egress`, `env`: 2. `kit`, `seed`: 7. All assembled in 8. |
| §1 redaction | 1, with storage through the same builders (5) |
| §2 snapshots: four parts, write on delivery and on change, 64 KiB cap, Postgres + Mem + storetest, dropped with the instance, not exported, failure logged | 4, 5 |
| §3 image facts: inspect, create/cp/rm, caps, LRU per digest, unavailable paths, argv via the runner | 6; rendered in 7 and 8 |
| §4 slice 1: both routes, `?diff=role`, `?sections`, both CLI verbs, the text format, admin auth, audit log | 2 (text), 9 (routes, client), 10 (CLI) |
| §5 errors: never fails as a whole, fixed reasons, 404s | 8, 9 |
| §6 golden tests, redaction fixture, snapshot write-on-change / cap / drop / parity, image reader with `runner.Fake`, diff staleness | 8; 1, 8; 4, 5; 6; 8 |
| Docs | 11 |

**Placeholder scan.**
- Every code step carries the code.
- Two adaptations are flagged rather than left open. Task 9 adapts `denyAuth` if `Operator`'s shape differs. Task 8's goldens are generated once with `-update-setup` and then reviewed against a listed checklist, because their exact bytes depend on boilerplate text and digests the plan cannot reproduce by hand (hex runs are normalized).

**Type consistency.**
- `SetupSection` and `StudioSetup` are defined in Task 2 and used unchanged in Tasks 8–10.
- `SetupSnapshot` and `SetupPart*` are defined in Task 4 and used in Tasks 5 and 8.
- `ImageFacts` and `ImageFactsReader` are defined in Task 6 and consumed in Tasks 7, 8 and 10.
- `instanceKit` is defined in Task 5 and used in Task 8.
- `seedFacts` is a Task 7 test helper that Task 8's fixture reuses; both are in package `jam` tests.
- `reason*` constants are defined in Task 8 and asserted only there.

**Review Focus check.** Each of the five lines names its test in its owning task: Task 1 and Task 8 for secrets, Task 8 for pre-feature studios, Task 5 for the refresh loop, Tasks 6 and 8 for docker failure, and Tasks 8 and 9 for a deleted role.
