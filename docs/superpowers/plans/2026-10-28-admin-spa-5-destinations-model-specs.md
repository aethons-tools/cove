# Admin SPA slice 5 (Destinations and Model Specs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the admin app, the Config tree's **Destinations** and **Model Specs** nodes work, at parity with the old pages:
- each list has New and Delete;
- a destination's page has the conflict banner, Broker, Agent connector, Used by, and edit;
- a model spec's page has Harness, Policy, Used by, Claude, and edit.

**Architecture:** `adminui/api_specs.go` holds the JSON twins of `dest_detail.go` and `model_specs.go`. Their form readers become `destFromFields(get, name)` and `specFromFields(get, name)`, both taking a field getter like slice 4's `formOf`. The HTML handlers pass `r.FormValue`, and the JSON handlers pass a string map mapped onto the same field names. Both run `jam.CreateDestination` / `UpdateDestinationKeepSpecs` / `CreateModelSpec` / `UpdateModelSpec` / `RemoveModelSpec` as today. The app gets `specs/` pages built on the shared components.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3, §6 slice 5). Conventions: slices 1–4's plans and `docs/web-app.md` "Admin area".

## Global Constraints

- **Parity confirms** (drift note appended as the templates do):
  - "Delete destination <n>?" (+ " <N> role(s) list it." when used) (+ " <drift>")
  - "Delete model-spec <n>?" (+ " <drift>")
  - "<drift> Save anyway?" on a managed object's edit.
- A create's 409 adds "; edit it on its page".
- Model-spec errors never echo a provider-env or header value: they name the line number or the key, as `parseProviderEnv` and `parseHeaderRules` do. The JSON reuses those parsers unchanged.
- Principal choices are credential **names** only (`specUI.choices`), plus `pool` when a pool is configured. The stored principal is kept even when it's no longer configured.
- **Destination selects:**
  - Identity in and Apply list `jam.Presets`.
  - `custom` shows, selected, only when the destination already uses it, labelled "custom (spec shown in Broker section; edit via import/API)".
  - Header logging: in is `none|examined|all`, out is `none|changed|all`.
- The destination conflict banner lists each conflicting role once (`ConflictRoles`) and each conflict line. Roles link to their leaves, and the other destination links to its page.
- Used-by rows link to role leaves. An implicit model-spec use shows "no model-spec set".

## Review Focus

1. **A destination using custom specs.** Saving the edit form keeps the specs (`UpdateDestinationKeepSpecs`), and the select shows "custom (…)". Tested in Task 1 (API) and Task 3.
2. **A model-spec whose principal credential was removed from the serve config.** It still shows as the selected option, and an unchanged save isn't silently switched. Tested in Task 1 and Task 4.
3. **A provider-env line without "="** gets 400 naming the line number. The pasted value isn't in the response. Tested in Task 1.
4. **Deleting the default model-spec, or one roles use,** is refused with `ModelSpecRemoveStatus`'s status, shown as a red notification. Tested in Task 1 and Task 4.
5. **The settings field with non-object JSON** (`[]`, `"x"`) gets 400 "claude.settings is not a JSON object", inline. Tested in Task 1.

---

### Task 1: Destinations and model-specs API

**Files:**
- Create: `internal/jam/adminui/api_specs.go`, `internal/jam/adminui/api_specs_test.go`
- Modify:
  - `internal/jam/adminui/apitypes.go`;
  - `internal/jam/adminui/dest_detail.go` (`destFromForm` → `destFromFields(get func(string) string, name string)`);
  - `internal/jam/adminui/model_specs.go` (`specFromForm` → `specFromFields(get, name)`);
  - `internal/jam/adminui/api.go`: `registerApp` takes `specUI`, built in `Handler` as `registerModelSpecs` gets it.

