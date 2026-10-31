# Always a repo, slice 5: docs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The usage docs describe a Jam whose config changes only in git. `config.git` is documented, and no current doc tells anyone to run a removed verb or press a removed button.

**Architecture:** Edit the docs that own each subject, per the docs-author skill, with `docs/INDEX.md` kept in sync. A guard test fails when a removed verb comes back into the current docs.

**Tech Stack:** Markdown; Go test (`deleted_test.go` pattern).

**Spec:** `docs/superpowers/specs/2026-10-31-always-a-repo-design.md`.

## Global Constraints

- **Follow the docs-author skill.** One owner doc per fact; frontmatter says what each doc is and when to read it; `docs/INDEX.md` is a map, with one row per doc.
- **`config.git` is documented in `docs/usage/jam/config-git-sources.md`,** the owner of the serve-config `config:` block. It covers the URL forms, `branch` required, SSH `cred` and `host-keys`, no PR, and the checked-out-branch refusal with its exact message.
- **Removed verbs** must not appear in `docs/` outside `docs/superpowers/`:
  - `jam role add`, `jam role rm`;
  - `jam project create`, `jam project rm`, `jam project rename`;
  - `jam user add`, `jam user rename`, `jam user rm`;
  - `jam connection add`, `jam connection cred`;
  - `jam room add`;
  - `jam context set`, `jam context clear`;
  - `jam kit push`, `jam kit pin`, `jam kit rm`, `jam kit draft`;
  - `jam standing add`, `jam egress set`, `jam destination add`;
  - `jam model-spec add`;
  - `jam config apply`, `jam config release`;
  - `--force` next to `config apply` or `sync`;
  - `config.drift`.
- **`dev/` and `deploy/` scripts** that call removed verbs are updated to a config repo, or the call is dropped. Grep them.

## Review Focus

- A doc that taught a removed verb, as the way to do a task (adding a role, changing a kit), now points at the branch editor or a git commit. It must not just delete the paragraph.
- `docs/DEVELOPMENT.md`/`dev/README` dev Jam setup now uses `config.git` with a local repo. There is a worked example.
- `monitoring.md` no longer lists `config.drift`.
- The deprecated `*-token-cred` docs say they bind an existing connection only.
- The studio template's `SANDBOX.md` text about `jam kit draft` (it lives outside this repo: `/agent-data/SANDBOX.md:27-29` mirrors it) is out of scope. It gets a note in the PR description.

---

### Task 1: The usage docs and the guard

**Files:**
- Modify: the docs listed in `/home/agent/workspace/notes/aar-inventory.md` §"Docs describing live editing", and `docs/INDEX.md`.
- Modify: `deleted_test.go`. Add a `removedConfigVerbs` list, the Global Constraints list, checked over `docs/` with the existing `mentionExempt` (which already exempts `docs/superpowers/`), plus `dev/` and `deploy/`.

- [ ] **Step 1: Write the failing guard test** `TestNoRemovedConfigVerbsInDocs` in `deleted_test.go`, reusing its file walk and exemption helpers.
- [ ] **Step 2: Run it and see it fail.** Run: `go test . -run TestNoRemovedConfigVerbsInDocs -v`. Expected: FAIL, listing the docs that mention removed verbs.
- [ ] **Step 3: Edit the docs** (load the docs-author skill first) until the guard passes and every Review Focus line holds.
- [ ] **Step 4: Run the guard and the docs audit.** Run: `go test . -run 'TestNoRemovedConfigVerbsInDocs|Deleted|Renamed' -v`, then the docs-audit skill's checker. Expected: PASS, and no orphans or dangling links.
- [ ] **Step 5: Commit:** `git commit -am "docs: config changes only in git; config.git"`
