# intercom 1a-4: connections in serve config

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal (spec §7):**
- Connections become admin-managed: add, rename, set credential, remove.
- Serve config names the connection the Requisitioner and the Discord relay use.
- `Project.ChatService` holds a connection id.

The old `tracker-token-cred` / `bot-token-cred` keep working, with a deprecation warning, by binding the implicit `linear` / `discord` connections that 1a-3a created.

## Decisions
- **Relay state stays keyed by service**, deferred from spec §7. There is one relay engine per service today, so re-keying cursors by connection only pays off with multi-connection relays (a later change, noted in TODO).
- **A connection's credential is a name** that must be a demanded credential in serve config. It is checked when serve resolves the connection, since the store can't see serve config.
- **"The connection of kind K"** means the live connection named K if there is one, else the lowest-id live connection of kind K. The roster view and the humans migration resolve handles and Discord ids through it, instead of assuming the name.
- **`SetChatService(project, ref)`** takes a connection name or id (or "") and stores the id. The connection must be of a chat kind (`discord`). `ChatKind(store, project)` gives consumers the kind, replacing every `ChatService == "discord"` check.
- **Migration:** `jam_settings.roster_schema` 1 → 2 turns stored `ChatService` kind names (`"discord"`) into the id of the connection of that kind, creating it if absent. It runs in the same Postgres load step as the humans migration, and inside `ImportConfig`.
- **`RemoveConnection`** is refused while accounts or a project's chat service reference the connection. Serve config references are checked at serve start, which fails closed on an unknown connection.

## Surface
- Store: `SetConnectionCred(id, cred)`; `prepareRemoveConnection` checks chat services.
- API:
  - `POST /admin/connections {kind, name, cred}`
  - `PUT /admin/connections/{c}/name`
  - `PUT /admin/connections/{c}/cred`
  - `DELETE /admin/connections/{c}`
  - `GET/PUT /admin/projects/{p}/chat-service` speaks a connection name
- CLI: `at-jam connection add --kind K --name N [--cred C] | list | rename | cred | rm`
- Serve config: `runtime.requisitioner.connection`, `runtime.discord.connection` (exclusive with the deprecated `*-token-cred`)

## Tasks
1. Store: `SetConnectionCred`; the remove refusal; conformance tests.
2. Connection-of-kind lookup; the roster view and migration use it.
3. `ChatService` as a connection id: store `SetChatService` by ref; `ChatKind`; the roster_schema 2 migration (both stores, import); consumers (relay, nag, personal sessions, UI select).
4. Admin API + client + CLI for connections; chat-service by connection name.
5. Serve config: the `connection` keys; mapping the deprecated keys; `resolveServeConnection` (kind check, credential demanded) with unit tests.
6. Docs: serve.md, requisitioner.md, discord.md, roster.md (connections), and a TODO entry for multi-connection relays.
