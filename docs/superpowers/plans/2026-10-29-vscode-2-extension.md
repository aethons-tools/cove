# VS Code slice 2 (extension, sign-in, SSH, getting in) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A VS Code extension `aethons.jam` (in `web/vscode/`, shipped as a `.vsix`) that:
- signs in to one Jam with an app token;
- writes the studio's SSH entry and cert, as `jam ssh-config --write` does;
- opens `vscode://aethons.jam/session/<ses_id>` as that studio's Remote-SSH window.

The web app's session header gets an *Open in VS Code* button, and `jam ssh-proxy` uses the extension's token.

**Architecture:**
- **Pure modules, a thin VS Code layer.** Most of the extension is plain TypeScript taking injected dependencies (fetch, fs paths, an `execFile`, a `Ui` interface): PKCE, auth, the API client, the token file, the ssh_config editor, key/cert handling, the open flow. Vitest tests them without VS Code. Only `src/extension.ts` imports `vscode`; it builds the real dependencies and registers commands and the URI handler. A small `vscode` mock (vitest alias) covers that wiring.
- **SSH stays the CLI's.** The extension writes the *same* `Host jam-<studio>` block `jam ssh-config --write --participant --admin-url <jam.url> <studio>` writes, plus one marker comment line. A golden file shared by a Go test and a TS test pins the two byte for byte. The ProxyCommand is still `jam ssh-proxy`. The extension hands `ssh-proxy` its token through `~/.config/jam/default-app-token.json`, and `sshFlags.resolve` prefers that file when it names the same Jam.
- **The token never reaches argv, a URL or the studio.** It lives in `SecretStorage` and the 0600 token file. The extension is `extensionKind: ["ui"]`.

**Tech Stack:** TypeScript 6, esbuild (bundle), Vitest 5 (tests), `@vscode/vsce` (package), `@types/vscode` 1.105; Go (cmd/jam); React 19 + Mantine 9 (web app).

**Spec:** `docs/superpowers/specs/2026-10-28-vscode-code-window-design.md` ("Getting in", "Sign-in: a Jam-minted app token", "SSH", "Which session a window belongs to", "Left to the builder"). Slice 2 of 5; consumes slice 1 (`docs/superpowers/plans/2026-10-29-vscode-1-app-tokens.md`, Tasks 3 and 5).

## Global Constraints

- **Identity:**
  - publisher `aethons`, name `jam`, display name "Jam";
  - `extensionKind: ["ui"]`;
  - engine `"vscode": "^1.105.0"`;
  - desktop VS Code only;
  - SSH on macOS and Linux only, the platforms the `jam` CLI ships for (`.goreleaser.yaml`). On `win32` the SSH step says so.
- **Settings:**
  - `jam.url` (string, default `""`): the Jam's base URL, normalized to its origin. It must be `https:`, except `http:` for `localhost`, `127.0.0.1` and `[::1]`.
  - `jam.cliPath` (string, default `""`): an absolute path to `jam`; empty means search `PATH`.
  - Both have `"scope": "application"`, so a workspace can't set them.
  - One Jam per extension.