**Interfaces:**
- Produces (types, camelCase json, lists never nil):
  ```go
  type DestListRow struct { Name, Route, Upstream, IdentityIn, Apply, CredName string; EnvKeys []string; GitRouted bool; UsedBy int; Managed *Managed `tstype:"Managed | null"`; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type DestinationsResponse struct { Destinations []DestListRow; Methods []string }
  type EnvRow struct { Key, Template string }
  type DestUse struct { Project, Role, Cred string; CredIsRole bool }
  type DestConflict struct { Project, Role, Other, What string }
  type DestFields struct { Route, Upstream, IdentityIn, Apply, CredName, Env, AllowPaths, Note, LogHeadersIn, LogHeadersOut string; Git bool } // the form's values ("none" for unset logging)
  type DestDetail struct {
      Name string; Managed *Managed `tstype:"Managed | null"`; Fields DestFields; Methods []string
      IdentityInSpec, ApplySpec string // the custom specs, rendered as the Broker section shows them ("" = none)
      AllowPaths []string; Env []EnvRow; EnvImplied, GitRouted, GitImplied bool
      Uses []DestUse; Conflicts []DestConflict; ConflictRoles []string
  }
  type SpecListRow struct { Name, Type, Version, VersionConstraint, Principal, ModelID, Effort, Mode, Provider string; UsedBy int; Managed *Managed `tstype:"Managed | null"` }
  type SpecChoices struct { Types, Principals, Modes, Providers []string }
  type SpecFields struct { Type, Version, VersionConstraint, Principal, ModelID, Effort, Mode, Provider, Headers, Allow, Deny, ProviderEnv, Plugins, Settings, Note string }
  type SpecUse struct { Project, Role string; Implicit bool }
  type HeaderRuleRow struct { Name, Op, Value string } // Op "=" or "+="
  type SpecDetail struct {
      Name string; Managed *Managed `tstype:"Managed | null"`; Fields SpecFields; Choices SpecChoices
      HeaderRules []HeaderRuleRow; Allow, Deny []string; HasClaude bool; ProviderEnvKeys, Plugins, SettingsKeys []string
      Uses []SpecUse
  }
  type SpecsResponse struct { Specs []SpecListRow; New SpecFields; Choices SpecChoices } // New: the create form's seed (claude on anthropic)
  ```
- Routes (log lines as the HTML):
  - `GET api/destinations`;
  - `POST api/destinations {name, …fields}` → 201 `Created{Path: config/destinations/<n>}`;
  - `GET api/destinations/{name}` → `DestDetail`, or 404;
  - `POST api/destinations/{name} {…fields}` → `DestDetail`;
  - `DELETE api/destinations/{name}` → 204;
  - `GET api/model-specs`;
  - `POST api/model-specs {name, …fields}` → 201;
  - `GET api/model-specs/{name}` → `SpecDetail`, or 404;
  - `POST api/model-specs/{name}` → `SpecDetail`;
  - `DELETE api/model-specs/{name}` → 204, or `ModelSpecRemoveStatus`.

  JSON field names map onto the form names: `identityIn`→`identity-in`, `credName`→`cred-name`, `allowPaths`→`allow-paths`, `logHeadersIn`→`log-headers-in`, `logHeadersOut`→`log-headers-out`, `git` ("on" or ""); `versionConstraint`→`version-constraint`, `modelId`→`model-id`, `providerEnv`→`provider-env`. Every other name is the same.
- [ ] **Step 1: Write the failing tests** in `api_specs_test.go`:
  - `TestDestinationsAPI`:
    - create (409 on repeat with the suffix) → list (env keys, git) → get → edit the note → delete;
    - a destination with an `IdentityInSpec` (via `jam.UpdateDestination`), saved through the API with `identityIn: "custom"`, keeps its spec;
    - two roles' destinations both setting `GH_HOST` differently → `conflicts`/`conflictRoles`.
  - `TestModelSpecsAPI`:
    - create (409 suffix) → get (choices: credential names only, plus a stale principal kept) → edit;
    - a provider-env line `oops` → 400 "provider-env line 1 is not KEY=VALUE", with no `oops` in the body;
    - settings `[]` → 400 "claude.settings is not a JSON object";
    - deleting a spec a role uses → its `ModelSpecRemoveStatus`;
    - an unused one → 204.
  - The existing destination and model-spec HTML tests keep passing.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'DestinationsAPI|ModelSpecsAPI'` → FAIL.
- [ ] **Step 3:** Implement, refactor the form readers, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): destinations and model-specs API`.

