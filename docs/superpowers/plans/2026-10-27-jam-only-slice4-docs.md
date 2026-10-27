# Jam only, slice 4 — docs and agent context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every doc an operator, a repo developer or an in-studio agent reads describes Jam only — no `at-cove`, `.at-cove/`, `at-cove recreate`, `at-task`, `at-switchboard` or at-cove-era kit words — and the rename guard's slice-4 exemptions are gone.

**Architecture:** Docs-only, plus the tests that pin docs: the in-image seed (`images/jam-studio-base/image-files/home/agent/.init-agent-data`) becomes Jam-shaped (a short fallback `SANDBOX.md`, Jam-shaped board skills), the repo's own top docs drop the slice-2 interim wording, and `renamed_test.go` / `deleted_test.go` lose the exemptions that slice 4 owned. One fact one place: Jam sessions get the sandbox rules from the session-context Boilerplate (`internal/jam/sessionctx/boilerplate.go`); the seed only carries a fallback for a session without a bundle.

**Tech Stack:** Markdown, Go tests (`go test`), the docs audit script.

**Spec:** `docs/superpowers/specs/2026-10-26-jam-only-design.md` §4 (slice 4).

## Global Constraints

- `GOPROXY=https://proxy.golang.org,direct` for every `go` command.
- Checks before each commit that touches Go: `just test`; before the last commit also `just lint` (check the exit code), `go vet -tags integration ./...`, `bash install.test.sh`, and `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` (no new errors outside `docs/superpowers/`).
- Kept on purpose (do not rename): "cove" as the word for a studio's sandbox, `/admin/coves` routes, the Go module path, `docs/usage/jam/coves.md`, the `cove-ic` kit name, `docs/superpowers/` history, `renamed-from-*.md`.
- Docs follow the repo's progressive-disclosure rules: frontmatter `summary`/`read_when`/`owns`/`updated: 2026-10-27`; one fact one place, link rather than copy; every changed doc reachable from its INDEX.
- Files under `images/*/image-files/` are **instructions for an agent inside a studio**, not for this repo; write them to that reader.
- Commit trailer exactly: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never weaken the hardening; this slice changes no file under `internal/assemble/hardening/`.

## Review Focus

1. A studio whose `/agent-data` was seeded by an older image still has `reference/sandbox-kit-changes.md` and `reference/sandbox-hardening-limits.md` — the next boot must remove them (`reference` stays in `.refresh`, so the directory is replaced whole). Pinned in Task 1's seed test.
2. A kit that overrides `SANDBOX.md` and a session with a bundle: the two-line `CORE.md` guard must stay the file's first lines, word for word as `session-context.md` describes it. Pinned in Task 1's seed test.
3. An in-studio agent following a board skill must never be told to read `.at-cove/config.yml` or run `at-cove`/`at-task`; the rename and deletion guards pin this once Task 2 drops their exemptions for the seed.
4. Every link in a changed doc resolves (deleted reference files leave no dangling link in the seed, `session-context.md`, OVERVIEW or AGENTS). Pinned by the docs audit in Task 3 and a grep step in Task 1.
5. The Boilerplate's changing-the-kit text and the fallback `SANDBOX.md` say the same path (a kit draft, published, then a re-raise) without copying each other's detail.

---

### Task 1: The in-image sandbox fallback and the Boilerplate's kit path

**Files:**
- Modify: `images/jam-studio-base/image-files/home/agent/.init-agent-data/SANDBOX.md`
- Delete: `images/jam-studio-base/image-files/home/agent/.init-agent-data/reference/sandbox-kit-changes.md`
- Delete: `images/jam-studio-base/image-files/home/agent/.init-agent-data/reference/sandbox-hardening-limits.md`
- Modify: `internal/assemble/image_scripts_test.go:503-545` (`sandboxKitCLI`, `TestSeedAgentDataWithBaseImageSeed`)
- Modify: `internal/jam/sessionctx/boilerplate.go` (`changingTheKit` text), plus any goldens that embed it (`internal/jam/testdata/setup/*.golden`, `internal/jam/sessionctx/boilerplate_test.go`) — regenerate with the package's existing update flag if it has one, else edit by hand
- Modify: `docs/usage/jam/session-context.md` §"The sandbox rules: context over `SANDBOX.md`"
- Modify: `AGENTS.md` only if it names a deleted file (it names `SANDBOX.md`, which stays)

