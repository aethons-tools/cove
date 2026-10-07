---
summary: What the Jam admin UI can change — enroll/revoke agents, roles and grants, raising and tearing down studios and requesting a personal session, and the kit/destination/model-spec registry — with the type-ahead fields, the write banner, and the gate/CSRF/audit rules every write obeys.
read_when: You want to change something from the Jam admin UI instead of the CLI — enroll or revoke an agent, add a grant, create a role, raise or tear down a studio, request a personal session, edit a kit/destination/model-spec — or a UI write was refused and you want to know why.
owns: the admin UI's write surface — enroll/revoke/grant/role create-delete, the type-ahead reference fields, create panels and the write banner, the CSRF origin check and audit logging, runtime raise/teardown and Request, and the config-plane editing pointers
prereqs: ui.md for reaching the UI and its sections; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive
tier: leaf
updated: 2026-10-07
---

# Editing from the admin UI

Beyond viewing, the [admin UI](ui.md) can do the roster day-job, raise and tear
down studios, and edit the config registry. Every write obeys the same gate as
the views, is audit-logged against the operator, and is refused unless it comes
from the UI itself.

## Roster and roles

Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
verbs in [roster.md](roster.md):

- **Enroll** an agent on the Agents list (id, project, role, optional destination overrides).
  The identity token is shown **once**, right after enrolling — copy it then; it
  is never shown again, stored in a list, or logged. For the full connection
  snippet (env vars / git config), use the CLI `at-jam enroll`.
- **Revoke** an agent and **add/remove** its grants on its
  [agent page](ui-pages.md#agent-pages); **create/delete** a role (and edit it
  on its [role page](ui-projects.md#role-pages)).
- Destination fields (role, enroll/grant overrides) take the CLI's
  `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
  or a mapping for a destination not in scope is rejected. Credential *names*
  are references, not secrets, so the UI shows them (a project's Roles renders
  `git → git-pat`); credential *values* never appear.
- Every field that names another entity is a **type-ahead**: projects, roles
  (of the project in the same form), kits, destinations and — after `=` in a
  destinations list — credentials, roster targets (`user:`/`channel:` in
  addressing and escalation tiers), Intercom participants, and chat services.
  In list fields it completes the entry under the cursor. ↑/↓ move, Enter or
  Tab accept, Esc closes. Suggestions guide but don't restrict: the server
  still validates, so a glob like `user:*` is fine and an unknown project is
  refused (a project must exist first — [projects.md](projects.md)). Project
  fields start at `default`. Credential suggestions are the names `at-jam serve`
  is configured with (names only, never values).

Create forms sit in collapsed **+ Add …** panels above each table. The
outcome of a write shows in a banner at the top of the page: a refused write
(validation error, conflict, CSRF refusal) appears as a dismissible error with
the server's message, rather than failing silently.

Every change obeys the same gate as the views (loopback, or an off-loopback
session with `require-scope`) and is recorded in Jam's audit log against the
operator who made it. Destructive actions ask for confirmation. State-changing
requests are refused unless they originate from the Jam UI itself (an
Origin/Referer check, plus any exact origins listed in
[`ui-origins`](serve.md)), so another site can't drive them through your browser.

The kit registry and destinations are also editable from here — see
[Config plane (kits, destinations, model-specs)](#config-plane-kits-destinations-model-specs) below.
Raising and tearing down studios is editable from the UI when a runtime
supervisor is configured — see [Runtime (studios)](#runtime-studios) below.

### Runtime (studios)

When Jam is configured with a runtime supervisor (`runtime:` in the serve
config — see [coves.md](coves.md)), the Agents list can also:

- **Raise a managed studio** — a label, role, optional project/unit and a workload
  prompt. Jam handles the studio's identity token and launch secret internally;
  they are never shown in the browser (use the CLI `at-jam studio raise` for
  manual wiring).
- **Tear down a studio** (confirmed) — from its row or its agent's page.

A project's Roles section and each role page (**Request agent**) gain a **Request** action: it raises a
[personal session](personal-sessions.md) — a personal agent — of that role **for you**, with the
prompt `Squawk me (user:<your name>) and we will get to work.`, so the
session opens the conversation with you on the intercom. You must be signed in
(`/ui/auth/login`) as a login linked to a member of the role's project.
As anonymous loopback `local`, the action asks you to sign in. Admission,
delivery checks, and errors are exactly those of `at-jam session request`, and
the outcome (the new session id, or the refusal) shows in the page's banner.

Without a runtime supervisor, studios are view-only. Setting a studio's
activity is not a UI action — that is reported by the studio itself. These actions
obey the same gate, CSRF, and audit-logging as the roster edits above.

### Config plane (kits, destinations, model-specs)

- **Kits** — create a kit (name + studio-kit YAML, validated like `kit push`)
  and delete an unused one; each kit's page shows its versions, diffs them,
  pins one, and pushes new versions — see [ui-pages.md](ui-pages.md#kit-pages).
- **Destinations** — create one (every field, including client env, git
  routing and the session note) and remove one; each destination's page shows and
  edits it — see [ui-pages.md](ui-pages.md#destination-pages).
- **Model-specs** — create, edit and delete one, validated exactly like
  `at-jam model-spec` — see [ui-pages.md](ui-pages.md#model-spec-pages).

A kit config references credentials by name only (no secret values), and a
destination's `cred-name` (or a model-spec's principal) is a reference, not a secret — the UI shows the name
but never a credential value. These actions obey the same
gate, CSRF, and audit-logging as the other edits.