### Task 2: Shared field helpers

**Files:**
- Create: `web/app/src/admin/ui/Facts.tsx` (a definition list: `Facts({ rows: [label, ReactNode | null, unsetText?][] })`, where `null` shows `unsetText` dimmed), `web/app/src/admin/ui/facts.test.tsx`

- [ ] **Step 1:** Write the failing test: a `null` value shows its unset text dimmed.
- [ ] **Step 2:** Run it → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npx vitest run src/admin/ui/facts` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin Facts list`.

### Task 3: Destinations pages

**Files:**
- Create: `web/app/src/admin/specs/DestinationsPage.tsx`, `DestinationPage.tsx`, `DestFields.tsx`, `destinations.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`config/destinations`, `config/destinations/:name`)

**Interfaces:**
- Produces:
  - `DestFields({ value, onChange, methods, creating })`: the old `dest-fields` grid, with the same labels and hints, and Default credential as a `TypeAhead kind="credentials"`. A Name field appears only when creating, and the custom option only when it's the current value.
  - **DestinationsPage**: the heading "Destinations" with its sub, "New destination" (`DestFields` seeded bearer → bearer), and a `DataTable` with the old columns. Name links to the node, with `AttnFlag`; Delete is a `ConfirmAction` with the parity text.
  - **DestinationPage**: a header (name, `ManagedBadge`, `route → upstream`, Delete → navigate to the list), the conflict banner, Broker (`Facts`), Agent connector (an env table, the implied note, git routing), Used by (role leaf links, credential plus role mapping/default/none), and "Edit destination" (`DestFields`, prefilled, with the drift confirm). Not found → "Destination not found".
- [ ] **Step 1: Write the failing tests** in `destinations.test.tsx`:
  - list and create navigation;
  - the 409 suffix inline;
  - Delete confirm text with "2 role(s) list it.";
  - the conflict banner with links;
  - edit posts the prefilled fields;
  - the custom option shows only for a custom destination;
  - not found.
- [ ] **Step 2:** `npx vitest run src/admin/specs/destinations` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin destinations pages`.

### Task 4: Model Specs pages

**Files:**
- Create: `web/app/src/admin/specs/ModelSpecsPage.tsx`, `ModelSpecPage.tsx`, `SpecFields.tsx`, `modelspecs.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`config/model-specs`, `config/model-specs/:name`)

**Interfaces:**
- Produces:
  - `SpecFields({ value, onChange, choices, creating })`: the old `spec-fields` grid, with the same labels, hints and placeholders. The selects come from `choices`; Principal has a "—" first option; Mode's "" option is "unset = bypassPermissions (legacy)".
  - **ModelSpecsPage**: the heading "Model-specs" with its sub, "New model-spec" (seeded from `new`), and a `DataTable` with the old columns plus Delete.
  - **ModelSpecPage**: a header (name, `ManagedBadge`, `type version`, Delete), then:
    - Harness (`Facts`, with header rules as `name op value` badges);
    - Policy;
    - Used by (role links; "no model-spec set" for implicit);
    - Claude (provider, provider env keys, plugins "installed at image build", settings keys; or "No body.");
    - "Edit model-spec" (`SpecFields`, prefilled, with the drift confirm).

    Not found → "Model-spec not found".
- [ ] **Step 1: Write the failing tests** in `modelspecs.test.tsx`:
  - list and create;
  - a stale principal is listed and selected;
  - a 400 shows inline with the typed settings kept;
  - a refused delete shows a red notification;
  - implicit uses are labelled;
  - not found.
- [ ] **Step 2:** `npx vitest run src/admin/specs/modelspecs` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin model-specs pages`.

### Task 5: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminDestinationNote`), `docs/web-app.md`, `docs/usage/jam/ui-console.md`, and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminDestinationNote`:
  - open `config/destinations/gh`;
  - open "Edit destination";
  - set Note and save;
  - wait for the note in Broker;
  - assert it's stored and there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for a destination edit; docs`.