**Interfaces:**
- Produces: the fallback `SANDBOX.md` with the unchanged two-line guard, and the marker phrase `jam kit draft` that Task 1's test asserts.

- [ ] **Step 1: Write the failing test.** In `image_scripts_test.go` delete the `sandboxKitCLI` const and its comment. In `TestSeedAgentDataWithBaseImageSeed`, replace both `sandboxKitCLI+" recreate"` assertions with `"jam kit draft"`, change the reference-set path in the loop to `"reference/progressive-disclosure.md"`, and add, before the first `seedAgentData` call, a stale file planted in the volume to prove old reference files go:

```go
	if err := os.MkdirAll(filepath.Join(dest, "reference"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "reference", "sandbox-kit-changes.md"), []byte("old"), 0o644); err != nil { // seeded by an older image
		t.Fatal(err)
	}
```

and after the first `seedAgentData` call:

```go
	if _, err := os.Lstat(filepath.Join(dest, "reference", "sandbox-kit-changes.md")); !os.IsNotExist(err) {
		t.Errorf("a reference file the image no longer ships must be removed: %v", err)
	}
	if !strings.HasPrefix(read(t, filepath.Join(dest, "SANDBOX.md")), "If `/agent-data/context/CORE.md` exists, this is a Jam session:\nits session context governs the sandbox rules — ignore the rest of this file.\n") {
		t.Error("SANDBOX.md must open with the Jam-session guard")
	}
```

(Check `seedAgentData` copes with a pre-existing `dest`: it seeds on first boot when the volume lacks a marker — read the helper; if a pre-existing dir changes first-boot behaviour, run `seedAgentData` once first, then plant the stale file, then run it again and assert.)

- [ ] **Step 2: Run it to see it fail.** `go test ./internal/assemble/ -run TestSeedAgentDataWithBaseImageSeed` → FAIL (no `jam kit draft` in SANDBOX.md; stale file present).

- [ ] **Step 3: Rewrite `SANDBOX.md`.** Keep the first two lines byte for byte. Replace the rest with a short Jam fallback (≤ 25 lines), written to the in-studio agent:
  - title `# Sandbox operating instructions (no Jam session context)` and one sentence: you are in a Jam studio started without a session-context bundle; these are the minimum rules.
  - **What persists** (keep the current paragraph, "rebuild" → "re-raise").
  - **Egress is locked down** (keep the paragraph; "add the domain to the kit" → "ask for the domain to be added").
  - **Changing the sandbox is human-gated**: you cannot rebuild your own studio or widen its access and must never weaken the hardening. Ask a human (who started you, or the project contact) for the exact change and why; an operator makes it in the role's studio kit — a kit draft (`jam kit draft …`) published as the kit's next version, or the role's egress policy (`jam egress set`) — and it takes effect when the studio is re-raised. The hardening layer (proxy, nftables, sshd, entrypoint, credential helper, managed settings, `PATH`/`CLAUDE_CONFIG_DIR`/proxy env) cannot be changed by any kit; if you think it must change, stop and explain why to a human.
  - No pointer to the deleted reference files.

- [ ] **Step 4: Delete the two reference files.** `git rm` both. Keep `reference/progressive-disclosure.md` and keep `reference` in `.refresh`. Then `grep -rn 'sandbox-kit-changes\|sandbox-hardening-limits' --exclude-dir=superpowers .` must print nothing outside `docs/superpowers/`; fix any hit.

- [ ] **Step 5: Boilerplate kit path.** In `changingTheKit` (`internal/jam/sessionctx/boilerplate.go`), step 3 currently says the change takes effect "after the kit is pushed (`jam kit push`) and the cove is re-raised". Make it: after the kit's draft is published (`jam kit draft … publish`, or `jam kit push`) and the cove is re-raised. Keep "cove" (the boilerplate's sandbox word). Update every golden/test that embeds the old sentence (`grep -rln 'kit is pushed' internal`).

