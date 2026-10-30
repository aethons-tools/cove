# VS Code hub, slice 4: the Dock launcher — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `jam vscode install` also creates a "Jam Hub" launcher with the J logo:
- a small `.app` in `~/Applications` on macOS, to keep in the Dock;
- a `.desktop` entry on Linux.

It opens `<scheme>://aethons.jam/hub?jam=<url>`, which starts VS Code (or VSCodium) and opens or focuses that Jam's hub.

**Architecture:**
- **The CLI** writes the launcher files with pure builders: `Info.plist`, the launcher script, the `.desktop` text, and `.icns` assembly from the embedded 512 px PNG. The builders are tested hermetically; a thin writer puts the files under `$HOME`.
- **The extension's `/hub` route** accepts `?jam=`. In this slice it warns when that Jam isn't the configured `jam.url`, and still opens the hub. Slice 5 makes it pick that Jam's hub.

**Tech Stack:**
- Go (`cmd/jam`, `internal/jam/uiassets`);
- TypeScript and vitest (`web/vscode`).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md`, *Getting in* (the Dock launcher). Slices 1–3 are on this branch.

## Global Constraints

- **Name:** `Jam Hub (<host>)`, where `<host>` is the Jam URL's host, with its port if any; for example `Jam Hub (jam.example.com)`. One launcher per Jam and editor.
  - A VSCodium or Insiders launcher appends ` VSCodium` or ` Insiders`: `Jam Hub (jam.example.com) VSCodium`.
- **Link:** `<scheme>://aethons.jam/hub?jam=<url.QueryEscape(jamURL)>`, where `scheme` is `vscode`, `vscode-insiders` or `vscodium`.
- **The editor is inferred from `--code`'s base name:**
  - `codium` → `vscodium`;
  - `code-insiders` → `vscode-insiders`;
  - anything else → `vscode`.
- **macOS:** `~/Applications/<name>.app/Contents/`, holding:
  - `Info.plist`, with:
    - `CFBundleName` = `<name>`;
    - `CFBundleIdentifier` = `tools.aethons.jam.hub.<host with every non-[A-Za-z0-9.-] byte as ->` (plus `.vscodium` or `.insiders`);
    - `CFBundleExecutable` = `jam-hub`;
    - `CFBundleIconFile` = `jam`;
    - `CFBundlePackageType` = `APPL`;
    - `LSMinimumSystemVersion` = `10.13`;
    - every value XML-escaped.
  - `MacOS/jam-hub`, mode 0755: `#!/bin/sh\nexec /usr/bin/open '<link>'\n`, with the link single-quote-escaped.
  - `Resources/jam.icns`: `icns`, a big-endian u32 total length, then one `ic09` entry (the 512 px PNG `icon-512.png` from `uiassets`), its length including its 8-byte header.
- **Linux:**
  - `~/.local/share/applications/jam-hub-<slug>.desktop` (`<slug>` is the identifier suffix after `tools.aethons.jam.hub.`), with:
    - `[Desktop Entry]`;
    - `Type=Application`;
    - `Name=<name>`;
    - `Exec=xdg-open "<link>"`, with every `%` in the link doubled, since the desktop spec reserves `%`;
    - `Icon=<abs path to ~/.local/share/icons/hicolor/512x512/apps/jam-hub.png>`;
    - `Terminal=false`;
    - `Categories=Development;`.
  - The PNG is written there too.
- **Other OSes:** no launcher, and a note on stdout: "no launcher on <goos>: open <link> instead".
- **`--no-launcher`** skips it.
- **Rewriting:** a rerun replaces the launcher's files.
- **Failure:** a launcher failure is a warning on stderr. The extension install still succeeds (exit 0).
- **The extension's `/hub?jam=`:**
  - the `jam` value must normalise (`normalizeJamUrl`) to a URL;
  - if it differs from the configured `jam.url`, show a warning: "This Jam Hub launcher is for <jam>, but jam.url is <url>.";
  - either way, open the hub.
- **Run checks with:** `go test ./cmd/jam/ ./internal/jam/uiassets/...`; `cd web/vscode && npm run check`.

## Review Focus

1. **A Jam URL with odd characters** (a port, a path, `'`, `%`, `&`): the link is escaped once, the shell script stays one quoted word, the plist stays well-formed, and the `.desktop` `Exec` doubles `%`. *(Test in Task 1.)*
2. **Rerunning install** replaces the launcher, and no stale files are left inside the `.app`. *(Test in Task 2.)*
3. **No writable home or Applications directory:** the extension still installs, there's a warning, and the exit code is 0. *(Test in Task 2.)*
4. **A launcher for another Jam:** the extension warns, and the hub still opens. *(Test in Task 3.)*
5. **The `.icns` header lengths are exact** (a wrong length shows a blank icon). *(Test in Task 1.)*

