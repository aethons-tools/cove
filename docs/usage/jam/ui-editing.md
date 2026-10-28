---
summary: What the Jam admin console can change — enroll/revoke agents, roles and grants, raising and tearing down agents and requesting a personal agent, and the kit/destination/model-spec registry — with the type-ahead fields, how a write's outcome and refusals show, and the gate/CSRF/audit rules every write obeys.
read_when: You want to change something from the Jam admin UI instead of the CLI — enroll or revoke an agent, add a grant, create a role, raise or tear down an agent, request a personal agent, edit a kit/destination/model-spec — or a UI write was refused and you want to know why.
owns: the admin console's write surface — enroll/revoke/grant/role create-delete, the type-ahead reference fields, create panels, confirmations and how refusals show, the CSRF origin check and audit logging, runtime raise/teardown and Request, and the config-plane editing pointers
prereqs: ui.md for reaching the UI and its sections; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive
tier: leaf
updated: 2026-10-28
---

# Editing from the admin UI

Beyond viewing, the [admin UI](ui.md) can do the roster day-job, raise and tear
down studios, and edit the config registry. Every write obeys the same gate as
the views, is audit-logged against the operator, and is refused unless it comes
from the UI itself.

## Roster and roles

Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
verbs in [roster.md](roster.md):

- **Enroll** an agent on the Agents tab (**Enroll agent**: id, role, project, optional destination overrides).
  The identity token is shown **once**, right after enrolling — copy it then; it
  is never shown again, stored in a list, or logged. For the full connection
  snippet (env vars / git config), use the CLI `jam enroll`.
- **Revoke** an agent and **add/remove** its grants on its
  [agent page](ui-pages.md#agent-pages); **create/delete** a role (and edit it
  on its [role page](ui-projects.md#role-pages)).
- Destination fields (role, enroll/grant overrides) take the CLI's
  `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
  or a mapping for a destination not in scope is rejected. Credential *names*
  are references, not secrets, so the UI shows them (a project's role list renders
  `git → git-pat`); credential *values* never appear.
- Every field that names another entity is a **type-ahead**: projects, roles
  (of the project in the same form), kits, destinations and — after `=` in a
  destinations list — credentials, roster targets (`user:`/`channel:` in
  addressing and escalation tiers), Intercom participants, and chat services.
  In list fields it completes the entry under the cursor. ↑/↓ move, Enter or
  Tab accept, Esc closes. Suggestions guide but don't restrict: the server
  still validates, so a glob like `user:*` is fine and an unknown project is
  refused (a project must exist first — [projects.md](projects.md)). Project
  fields start at `default`. Credential suggestions are the names `jam serve`
  is configured with (names only, never values).

Create forms sit in collapsed panels (**New project**, **New role**,
**Enroll agent**, **Raise agent**, **Add member**, …). A form shows it is
busy and can't send twice. A refused write (validation error, conflict, CSRF
refusal) shows the server's message, rather than failing silently: inline in
its form, keeping what you typed, or — for a confirmed action such as Delete
or Teardown — as a notification that stays until dismissed.

Every change obeys the same gate as the views (loopback, or an off-loopback
session with `require-scope`) and is recorded in Jam's audit log against the
operator who made it. Destructive actions ask for confirmation in a dialog.
The console's writes are JSON `POST`/`DELETE`s to `/app/admin/api/`, refused
(`403`) unless they originate from the Jam app itself (an Origin/Referer
check, plus any exact origins listed in [`ui-origins`](serve.md)), so another
site can't drive them through your browser.

The kit registry and destinations are also editable from here — see
[Config plane (kits, destinations, model-specs)](#config-plane-kits-destinations-model-specs) below.
Raising and tearing down studios is editable from the UI when a runtime
supervisor is configured — see [Runtime (studios)](#runtime-studios) below.

### Runtime (studios)

When Jam is configured with a runtime supervisor (`runtime:` in the serve
config — see [coves.md](coves.md)), the Agents tab can also:

- **Raise a managed studio** (**Raise agent**) — a label, role, optional project/unit and a workload
  prompt. Jam handles the studio's identity token and launch secret internally;
  they are never shown in the browser (use the CLI `jam studio raise` for
  manual wiring).
- **Tear down a studio** (confirmed) — from its row or its agent's page.

A project's role list (**Request**, on its Agents page) and each role page (**Request agent**) raise a
personal agent — a [personal session](personal-sessions.md) — of that role **for you**, with the
prompt `Squawk me (user:<your name>) and we will get to work.`, so the
agent opens the conversation with you on the intercom. You must be signed in
(`/app/auth/login`) as a login linked to a member of the role's project.
As anonymous loopback `local`, the action asks you to sign in. Admission,
delivery checks, and errors are exactly those of `jam session request`, and
the outcome (the new agent id, or the refusal) shows as a notification.

Without a runtime supervisor, studios are view-only. Setting a studio's
activity is not a UI action — that is reported by the studio itself. These actions
obey the same gate, CSRF, and audit-logging as the roster edits above.

### Config plane (kits, destinations, model-specs)

- **Kits** — create a kit (name + studio-kit YAML, validated like `kit push`)
  and delete an unused one; each kit's page shows its versions, diffs them,
  pins one, and pushes new versions — see [ui-pages.md](ui-pages.md#kit-pages); its
  Draft section edits a draft ([kits-drafts-ui.md](kits-drafts-ui.md)).
- **Destinations** — create one (every field, including client env, git
  routing and the session note) and remove one; each destination's page shows and
  edits it — see [ui-pages.md](ui-pages.md#destination-pages).
- **Model-specs** — create, edit and delete one, validated exactly like
  `jam model-spec` — see [ui-pages.md](ui-pages.md#model-spec-pages).

A kit config references credentials by name only (no secret values), and a
destination's `cred-name` (or a model-spec's principal) is a reference, not a secret — the UI shows the name
but never a credential value. These actions obey the same
gate, CSRF, and audit-logging as the other edits.
Objects a config repo manages carry a `managed · config @<commit>` badge, and every write to one asks first — see [ui-config.md](ui-config.md).