- [ ] **Step 6: `session-context.md`.** In §"The sandbox rules", replace "Outside a Jam session an agent learns the sandbox rules from the image's `SANDBOX.md`" with: a session without a bundle (no `CORE.md`) learns the minimum rules from the image's fallback `SANDBOX.md`; with a bundle, the Boilerplate owns them. Keep the guard description. Set `updated: 2026-10-27`.

- [ ] **Step 7: Run tests.** `go test ./internal/assemble/ ./internal/jam/...` → PASS. `just test` → PASS.

- [ ] **Step 8: Commit.**

```bash
git add -A images/jam-studio-base internal/assemble/image_scripts_test.go internal/jam docs/usage/jam/session-context.md
git commit -m "docs(studio): Jam-shaped fallback SANDBOX.md; kit changes via a draft

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Ordering note: the seed's rename-guard exemption is removed in Task 2 (after the board skills are rewritten), not here.

### Task 2: Board skills for Jam's Requisitioner and ticket sessions

**Files:**
- Modify: `images/jam-studio-base/image-files/home/agent/.init-agent-data/skills/board-intake/SKILL.md`
- Modify: `…/skills/board-plan/SKILL.md`
- Modify: `…/skills/board-execute/SKILL.md`
- Modify: `…/skills/board-attend/SKILL.md`
- Modify: `renamed_test.go` (drop the `.init-agent-data/` row of `renamedAllow`)
- Modify: `deleted_test.go` (drop `.init-agent-data/` from `mentionExempt`; fix its comment)

**Interfaces:**
- Consumes: the facts in `docs/usage/jam/requisitioner.md` (READY state + `dispatch:` label gate, dedup, cap, claim READY→IN PROGRESS, raise with the issue brief, keys `AET-42` / `owner/repo#<n>`) and `docs/usage/jam/turn-end.md#reporting-a-ticket` (`report(state, summary, pr?)`, `end`, the PR-watch alarm). Read both before writing.

Each skill keeps its name, its frontmatter shape (`name`, `description`) and roughly its length. Rewrite what it says about the machinery; keep the house task shape (what to do / constraints / definition of done → one PR) and the sizing advice.

- [ ] **Step 1: Write the failing test.** Delete the `{"images/jam-studio-base/image-files/home/agent/.init-agent-data/", []string{`\bat-cove`}}` row from `renamedAllow` in `renamed_test.go`, and the `.init-agent-data/` entry from `mentionExempt` in `deleted_test.go` (update its comment: the slice-4 clause goes).

- [ ] **Step 2: Run it to see it fail.** `go test -run 'Renamed|Deleted' .` → FAIL listing the board skills.