---

### Task 1: launcher builders

**Files:**
- Modify: `internal/jam/uiassets/uiassets.go`: add `func Icon(name string) ([]byte, error)`, which reads `static/icons/<name>` from the embedded FS.
- Create: `cmd/jam/launcher.go`, holding pure functions:
  - `editorScheme(codeCLI string) string`;
  - `launcherName(jamURL, scheme string) (string, error)`;
  - `launcherSlug(jamURL, scheme string) (string, error)`;
  - `hubLink(scheme, jamURL string) string`;
  - `macInfoPlist(name, slug string) string`;
  - `macScript(link string) string`;
  - `icns(png512 []byte) []byte`;
  - `desktopEntry(name, link, iconPath string) string`.
- Test: `cmd/jam/launcher_test.go`, `internal/jam/uiassets/uiassets_test.go`.

**Interfaces:**
- Produces the functions above, which Task 2 uses.

- [ ] **Step 1: Write the failing tests:**
- `TestEditorScheme`: `/usr/bin/codium` → `vscodium`; `code-insiders` → `vscode-insiders`; `""`, `code` and `/Applications/…/bin/code` → `vscode`.
- `TestLauncherNameAndSlug`:
  - `https://jam.example.com` with `vscode` → `Jam Hub (jam.example.com)` and `jam.example.com`;
  - `https://h:8443/x` with `vscodium` → `Jam Hub (h:8443) VSCodium` and `h-8443.vscodium`;
  - with `vscode-insiders` → the ` Insiders` and `.insiders` suffixes;
  - a URL with no host → error.
- `TestHubLink`: `vscode`, `https://jam.example.com` → `vscode://aethons.jam/hub?jam=https%3A%2F%2Fjam.example.com`.
- `TestMacInfoPlist`: `xml.Unmarshal` of the output succeeds. It contains `<key>CFBundleExecutable</key><string>jam-hub</string>` and `<string>tools.aethons.jam.hub.h-8443.vscodium</string>`. A name with `&<` is escaped.
- `TestMacScript` *(Review Focus 1)*: for the link `vscode://aethons.jam/hub?jam=a'b%26c`, the script is exactly `"#!/bin/sh\nexec /usr/bin/open 'vscode://aethons.jam/hub?jam=a'\\''b%26c'\n"`.
- `TestICNS` *(Review Focus 5)*: for an `n`-byte PNG `p`, the output is `"icns"` + `be32(16+n)` + `"ic09"` + `be32(8+n)` + `p`, and the total length is `16+n`.
- `TestDesktopEntry` *(Review Focus 1)*: `Exec=xdg-open "vscode://aethons.jam/hub?jam=https%%3A%%2F%%2Fh"`, and it contains `Type=Application`, `Icon=/i/jam-hub.png`, `Terminal=false` and `Name=Jam Hub (h)`.
- `TestIcon` (uiassets): `Icon("icon-512.png")` starts with the PNG magic; `Icon("nope.png")` is an error.

- [ ] **Step 2: Run, and see them fail.** Run: `go test ./cmd/jam/ -run 'Launcher|EditorScheme|HubLink|MacInfoPlist|MacScript|ICNS|DesktopEntry' ./internal/jam/uiassets/ -run TestIcon`. Expected: FAIL to compile.
- [ ] **Step 3: Implement** `launcher.go` and `uiassets.Icon`.
- [ ] **Step 4: Run, and see them pass.** Run the same command, then `go vet ./cmd/jam/ ./internal/jam/uiassets/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Jam Hub launcher builders`.

### Task 2: `jam vscode install` writes the launcher

**Files:**
- Modify: `cmd/jam/launcher.go`:
  - add `func writeLauncher(home, goos, scheme, jamURL string, png []byte) (path string, err error)`;
  - on darwin, remove `<home>/Applications/<name>.app` and then write it fresh;
  - on linux, write the `.desktop` file and the PNG;
  - on anything else, return `("", errNoLauncher)`.