- **Sign-in (slice 1's contract):**
  - The extension opens `GET <jam.url>/app/auth/app-token?redirect=<scheme>://aethons.jam/auth&state=<s>&code_challenge=<c>&code_challenge_method=S256&client=VS Code on <os.hostname()>`. `<scheme>` is `vscode.env.uriScheme`, which must be `vscode` or `vscode-insiders`.
  - The callback is `<scheme>://aethons.jam/auth?code=…&state=…` or `?error=access_denied&state=…`.
  - It then sends `POST <jam.url>/app/auth/app-token/exchange` `{code, code_verifier, redirect}` and gets `200 {token, expires_at, user}` back. Any failure is `400 {"error":"invalid_grant"}`.
  - Verifier: 32 random bytes, base64url (43 chars). State: 32 random bytes, base64url.
  - A pending sign-in lives `SIGN_IN_TIMEOUT_MS = 5 * 60_000`, in memory only. A new Sign In replaces it.
- **Storage:**
  - `SecretStorage` key `jam.session` holds JSON `{url, token, user, expiresAt}`. A stored session whose `url` isn't the current `jam.url` counts as signed out.
  - The token file is `<configDir>/default-app-token.json` = `{"token","jam_url"}`, mode 0600, its directory 0700, written atomically.
  - `configDir` is `$XDG_CONFIG_HOME/jam`, else `~/.config/jam`, matching `cmd/jam/clientconfig.go:configDir`. Sign Out and any 401 delete both.
- **API:**
  - every request carries `Authorization: Bearer <token>`, `Accept: application/json` and `redirect: "error"`;
  - a 401 anywhere means signed out;
  - user-API error bodies are `{error, code}` (`userui/api.go:122`); `/me/studios` bodies are text.
- **SSH (the CLI's values, `cmd/jam/ssh.go`):**
  - the key is `<configDir>/ssh/id_ed25519`, ed25519;
  - the cert is `<configDir>/ssh/<studio>-cert.pub`, mode 0644, renewed when under `CERT_RENEW_BEFORE_MS = 3_600_000` remains;
  - the alias is `jam-<studio>`;
  - the markers are `# >>> jam <studio> >>>` / `# <<< jam <studio> <<<`;
  - the extension's marker line is exactly `# managed by the Jam VS Code extension (aethons.jam)`, the line right after the begin marker.
- **Remote:**
  - the window opens `vscode-remote://ssh-remote+jam-<studio>/home/agent/workspace` via `vscode.openFolder` with `{ forceNewWindow: true }`;
  - the studio id is the session id, which must match `SESSION_ID_RE = /^ses_[0-7][0-9a-hjkmnp-tv-z]{25}$/` (`internal/ident`).
- **Processes:** child processes run through `execFile` with an argv array, never a shell. No token in any argv, URL Jam doesn't define, log or the studio.
- **Web links:** a session is `<jam.url>/app/user/c/<channel>` (`web/app/src/App.tsx` route `c/:channel`). Without a channel it is `<jam.url>/app/user/`.

## Review Focus

1. **A workspace `.vscode/settings.json`** that sets `jam.url` (to steal the token) or `jam.cliPath` (to run a binary) must have no effect. Task 3 tests that both settings are application-scoped.
2. **A token meeting the wrong Jam:**
   - after `jam.url` changes, the stored session is treated as signed out and its token is never sent (Task 4);
   - a token file naming another Jam is ignored by `ssh-proxy` (Task 1).
3. **A user's existing `~/.ssh/config`** is never damaged:
   - a `jam ssh-config` block or a hand-written `Host jam-<studio>` block is left byte for byte;
   - a begin marker without its end, or two begin markers, is an error with nothing written;
   - CRLF endings, the file's mode and a symlinked config are kept.

   Task 5 tests it.
4. **A crafted link** such as `vscode://aethons.jam/session/ses_x%0AHost%20*`, `…/session/../../x` or an unknown path is refused before any API call or file write. Task 7 tests it.
5. **A token revoked mid-use:** a 401 from the API or the cert route signs out, clearing the secret and the token file, and offers "Sign In". A fresh sign-in then works without a reload. Tasks 4 and 6 test it.

---

### Task 1: ssh-proxy uses the extension's app token

**Files:**
- Modify:
  - `cmd/jam/clientconfig.go`: `appTokenPath`, `loadAppToken`, beside `participantTokenPath`;
  - `cmd/jam/ssh.go`: `sshTarget.appToken`; `resolve` checks `loadAppToken` before `resolveParticipantToken`; `sshErrMessage`'s 401 case.
- Create: `web/vscode/test/fixtures/ssh-block.txt`, the golden block for studio `ses_0abcdefghjkmnpqrstvwxyz012`, jam `/usr/local/bin/jam`, `--admin-url 'https://jam.example.com'`, `XDG_CONFIG_HOME=/home/u/.config`. It is Go's `sshConfigBlock()` with the marker line inserted as line 2.
- Test: `cmd/jam/clientconfig_test.go`, `cmd/jam/ssh_test.go`.

**Interfaces:**
- Produces:
  ```go
  func appTokenPath(app string) string // configDir()/<app>-app-token.json
  type appTokenFile struct{ Token string `json:"token"`; JamURL string `json:"jam_url"` }
  func loadAppToken(app, adminURL string) (string, bool) // ok: file parses, Token != "", TrimRight(JamURL,"/") == TrimRight(adminURL,"/")
  ```
  - In `resolve`, when not `--operator`, a loaded app token sets `participant=true, token, appToken=true` and returns.
  - `sshErrMessage` 401 with `t.appToken`: `"your VS Code sign-in to Jam is missing or was revoked; run Jam: Sign In in VS Code"`.
  - `ssh-config --prune` and `studio share|access` pick it up through `resolve` unchanged.

- [x] **Step 1: Write the failing tests:**
  - `TestResolvePrefersAppTokenFile`: with an app token file and a valid participant ID token for the same URL, the result is the app token, `participant && appToken`.
  - `TestResolveAppTokenOtherJamIgnored` (Review Focus 2): the file's `jam_url` is `https://other.example.com`, so the participant ID token is used.
  - `TestResolveAppTokenOperatorFlagIgnores`.
  - `TestResolveAppTokenMalformedIgnored`, a table: bad JSON, empty token, missing file.
  - `TestAppTokenPathDistinct`: for apps `default`, `x-app`, `x`, no `appTokenPath` equals any `tokenPath` or `participantTokenPath`.
  - `TestSSHErrMessageAppToken401`.
  - `TestExtensionSSHBlockGolden`: stub `sshSelf` → `/usr/local/bin/jam` and set `XDG_CONFIG_HOME`. Then `sshConfigBlock()` with `# managed by the Jam VS Code extension (aethons.jam)\n` inserted after line 1 equals the fixture byte for byte.
  - `TestExtensionSSHBlockPrunable`:
    - `sshConfigEntries(fixture)` is one entry with that studio and the ProxyCommand;
    - `pruneTarget` parses it with `adminURLFlag == "https://jam.example.com"`, `participant`;
    - `removeSSHConfigBlock` leaves `""`;
    - `spliceSSHConfigBlock` replaces it whole.
- [x] **Step 2:** `go test ./cmd/jam/ -run 'AppToken|ExtensionSSHBlock'` → FAIL.
- [x] **Step 3:** Implement. Write the fixture from the Go output once, then check it by eye against the Constraints.
- [x] **Step 4:** `go test ./cmd/jam/` → PASS.
- [x] **Step 5: Commit** `feat(jam): ssh-proxy uses the VS Code extension's app token file`.

### Task 2: Session channel and "Open in VS Code"

**Files:**
- Modify:
  - `internal/jam/userui/apitypes.go`: `SessionHead.Channel string \`json:"channel"\``, the session's home channel (`/app/user/c/<channel>`);
  - `internal/jam/userui/api_sessions.go` (`apiSessionHead` passes `home`) and `sessionHead`;
  - `web/app/src/api/types.ts` (regenerated);
  - `web/app/src/session/SessionHeader.tsx`.
- Create: `web/app/src/session/vscode.ts`.
- Test: `internal/jam/userui/api_sessions_test.go`, `web/app/src/session/SessionView.test.tsx`.

**Interfaces:**
- Produces:
  - `GET /app/user/api/sessions/{id}` → `…, "channel": "<chn id>"`;
  - `export const vscodeLink = (id: string) => \`vscode://aethons.jam/session/${encodeURIComponent(id)}\``.
  - The button is `<Button component="a" href={vscodeLink(h.id)} size="compact-xs" variant="default" title="Open this session's studio in VS Code (needs the Jam extension)">Open in VS Code</Button>`, placed right after Copy SSH and rendered only when `h.ssh` is set.

- [x] **Step 1: Write the failing tests:**
  - `TestAPISessionHeadChannel`: `channel` equals the session's home channel id.
  - Vitest:
    - `Open in VS Code links to the extension when SSH is allowed`: `getByRole("link", { name: "Open in VS Code" })` has `href` `vscode://aethons.jam/session/ses_1`.
    - The existing hidden-actions test (`ssh: ""`) also expects `queryByRole("link", { name: "Open in VS Code" })` to be null.
- [x] **Step 2:** `go test ./internal/jam/userui/ -run SessionHead && (cd web/app && npx vitest run src/session)` → FAIL.
- [x] **Step 3:** Implement. Run `go generate ./internal/jam/userui/`.
- [x] **Step 4:** `go test ./internal/jam/userui/ && just app-check` → PASS.
- [x] **Step 5: Commit** `feat(app): Open in VS Code in the session header; session head carries its channel`.

### Task 3: Extension skeleton, settings, build, CI and release

**Files:**
- Create in `web/vscode/`:
  - `package.json` and `package-lock.json` (`npm install` against the registry);
  - `tsconfig.json`: strict, `module` `node16`, includes `src`, `test` and the type-only `../app/src/api/types.ts`;
  - `esbuild.mjs`: `src/extension.ts` → `dist/extension.js`, cjs, node, `external: ["vscode"]`, minify;
  - `vitest.config.ts`: alias `vscode` → `test/vscode.ts`;
  - `test/vscode.ts`, the mock: `window`, `commands`, `env`, `workspace.getConfiguration`, `Uri.parse`, `extensions.getExtension`, `ConfigurationTarget`, `ProgressLocation`, each a `vi.fn` with recorded calls;
  - `src/extension.ts`, `src/config.ts`;
  - `test/config.test.ts`, `test/manifest.test.ts`, `test/extension.test.ts`;
  - `README.md`: three lines and a link to `docs/usage/jam/vscode.md`, the extension's details page in VS Code;
  - `.vscodeignore`: ship only `dist/`, `package.json`, `README.md`.
- Create: `scripts/vscode.sh`.
- Modify:
  - `.gitignore` (`/web/vscode/node_modules/`, `/web/vscode/dist/`);
  - `justfile`: `vscode` runs `./scripts/vscode.sh`; `vscode-check` runs `cd web/vscode && npm ci … && npm run check`;
  - `.github/workflows/gate.yml`, `.github/workflows/release.yml`.

**Interfaces:**
- Produces:
  - `package.json`:
    - `main: "./dist/extension.js"`, `activationEvents: ["onUri"]`, `extensionKind: ["ui"]`, `capabilities.untrustedWorkspaces.supported: true`;
    - `repository` pointing at the cove repo;
    - `license: "UNLICENSED"`, `private: true`, `version: "0.0.0"`;
    - commands `jam.signIn` "Jam: Sign In", `jam.signOut` "Jam: Sign Out", `jam.openSession` "Jam: Open Session…", `jam.forgetHostKey` "Jam: Forget Host Key";
    - settings `jam.url` and `jam.cliPath`, as in the Constraints;
    - scripts `build`, `typecheck` (`tsc --noEmit`), `test` (`vitest run`), `check` (typecheck && test), `package`.
  - `src/config.ts`:
    ```ts
    export function normalizeJamUrl(raw: string): string | Error // origin, no trailing slash; "" → Error("Set jam.url to your Jam's address")
    export function settings(get: (key: string) => string | undefined): { url: string | Error; cliPath: string }
    ```
  - `src/extension.ts`: `export function activate(ctx: vscode.ExtensionContext): void` registers the four commands and one `registerUriHandler`, wired to later tasks' modules. Until those land, each command shows "Not yet available". `export function deactivate(): void`.
  - `scripts/vscode.sh`, shellcheck-clean, runs `npm ci`, `npm run check`, `node esbuild.mjs`, then `npx vsce package --no-dependencies --skip-license --no-update-package-json "$ver" -o "$out"`:
    - `ver` is `0.${VERSION%%-*}.0` when `VERSION` (`<N>-<MMDD>`) is set, else `0.0.0`, since vsce needs SemVer;
    - `out` is `${VSIX_OUT:-web/vscode/dist/jam.vsix}`, made absolute before `cd`.
  - gate.yml:
    - `setup-node`'s `cache-dependency-path` lists both lockfiles;
    - a step `vscode extension: ./scripts/vscode.sh` after "app checks";
    - `actions/upload-artifact` (current major), name `jam-vscode`, path `web/vscode/dist/jam.vsix`.
  - release.yml:
    - the `go` paths filter adds `web/vscode/**` and `scripts/vscode.sh`;
    - `binaries`' setup-node caches both lockfiles;
    - after "Check release archives", the job runs `VSIX_OUT="dist/jam-vscode_${VERSION}.vsix" ./scripts/vscode.sh`;
    - "Publish release" adds `dist/jam-vscode_*.vsix`. On PRs it builds and doesn't publish.

- [ ] **Step 1: Write the failing tests:**
  - `normalizeJamUrl`, a table:
    - accepted: `https://jam.example.com/` → `https://jam.example.com`; `http://localhost:8081` → itself; `http://127.0.0.1:8081`;
    - errors: `http://jam.example.com`, `ftp://x`, `jam.example.com`, `""`.
  - `manifest.test.ts` (Review Focus 1):
    - `jam.url` and `jam.cliPath` have `scope: "application"`;
    - `extensionKind` is `["ui"]`, `publisher` is `aethons`, `name` is `jam`, `engines.vscode` is `^1.105.0`;
    - every `contributes.commands` id is registered by `activate`.
  - `extension.test.ts`: `activate registers four commands and a uri handler`, on the mock.
- [ ] **Step 2:** `cd web/vscode && npx vitest run` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just vscode` → PASS: `web/vscode/dist/jam.vsix` exists, and `unzip -l` lists `extension/dist/extension.js` and `extension/package.json` and no `node_modules`, `src` or `test`. `just lint` passes.
- [ ] **Step 5: Commit** `feat(vscode): extension skeleton, settings, .vsix in CI and releases`.

### Task 4: Sign In / Sign Out, the API client, the token file

**Files:**
- Create in `web/vscode/src/`:
  - `pkce.ts`, `auth.ts`, `api.ts`, `tokenfile.ts`, `paths.ts`, `uri.ts`;
  - tests in `web/vscode/test/`: `pkce.test.ts`, `auth.test.ts`, `api.test.ts`, `tokenfile.test.ts`, `uri.test.ts`.
- Modify: `src/extension.ts` wires Sign In, Sign Out and the `/auth` route.

**Interfaces:**
- Consumes: Task 3's `settings`, and slice 1's routes (Constraints).
- Produces:
  ```ts
  // paths.ts — env/home injected
  export function configDir(env: NodeJS.ProcessEnv, home: string): string
  export function sshDir(cfg: string): string; export function keyPath(cfg: string): string
  export function certPath(cfg: string, studio: string): string; export function appTokenPath(cfg: string): string // <cfg>/default-app-token.json
  export function sshConfigFile(home: string): string; export function knownHostsFile(home: string): string
  // pkce.ts
  export function challengeFor(verifier: string): string
  export function newPkce(): { state: string; verifier: string; challenge: string }
  export function consentUrl(base: string, p: { redirect: string; state: string; challenge: string; client: string }): string
  export function redirectFor(uriScheme: string): string | Error // vscode | vscode-insiders → "<s>://aethons.jam/auth"
  // tokenfile.ts
  export async function writeAppTokenFile(path: string, f: { token: string; jamUrl: string }): Promise<void>
  export async function removeAppTokenFile(path: string): Promise<void> // missing is fine
  // auth.ts
  export type Session = { url: string; token: string; user: string; expiresAt: string }
  export interface SecretsLike { get(k: string): Thenable<string | undefined>; store(k: string, v: string): Thenable<void>; delete(k: string): Thenable<void> }
  export class Auth {
    constructor(d: { secrets: SecretsLike; openExternal: (url: string) => Thenable<boolean>; fetch: typeof fetch;
                     uriScheme: string; hostname: string; tokenFile: string; setTimeout?: typeof setTimeout })
    session(url: string): Promise<Session | undefined>     // undefined when stored url ≠ url
    signIn(url: string): Promise<Session>                  // opens consent, awaits callback, exchanges, stores, writes token file
    handleCallback(q: URLSearchParams): boolean            // true when it matched the pending state
    signOut(): Promise<void>                               // deletes secret + token file
    readonly onChange: (cb: (s: Session | undefined) => void) => () => void
  }
  // api.ts
  export class SignedOutError extends Error {}
  export class ApiError extends Error { status: number; code: string }
  export class JamApi {
    constructor(base: string, token: string, fetch: typeof fetch)
    sessionHead(id: string): Promise<SessionHead>           // GET /app/user/api/sessions/{id}
    rail(): Promise<RailResponse>                           // GET /app/user/api/rail
    conversation(c: string): Promise<Conversation>          // GET /app/user/api/conversations/{c}
    issueCert(studio: string, publicKey: string): Promise<{ certificate: string; expiresAt: string }> // POST /me/studios/{id}/ssh/cert {public_key}
  }
  // uri.ts
  export type Route = { kind: "auth"; query: URLSearchParams } | { kind: "session"; id: string } | { kind: "bad"; reason: string }
  export function route(path: string, query: string): Route
  ```
  - `SessionHead`, `RailResponse` and `Conversation` are type-only imports from `web/app/src/api/types.ts`.
  - `extension.ts` wires the pieces:
    - a `signedOut()` helper calls `auth.signOut()` and shows "Your Jam sign-in expired or was revoked." with a [Sign In] action;
    - every `SignedOutError` goes to it;
    - Sign In shows a cancellable `withProgress` notification "Signing in to Jam in your browser…";
    - success shows "Signed in to Jam as <user>".

- [ ] **Step 1: Write the failing tests:**
  - pkce:
    - `challengeFor` matches the RFC 7636 vector: `dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk` → `E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM`.
    - `newPkce` gives a 43-char `[A-Za-z0-9_-]` verifier, and two calls give different states.
    - `consentUrl` gives exactly `https://jam.example.com/app/auth/app-token?redirect=vscode%3A%2F%2Faethons.jam%2Fauth&state=S&code_challenge=C&code_challenge_method=S256&client=VS+Code+on+host`.
    - `redirectFor`: `vscode`, `vscode-insiders`, and `code-oss` → Error.
  - auth, with a fake fetch, secrets and fake timers:
    - `signIn opens the consent URL and completes on the matching callback`. The exchange POST body is `{code:"c1", code_verifier, redirect:"vscode://aethons.jam/auth"}`. The secret holds the session, and the token file holds `{token, jam_url}`.
    - `callback with another state is ignored` (returns false; still pending).
    - `error=access_denied rejects "Sign-in was cancelled"`.
    - `times out after 5 minutes` → "Sign-in timed out".
    - `exchange 400 rejects "Jam refused the sign-in code; try again"`.
    - `a second signIn cancels the first`.
    - `session for a different jam.url is undefined` (Review Focus 2).
    - `signOut deletes the secret and the token file`.
    - `the token is never passed to openExternal`.
  - api:
    - `sends bearer, Accept json and redirect "error"`;
    - `401 throws SignedOutError` (Review Focus 5);
    - `404 ApiError carries the server's message`, for a JSON body and for a text body;
    - `issueCert posts {public_key} to /me/studios/ses_…/ssh/cert`;
    - path segments are `encodeURIComponent`ed.
  - tokenfile: `writes 0600 in a 0700 dir`, `replaces via rename (no partial file visible)`, `remove of a missing file resolves`.
  - uri: `/auth` → auth; `/session/<valid id>` → session; unknown path → bad.
  - extension: a `SignedOutError` from a command clears the secret and the token file, and shows [Sign In] (Review Focus 5).
- [ ] **Step 2:** `cd web/vscode && npx vitest run` → FAIL.
- [ ] **Step 3:** Implement. Use `node:crypto` (`randomBytes`, `createHash`) and the injected `fetch`.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): Jam sign-in with an app token (PKCE), API client, token file`.

### Task 5: The ~/.ssh/config block

**Files:**
- Create: `web/vscode/src/ssh/config.ts`, `web/vscode/src/ssh/quote.ts`, `web/vscode/test/sshconfig.test.ts`.

**Interfaces:**
- Consumes: Task 1's fixture, and the `ssh.go` functions named below (ported, not called).
- Produces:
  ```ts
  export const EXT_MARK = "# managed by the Jam VS Code extension (aethons.jam)"
  export type BlockOpts = { studio: string; jamPath: string; jamUrl: string; keyPath: string; certPath: string }
  export function renderBlock(o: BlockOpts): string      // = ssh.go sshConfigBlock() + EXT_MARK as line 2
  export type Owner = "none" | "extension" | "other"
  export function blockOwner(config: string, studio: string): Owner // "other": a marked block without EXT_MARK, or any `Host` line naming jam-<studio> outside a marked block
  export function spliceBlock(config: string, studio: string, block: string): string // ssh.go spliceSSHConfigBlock; throws on 2 begins / begin without end
  export function stripLegacyBlock(config: string, studio: string): string           // ssh.go stripLegacySSHBlocks for one studio ("# >>> at-jam ")
  export function listJamStudios(config: string): string[]                          // studios with a complete marked block
  export async function editSshConfig(path: string, edit: (s: string) => string): Promise<void> // ssh.go editSSHConfig: symlink target, mode (new: 0600, dir 0700), CRLF kept, atomic
  // quote.ts — ssh.go's helpers
  export function shellQuote(s: string): string
  export function proxyBinary(absPath: string): string   // quoted when outside [A-Za-z0-9/._+-]
  export function sshConfigEscape(s: string): string      // % → %%
  export function sshConfigPath(p: string): string        // escaped; "…" when it has space/tab
  ```
  - The ProxyCommand is `sshConfigEscape("<proxyBinary(jamPath)> ssh-proxy --admin-url <shellQuote(jamUrl)> --participant <studio>")`.

- [ ] **Step 1: Write the failing tests:**
  - `renderBlock equals the Go golden fixture` (reads `test/fixtures/ssh-block.txt`).
  - `paths with spaces and % are quoted and escaped as ssh.go does`: `/Users/a b/.config/jam/ssh/id_ed25519` → `"/Users/a b/…"`, and `100%` → `100%%`. The jam path `/opt/my jam/jam` is single-quoted.
  - `blockOwner`, a table:
    - none;
    - an extension block;
    - a `jam ssh-config` block (no mark) → other;
    - a hand-written `Host jam-<studio>` → other;
    - `Host foo jam-<studio>` → other;
    - another studio's block → none.
  - `spliceBlock`:
    - into an empty file;
    - after leading comments and before `Host *`;
    - before another studio's block;
    - in place of its own block;
    - 2 begins → throws;
    - begin without end → throws.
  - `stripLegacyBlock removes the studio's at-jam block and the blank line after it`.
  - `editSshConfig` on a temp HOME:
    - keeps CRLF;
    - keeps mode 0644;
    - writes through a symlink to its target;
    - creates `~/.ssh` 0700 and the file 0600;
    - a throwing edit leaves the file untouched (Review Focus 3).
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/sshconfig.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Same command → PASS.
- [ ] **Step 5: Commit** `feat(vscode): write the studio's ssh_config block, byte-compatible with jam ssh-config`.

### Task 6: Key, cert and the jam CLI

**Decision, generating the key:** the extension shells out to `ssh-keygen -t ed25519 -N "" -C jam -q -f <tmpdir>/id_ed25519`. It does not encode the key itself:
- `ssh-keygen` ships with every OpenSSH client, and Remote-SSH already needs one;
- `node:crypto` can make ed25519 keys but can't write OpenSSH's private-key format;
- the same tool derives the public key (`ssh-keygen -y -f <key>`) from a key `jam` generated, since Go's `createSSHKey` writes no `.pub`.

**Files:**
- Create: `web/vscode/src/ssh/keys.ts`, `cert.ts`, `cli.ts`, `setup.ts`, and the tests `keys.test.ts`, `cert.test.ts`, `cli.test.ts`, `setup.test.ts`.

**Interfaces:**
- Consumes: Task 4's `JamApi.issueCert`, `paths.ts` and `SignedOutError`; Task 5's block functions.
- Produces:
  ```ts
  export type ExecFile = (file: string, args: string[]) => Promise<{ stdout: string; stderr: string }>
  // keys.ts
  export async function ensureKey(keyPath: string, sshKeygen: string, run: ExecFile): Promise<string> // "ssh-ed25519 AAAA… jam" line
  // cert.ts
  export const CERT_RENEW_BEFORE_MS = 3_600_000
  export function certValidBefore(certLine: string, pubLine: string): Date | null // ssh-ed25519-cert-v01@openssh.com for this key, else null
  export async function ensureCert(api: JamApi, studio: string, pubLine: string, certPath: string, now: Date): Promise<void>
  export function certErrorMessage(e: ApiError, studio: string): string
  // cli.ts
  export async function findJam(cliPath: string, envPath: string): Promise<string | Error>
  export async function findSshKeygen(envPath: string): Promise<string | Error>
  // setup.ts
  export type SshResult = { alias: string; owner: "extension" | "other" }
  export async function ensureSsh(d: { api: JamApi; jamUrl: string; cliPath: string; env: NodeJS.ProcessEnv; home: string;
                                       platform: NodeJS.Platform; run: ExecFile; now: () => Date }, studio: string): Promise<SshResult>
  ```
  - `ensureKey` follows `ssh.go:ensureSSHKey`:
    - when the key is absent, it generates in a `mkdtemp` under `sshDir` and `link`s the key into place, so `EEXIST` means another run won;
    - it tightens a key with group or other bits to 0600;
    - output of `-y` not starting `ssh-ed25519 ` → Error "`<path>` is not an ed25519 key (remove it to generate a new one)".
  - `certValidBefore` decodes the cert blob: type string, nonce, the 32-byte ed25519 key (must equal the pub's), serial u64, type u32, key id, principals, valid_after u64, valid_before u64.
  - `ensureCert` renews when the cert is absent or not for this key, or when `validBefore - now < CERT_RENEW_BEFORE_MS`. It checks `certValidBefore` on the returned cert ("Jam returned a certificate that is not for this key"). It writes 0644 atomically, and a `SignedOutError` propagates.
  - `certErrorMessage` mirrors `sshErrMessage`'s 403/404/409/429/503/504 lines for the participant plane.
  - `findJam`: a set `cliPath` must be absolute and executable. Otherwise it searches `PATH` with `fs.access(X_OK)`, no shell. Not found → Error "The jam CLI was not found. Install it (see the Jam docs) or set jam.cliPath."
  - `ensureSsh`:
    - throws "Jam's SSH needs the jam CLI, which runs on macOS and Linux" on `win32`;
    - when `blockOwner` is `"other"`, returns `{owner:"other"}` and writes nothing, since the CLI's `ssh-proxy` renews its cert;
    - otherwise: `ensureKey` → `ensureCert` → `editSshConfig(stripLegacyBlock → spliceBlock(renderBlock))`, and returns `{owner:"extension"}`.

- [ ] **Step 1: Write the failing tests:**
  - keys, with a fake `ExecFile` that writes a fixture key:
    - `generates with ssh-keygen when absent`: argv exactly `["-t","ed25519","-N","","-C","jam","-q","-f",<tmp>/id_ed25519]`, and the final key is 0600.
    - `existing key is not regenerated`.
    - `0644 key is tightened to 0600`.
    - `non-ed25519 key errors`.
  - cert, with fixture certs generated once by `ssh-keygen -s` and committed under `test/fixtures/`:
    - `certValidBefore reads valid_before`;
    - `cert for another key is null`;
    - `renews with 59 min left, keeps with 61 min`;
    - `returned cert for another key errors`;
    - `401 propagates SignedOutError` (Review Focus 5);
    - `409 message "studio ses_… is not running"`.
  - cli:
    - `cliPath relative is an error`;
    - `PATH search finds an executable, skips a non-executable`;
    - `not found message`.
  - setup, on a temp HOME and XDG dir with fakes:
    - `writes key, cert and block, then the block equals renderBlock`;
    - `a jam ssh-config block is left byte for byte and no cert is fetched` (Review Focus 3);
    - `win32 errors before touching files`;
    - `a second run with a fresh cert makes no API call`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/keys.test.ts test/cert.test.ts test/cli.test.ts test/setup.test.ts` → FAIL.
- [ ] **Step 3:** Implement. `run` is `util.promisify(child_process.execFile)`.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): ssh key, Jam cert renewal and jam CLI lookup`.

### Task 7: Getting in (link, Open Session…, Forget Host Key)

**Decision on `remote.SSH.localServerDownload`:** it is a global Remote-SSH setting, and not per host. On the first open, when its global value isn't `always`, the extension asks once: "Jam studios can't download VS Code Server themselves. Set Remote-SSH's localServerDownload to 'always'?" The answers are [Set it] and [Not now]. It remembers the answer in `globalState` key `jam.askedLocalServerDownload`, then opens either way. Remote-SSH's default `auto` falls back to a local download after the studio's attempt fails; that is slower, but it works.

**Files:**
- Create: `web/vscode/src/open.ts`, `web/vscode/src/hostkey.ts`, and the tests `open.test.ts`, `hostkey.test.ts`.
- Modify: `src/extension.ts`, which wires the `/session` route, `jam.openSession` and `jam.forgetHostKey`.

**Interfaces:**
- Consumes: Tasks 4 and 6.
- Produces:
  ```ts
  export interface Ui {
    error(msg: string, ...actions: string[]): Promise<string | undefined>
    info(msg: string, ...actions: string[]): Promise<string | undefined>
    pick<T extends { label: string }>(items: T[], placeholder: string): Promise<T | undefined>
    openExternal(url: string): Promise<void>
    openFolder(uri: string): Promise<void>                // executeCommand("vscode.openFolder", Uri.parse(uri), { forceNewWindow: true })
    hasRemoteSsh(): boolean                               // extensions.getExtension("ms-vscode-remote.remote-ssh")
    installRemoteSsh(): Promise<void>                     // workbench.extensions.installExtension
    getGlobal(section: string, key: string): unknown; setGlobal(section: string, key: string, v: unknown): Promise<void>
    remembered(key: string): boolean; remember(key: string): Promise<void>
    remoteAuthority(): string | undefined                 // vscode.env.remoteAuthority
  }
  export const remoteUri = (studio: string) => `vscode-remote://ssh-remote+jam-${studio}/home/agent/workspace`
  export function webUrl(base: string, channel: string): string // /app/user/c/<enc channel> | /app/user/
  export type OpenDeps = { ui: Ui; signedIn: () => Promise<{ api: JamApi; url: string }>; ssh: (api: JamApi, studio: string) => Promise<SshResult> }
  export async function openSession(d: OpenDeps, id: string): Promise<void>
  export async function pickSession(d: OpenDeps): Promise<void>
  export function studioFromAuthority(a: string | undefined): string | undefined // "ssh-remote+jam-<ses id>" → id
  export async function forgetHostKey(alias: string, knownHosts: string, sshKeygen: string, run: ExecFile): Promise<void> // execFile ssh-keygen -R alias -f knownHosts; missing file → no-op
  ```
  - `signedIn` returns the stored session, or runs `Auth.signIn` and then continues.
  - **`openSession` order:**
    1. Validate the id against `SESSION_ID_RE`; a bad one shows "That isn't a Jam session link."
    2. Call `signedIn`.
    3. Fetch `api.sessionHead(id)`.
       - A 404 shows "No such session, or you can't see it." with [Open in the web app] → `<url>/app/user/`.
       - An empty `ssh` shows "You can't SSH into <label>'s studio (ask an operator to share it)." with [Open in the web app] → `webUrl(url, head.channel)`.
    4. Without Remote-SSH, show "Opening a studio needs Microsoft's Remote - SSH extension." with [Install].
    5. Run `ssh(api, id)`; an error shows its message.
    6. Ask about `localServerDownload` (the decision above).
    7. `ui.openFolder(remoteUri(id))`.
  - **`pickSession`** builds a quick pick from the `api.rail()` rows:
    - only rows with `kind === "studio"` in buckets `needs-you` and `active`, deduped by id;
    - each item's label is `row.label`, description `row.project`, detail `row.mission`; a needs-you row is prefixed `$(bell) `.
    - On a pick it reads `api.conversation(row.id).session`: empty shows "That conversation has no session you can open", else `openSession`.
  - **`jam.forgetHostKey`** uses `studioFromAuthority(ui.remoteAuthority())` when it is set, else a pick from `listJamStudios(~/.ssh/config)`. It then confirms "Forget the pinned host key of jam-<studio>? Do this after the studio was upgraded and Remote-SSH reports the host identification changed." with [Forget]. Remote-SSH's failure isn't visible to an extension, so the command is offered here, in the palette, and in the docs' troubleshooting.

- [ ] **Step 1: Write the failing tests** (fake `Ui`, api and ssh):
  - `opens the remote folder after sign-in and ssh`: `openFolder` is called once with `vscode-remote://ssh-remote+jam-ses_0abc…/home/agent/workspace`.
  - `not signed in runs sign-in first, then opens`.
  - `bad ids are refused before any call` (Review Focus 4), a table: `ses_x`, `ses_0abcdefghjkmnpqrstvwxyz01%0AHost`, `../../x`, `ses_0ABC…`, `""`. Neither `signedIn` nor `ssh` is called.
  - `empty ssh shows the reason and Open in the web app → https://jam.example.com/app/user/c/chn_1`.
  - `404 offers the web app root`.
  - `missing Remote-SSH offers Install and does not open`.
  - `ssh error is shown and does not open`.
  - `asks about localServerDownload once; Set it writes "always" globally`. On a second open it doesn't ask, and when the value is already `always` it never asks.
  - `pickSession lists live studio rows only and opens the picked row's session`: a named row, a dm row and a history row are excluded.
  - `pickSession with no session on the conversation shows the message`.
  - `studioFromAuthority`: `ssh-remote+jam-<id>` → id; `ssh-remote+other` → undefined; undefined → undefined.
  - `forgetHostKey runs ssh-keygen -R jam-<studio> -f <known_hosts> via execFile`; `missing known_hosts is a no-op`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/open.test.ts test/hostkey.test.ts` → FAIL.
- [ ] **Step 3:** Implement and wire.
- [ ] **Step 4:** `just vscode` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): open a session's studio from a link or Jam: Open Session…`.

### Task 8: Docs and the manual end-to-end checklist

**Files:**
- Create:
  - `docs/usage/jam/vscode.md` (leaf; frontmatter `summary`/`read_when`/`owns`/`prereqs`/`tier: leaf`/`updated: 2026-10-29`). It **owns**:
    - installing the `.vsix` (CI artifact `jam-vscode` or the release asset `jam-vscode_<version>.vsix`; *Extensions: Install from VSIX…*);
    - what it needs: the `jam` CLI from the same release or newer, Remote - SSH, macOS or Linux;
    - the settings `jam.url` and `jam.cliPath`, and why they are application-scoped;
    - Sign In and Sign Out (browser consent; the token in SecretStorage and `~/.config/jam/default-app-token.json`; revoking under Connected apps, linking to `app-tokens.md`);
    - opening a session (the link, the header button, *Jam: Open Session…*);
    - what it writes (the block and its marker line, the key, the cert; it leaves a `jam ssh-config` block alone);
    - `remote.SSH.localServerDownload`;
    - troubleshooting: host identification changed → *Jam: Forget Host Key*; "participant sign-in missing" from an older `jam`; jam not found; "can't SSH into"; signed out after a revoke.
  - `docs/usage/jam/vscode-checklist.md` (leaf; `read_when`: you changed `web/vscode` or are cutting a release with it, and must build it or verify it by hand, since VS Code can't run in CI). It **owns** the extension's build: `just vscode` / `just vscode-check`, `scripts/vscode.sh`, the `0.<N>.0` version, the CI artifact and the release asset, the unit-test layout (pure modules plus the `vscode` mock), and the Go/TS golden `test/fixtures/ssh-block.txt`. Its checklist covers:
    - **To verify first (spec):**
      - with `localServerDownload: always`, the studio makes no download attempt (squid log in the studio shows no `update.code.visualstudio.com`);
      - whether VS Code asks for confirmation when a `vscode://aethons.jam/…` link opens a remote;
      - `vscode.openFolder` with `forceNewWindow` focuses an existing window on that folder rather than opening a second.
    - **The steps:**
      - install the `.vsix`;
      - set `jam.url`, Sign In (consent page names "VS Code on <host>"), and check Connected apps lists it;
      - Open in VS Code from the session header (a window opens on `/home/agent/workspace`, and the terminal works);
      - the same link again focuses that window;
      - `open vscode://aethons.jam/session/<id>` from a terminal;
      - Open Session… lists live sessions;
      - a member without SSH sees the error and the web app link;
      - after an existing `jam ssh-config --write`, the block is unchanged;
      - `jam ssh-config --prune --dry-run` handles an extension block;
      - revoke in Connected apps → the next open says signed out, and `ssh-proxy` fails with the VS Code message;
      - Sign Out deletes the token file;
      - upgrade the studio → host-key error → Forget Host Key → reconnect works;
      - VS Code Insiders sign-in.
- Modify:
  - `docs/usage/jam/INDEX.md`: rows for both new docs.
  - `docs/usage/jam/collaboration.md`:
    - "Collaborator" gets a line: in VS Code, use the Jam extension (`vscode.md`) instead of `login --participant` + `ssh-config`;
    - the plane-choice paragraph now reads: the VS Code app token file when it names the same Jam, else the participant login, else the operator token;
    - the troubleshooting host-key row mentions *Jam: Forget Host Key*.
  - `docs/usage/jam/session-view.md`: the header's **Open in VS Code** bullet. It is shown when Copy SSH is, and links `vscode://aethons.jam/session/<id>` (→ `vscode.md`). The session head's `channel` field goes there too.
  - `docs/web-app.md`, Layout: one line saying `web/vscode/` is the VS Code extension, a separate package (→ `vscode.md`), and that it type-imports `src/api/types.ts`.
  - The spec's status line: "slice 2 (extension, sign-in, SSH, getting in) built".
- [ ] **Step 1:** Write the docs. Each fact goes in the one doc named above; the others link to it.
- [ ] **Step 2:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new findings beyond the plan file's own.
- [ ] **Step 3:** `just test && just app-check && just vscode && just lint` → PASS.
- [ ] **Step 4: Commit** `docs: the Jam VS Code extension`.