- [ ] **Step 3: Rewrite the four skills** with these facts (and only these — do not invent config keys):
  - **Where the board settings come from.** Not a file in the workspace. Jam's Requisitioner is configured by an operator (`runtime.requisitioner`: one tracker, Linear team or GitHub repo, its state names, `class-label-prefix` default `class:`, `dispatch-label-prefix` default `dispatch:`). The agent learns the team/repo and state names from its session context (the Project layer) or the ticket; if they are not there, ask a human — never guess.
  - **Tracker access.** Through the session's connections (the Linear MCP or API for Linear; `gh` for GitHub Issues).
  - **What gets dispatched.** An issue in the READY state carrying a `dispatch:` label; Jam claims it (READY → IN PROGRESS) and raises one ticket session per issue for the configured role. Blockers: an unfinished blocker means not dispatchable (Linear relations; GitHub "Depends on #N" in the body).
  - **board-intake:** produce one shaped issue; the fast path may add the `dispatch:` label (autonomous) or `class:attended` (supervised) and move it to READY; otherwise leave it for board-plan.
  - **board-plan:** size units for one ticket session (one PR); sub-issues each shaped, READY, with `dispatch:` (autonomous) or `class:attended` (attended). "Hand off to dispatch": the Requisitioner picks them up on its next poll; post the list; do not implement.
  - **board-execute:** two entries — a **ticket session** raised by Jam (the prompt is the issue brief; the session owns branch → push → PR through merge, keeps the ticket current with `report`, finishes with `end`; link turn-end semantics in one line, don't copy them) and a **collaborator** making a direct fix. Drop every `at-task prepare`/`complete` step: the session clones/branches itself (branch first, never the default branch) and opens the PR.
  - **board-attend:** the supervised counterpart to the Requisitioner: it works READY `class:attended` issues that carry no `dispatch:` label, so Jam never raises them; claim (→ IN PROGRESS), work to one PR, move to the review state. Keep the `/loop` usage.
  - Replace every "at-cove dispatch", "the scheduler", "`workers:`/`collaborators:` keys", "`.at-cove/config.yml` → `tracker.linear`".

- [ ] **Step 4: Run tests.** `go test -run 'Renamed|Deleted' .` → PASS; `grep -rnE 'at-cove|at-task|at-switchboard|\.at-cove|scheduler' images/jam-studio-base/image-files/home/agent/.init-agent-data` → nothing.

- [ ] **Step 5: Commit.**

```bash
git add -A images/jam-studio-base renamed_test.go deleted_test.go
git commit -m "docs(studio): board skills follow Jam's Requisitioner and ticket sessions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: The repo's own docs around Jam

**Files:**
- Modify: `docs/OVERVIEW.md` (the interim banner at lines 12–19; at-cove-era words; the `allowed_domains.session.txt` row's "went with at-cove" wording; the seed paragraph near line 147 that lists `reference/`)
- Modify: `AGENTS.md`, `docs/DEVELOPMENT.md`, `docs/INDEX.md`, `docs/usage/INDEX.md`, `README.md`, `docs/TODO.md` — as the grep in Step 1 finds
- Modify: `docs/usage/jam/kits.md:99` (the "(jam-only slice 2)" history sentence → drop or shorten to the present fact)
- Modify: `renamed_test.go` (drop the `docs/OVERVIEW.md` row of `renamedAllow`)
- Modify: `AGENTS.md` "Design history" line — keep pointing at the jam-only spec, but as history

- [ ] **Step 1: Inventory.** Run

```bash
grep -rnE 'at-cove|\.at-cove|at-task|at-switchboard|switchboard|config\.yml|allowed-domains|image\.env|image\.paths|slice [0-9]|interim|Interim' \
  docs/OVERVIEW.md AGENTS.md docs/DEVELOPMENT.md docs/INDEX.md docs/usage/INDEX.md README.md docs/TODO.md docs/usage/jam/kits.md
```

and list each hit. `config.yml` in `studio-manager.md` (the manager's own config) is correct — only at-cove's kit `config.yml` is stale. A studio kit has `kit.yml` (`base:`, `egress:`, `build-args:`, a `Dockerfile`, a tree) — check `docs/usage/jam/kits.md` for the exact keys before rewording.

- [ ] **Step 2: Drop the renamed-guard exemption for OVERVIEW** (`renamedAllow` row `{"docs/OVERVIEW.md", …}`) and run `go test -run Renamed .` → FAIL on the banner.

- [ ] **Step 3: Rewrite.** Delete the interim banner. Replace at-cove-era kit words with the studio-kit terms (link `usage/jam/kits.md` rather than restating the schema). In the egress-tiers table say what the session tier is now (baked empty; the per-session tier is unused) without naming at-cove. Update the seed paragraph to the current `reference/` contents (`progressive-disclosure.md` only). AGENTS.md: the "Design history" line names the jam-only spec as the record of why Jam is the only mode; keep everything else unless the grep finds a stale fact. Each changed doc gets `updated: 2026-10-27`. Leave `renamed-from-at.md` and `docs/superpowers/` alone.

- [ ] **Step 4: Dev kit and justfile.** Confirm there is no `.at-jam/` or `.jam/` dev-kit directory in the repo (`ls -a`; the `cove-ic` kit lives in Jam) and that `grep -nE 'at-|cove-master' justfile` finds only the header comment's project name. Record the result in the commit message; no change expected.

- [ ] **Step 5: Run checks.** `go test -run 'Renamed|Deleted' .` → PASS; `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new errors outside `docs/superpowers/`; `just test`, `just lint` (exit 0), `go vet -tags integration ./...`, `bash install.test.sh` → PASS.

- [ ] **Step 6: Commit.**

```bash
git add -A docs AGENTS.md README.md renamed_test.go
git commit -m "docs: describe Jam as the only mode; drop slice-4 exemptions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