- Modify: `cmd/jam/vscode.go`:
  - `--no-launcher` flag;
  - after a successful install, unless `--no-launcher`: `writeLauncher(os.UserHomeDir(), vscodeGOOS, editorScheme(cli), t.adminURL, icon)`;
  - print `Added the Jam Hub launcher: <path>` on success, or `jam vscode install: no Jam Hub launcher: <err>` on stderr. `errNoLauncher` prints the "open <link> instead" note to stdout instead;
  - the exit code stays 0.
  - Update the usage line: `jam vscode install [--code PATH] [--no-launcher] [--participant|--operator]`.
- Test: `cmd/jam/vscode_test.go`.

**Interfaces:**
- Consumes Task 1's builders and `uiassets.Icon`.

- [ ] **Step 1: Write the failing tests:**
- `TestVSCodeInstallMacLauncher`, built on `TestVSCodeInstall`'s setup with `stubCode(t, "darwin", "codium")` and `--code codium`:
  - `<HOME>/Applications/Jam Hub (127.0.0.1:<port>) VSCodium.app/Contents/MacOS/jam-hub` exists with mode `0755`, and its script contains `vscodium://aethons.jam/hub?jam=`;
  - `Resources/jam.icns` starts with `icns`;
  - stdout has `Added the Jam Hub launcher`.
  - *(Review Focus 2)* Write a stray file inside the `.app`, rerun install, and the stray file is gone.
- `TestVSCodeInstallLinuxLauncher`: the `.desktop` file under `<HOME>/.local/share/applications/` holds `Exec=xdg-open "vscode://aethons.jam/hub?jam=http%%3A…"`, and the icon PNG exists.
- `TestVSCodeInstallNoLauncher`: with `--no-launcher`, no `Applications` dir is created.
- `TestVSCodeInstallLauncherFailureIsAWarning` *(Review Focus 3)*: `HOME` is a read-only dir (`chmod 0500`; skip when running as root). The exit code is 0, stderr has `no Jam Hub launcher`, and the extension install ran.
- `TestVSCodeInstallOtherOSNote`: goos `windows`; stdout has `open vscode://aethons.jam/hub?jam=`.
- `TestVSCodeUsage`: `install --no-launcher extra` exits 2.

- [ ] **Step 2: Run, and see them fail.** Run: `go test ./cmd/jam/ -run VSCode`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `go test ./cmd/jam/ && go vet ./cmd/jam/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): jam vscode install adds the Jam Hub launcher`.

### Task 3: the extension's `/hub?jam=`

**Files:**
- Modify: `web/vscode/src/uri.ts`: `{ kind: "hub"; jam: string }`, where `jam` is the `jam` query value (`""` when there isn't one).
- Modify: `web/vscode/src/extension.ts`: the `hub` route compares `normalizeJamUrl(r.jam)` (from `config.ts`) with `jamSettings().url`. If `r.jam` is set, valid and different, it shows the warning from Global Constraints. In every case it opens the hub.
- Tests: `test/uri.test.ts`, `test/extension.test.ts`.

**Interfaces:** none new.

- [ ] **Step 1: Write the failing tests:**
- `uri.test.ts`: `route("/hub", "jam=https%3A%2F%2Fj")` → `{ kind: "hub", jam: "https://j" }`, and `route("/hub", "")` → `{ kind: "hub", jam: "" }`.
- `extension.test.ts` *(Review Focus 4)*:
  - `"a launcher for another Jam warns and still opens the hub"`: with `jam.url` = JAM, `/hub?jam=https://other.example` → `showWarningMessage("This Jam Hub launcher is for https://other.example, but jam.url is https://jam.example.com.")`, and `openFolder` is called on the hub;
  - `"a launcher for this Jam opens the hub quietly"`: no warning.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/uri.test.ts test/extension.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): the hub link names its Jam`.

### Task 4: docs

- `docs/usage/jam/vscode-hub.md`: under *The hub*, add a **Launcher** bullet: what `jam vscode install` writes on macOS and Linux, its name, the link, `--no-launcher`, and how to keep it in the Dock or the app menu.
- `docs/usage/jam/vscode.md`, *Install and requirements*: one line saying install also adds the launcher (link to `vscode-hub.md`).
- `docs/usage/jam/vscode-checklist.md`:
  - on macOS, the `.app` opens VS Code on the hub, its icon is the J mark, and it can be kept in the Dock;
  - on Linux, the desktop entry shows in the app menu and opens the hub;
  - a VSCodium launcher opens VSCodium;
  - a launcher for another Jam warns.
- Wherever `jam vscode install`'s flags are documented (`grep -rn "vscode install" docs/usage`), add `--no-launcher`.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR`. Expected: no new errors.
- [ ] **Step 3: Commit** `docs(vscode): the Jam Hub launcher`.
