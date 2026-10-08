# intercom slice 2: channel-centric intercom — plan series

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Each sub-plan is its own PR, test-first, written against the code the previous one landed.

**Spec:** [`../specs/2026-10-06-intercom-slice2-channels-design.md`](../specs/2026-10-06-intercom-slice2-channels-design.md) (approved 2026-10-06; §9 decided as recommended).

## Where things stand (inventory, 2026-10-06)

- **Roster channels** are `Project.Roster.Channels []Channel{Name, Service, Ref}` in the project doc. `Service` is a kind name (`linear`, `discord`), not a connection.
  - Written by `AddChannel` / `RemoveChannel`, through the admin API, `at-jam project roster add-channel|rm-channel` and the admin UI project page.
  - Read by `decide.go` (resolve and list targets), `channels.go` (`/me` projection), `relay_linear.go` (route and resolve), `relay_discord.go` (polled channels), `adminui/suggest.go` and `identity.go`.
- **The log** is `intercompg` (its own migration runner, the same pool). Every reader keys on `To[]`; see the spec §3 and the inventory in the spec PR.
- **Projects are hard-deleted** (tombstones are 1b-3). A project's rooms go with it until channels carry history.

## Sub-plans

### 2a-1: channel registry and rooms

**Goal:** channels are registry rows, and roster channels become rooms. Every existing consumer keeps reading `Roster.Channels`, now a view computed from rooms (the 1a-3a pattern).

- **Schema.** jam migration `0010_channels.sql` creates `channels`, `channel_bindings` and `channel_members`, as in spec §2. `channel_reads` waits for 2b, its first user.
  - `channels.project_id` is `REFERENCES projects(id) ON DELETE CASCADE`, and so are bindings and members via `channel_id`. Removing a project removes its rooms, as it removes its roster today.
  - 2b revisits this once the log references channels (it needs project tombstones; see 2b).
- **Types** (`internal/jam/channel_registry.go`):
  - `Channel{ID, ProjectID, Kind, Key, Label, Status}` and `Binding{ConnectionID, Ref, Mode}`.
  - `ChannelStatus` is `live|archived`. `BindBoth = "both"`, `BindEgress = "egress"`.
  - The roster's `Channel` type is renamed `RosterChannel`; it is deleted in 2b. Its JSON is unchanged.
- **Store** (`ChannelStore`, embedded in `Store`, with mem and pg implementations using memState prepare/apply):
  - `CreateChannel(c Channel, bs []Binding) (Channel, error)`, where `(project, kind, key)` must be live-unique and a `both` binding must be unique per `(connection, ref)`;
  - `GetChannel`, `ChannelByKey(project, kind, key)`, `ChannelByBinding(conn, ref)` (live `both` bindings), `ListChannels(project, kind)`;
  - `RenameChannel(id, key, label)`, `SetChannelBindings(id, bs)`, `ArchiveChannel(id)`;
  - `JoinChannel(ch, p, seq)`, `LeaveChannel(ch, p, seq)`, `ChannelMembers(ch)` (current members), `ChannelsOf(p)`.
  - Errors: `ErrChannelNotFound`, `ErrChannelExists` and `ErrBindingTaken`. `ErrRemoved` covers an archived channel.
- **Directory:** `Resolve(chn_…)` returns the label (archived counts as removed for rendering). `RemoveConnection` is refused while a live channel binds it.
- **Roster view:** `viewProject` fills `Roster.Channels` from the project's live rooms, sorted by name, with `Service` = the binding connection's kind and `Ref` = the binding ref. `AddChannel` and `RemoveChannel` write rooms:
  - an upsert by name sets the binding on `ConnectionOfKind(service)`, creating the implicit connection if there is none, as `planUserAccount` does;
  - remove archives the room (archived rooms free their name).
- **Migration** `roster_schema` 5 (`planRooms`): every project doc's `Roster.Channels` becomes a room with its binding, and the doc is cleared. The same step runs for an import of an older snapshot.
- **Snapshot v3:** `ConfigSnapshot.Channels` carries rooms (live and archived) with their bindings. Export writes them sorted by id. Import replays them through the store's own rules, and v1 and v2 snapshots import their roster channels via step 5.
- **Tests:**
  - conformance (mem + pg): create, lookups, uniqueness, archive frees the name, the binding-taken refusal, membership join/leave/rejoin, project removal cascades, the connection-in-use refusal;
  - the roster view round trip (`AddChannel` then `GetRoster`);
  - migration fixtures, step 5 included;
  - snapshot v3 round trip and v2 import.
- **Docs:** `comms-addressing.md` (channels are rooms in the registry), `backup.md` (v3).

### 2a-2: sources and `Post` (not yet on the wire)

- `internal/jam/intercom_sources.go`: the `Source` interface (spec §2) and the `chat`, `ticket` and `room` sources over `ChannelStore` and `Directory`; `Intercom.Resolve(ctx, addr)` for the grammar in spec §5.
- **Ticket channels at setup:** the dispatcher's setup creates or rejoins `ticket:<tracker connection>/<unit>` and joins the session (`joined_seq` = log tail). Teardown leaves it. This is harmless before 2b and means sessions set up after 2a-2 need no cutover backfill.
- `Post` itself needs the new log, so it lands in 2b. 2a-2 ships `Intercom.Plan(from, addr) (Channel, audience, error)`: the resolve, authorization and audience step, tested on its own (fail-closed ordering, ceilings, ended sessions, archived channels).
- As built: `Intercom.Plan(poster, addr, now)` for a session's address, `PlanChannel(poster, channel)` for a post into an existing channel, `CanSee`; `Supervisor.SetSessionChannels` calls `SetUp`/`Ended` at raise and teardown (best effort). Bindings for egress are computed with the relays in 2b.

### 2a-3: `at-jam room`

- `at-jam room add|list|rename|rm` and `/admin/projects/{p}/rooms` replace `project roster add-channel|list|rm-channel`, and the admin UI project page follows.
- As built: `PutRoom` (add or rebind; a post-only room keeps its mode on an unchanged ref), `RenameRoom`, `RemoveRoom` (archive), `ListRooms`; a room's connection is a name, id or kind (default: the linear connection). The `/admin/projects/{p}/channels` routes and `project roster` are gone; `Store.AddChannel`/`RemoveChannel` remain for the roster view until 2b deletes it.

### 2b: log cutover — detailed plan: [`2026-10-06-intercom-2b-log-cutover.md`](2026-10-06-intercom-2b-log-cutover.md) (one draft PR, a commit per task)

1. intercompg `0004` (spec §3), `intercom.Store` v2, the in-memory `Log` and conformance; `LegacyStore`.
2. `Post`; `/squawks` (send, read with the grandfathered union, commit, targets); nagger; wake-on; supervisor.
3. Relays: binding-driven egress and ingress, receipts v2 with legacy fallback.
4. `/me` on channels (`channel_reads`, rail, picker, History tab); admin intercom view, studio page, search, suggestions.
5. **Project removal:** once channels carry history, `RemoveProject` can no longer cascade. Pull in 1b-3's project tombstones here, or refuse removal while channels exist. Decide when writing 2b's plan.
6. **Ticket bindings across projects:** an issue's binding goes to the first project that sets a session up on it (a manual raise with another project's issue key would take its replies). Bind only when the issue belongs to the project's tracker, or refuse the raise.
7. **No tracker:** the default send of a ticket session needs a tracker connection (the requisitioner's, else the linear one); decide the fallback when there is none.
8. Delete the `Roster.Channels` view, `RosterChannel`, the `Human` view (1a-3e) and the synthetic channel projection (kept only for the History tab).
