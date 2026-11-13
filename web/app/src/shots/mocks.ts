// The mock API that `just manual-shot` serves the app on
// (vite.shot.config.ts, build/mockApi.ts; docs/modules/ROOT/pages/manual.adoc): each key is
// "METHOD /path" (with its query, or without to match any), each value the
// JSON answer, built from the tests' fixtures. A request with no entry
// answers 404, is logged and fails the shot, naming what it still needs.
import type { ConfigBranches, Plan, SpecListRow, SpecsResponse, Tracking, TreeNode, TreeResponse } from "../admin/api/types";
import { adminBoot } from "../admin/fixtures";
import { filledContext, project } from "../admin/projects/fixtures";
import { dest, destFields, endpoint, spec, specFields } from "../admin/specs/fixtures";
import type { AgentDetail, AgentListRow, AgentsResponse, CoveRow } from "../admin/api/types";
import { agents as projectAgents, role } from "../admin/roles/fixtures";
import { emptyContext } from "../admin/projects/fixtures";
import type { Bootstrap, ChannelRow, Conversation, MessageRow } from "../api/types";
import { card } from "../test/card";

const leaf = (path: string, label: string): TreeNode => ({ path, label, group: false, badge: 0, badgeRed: false, badgeTitle: "", children: [] });
const group = (path: string, label: string, ...children: TreeNode[]): TreeNode => ({ ...leaf(path, label), group: true, children });

const acme = "config/projects/acme";
const tree: TreeResponse = {
  nodes: [
    leaf("config/tracking", "Tracking"),
    group("config/projects", "Projects", group(acme, "acme",
      group(acme + "/members", "Members", leaf(acme + "/members/usr_1", "alice")),
      group(acme + "/agents", "Agents", leaf(acme + "/agents/coder", "coder"), leaf(acme + "/agents/reviewer", "reviewer")),
      leaf(acme + "/intercom", "Intercom"),
      leaf(acme + "/escalation", "Escalation"),
    )),
    group("config/users", "Users", leaf("config/users/usr_1", "alice")),
    group("config/kits", "Kits"),
    group("config/destinations", "Destinations", leaf("config/destinations/github", "github")),
    group("config/model-specs", "Model Specs", leaf("config/model-specs/claude-default", "claude-default"), leaf("config/model-specs/review-opus", "review-opus")),
  ],
};

const branches: ConfigBranches = {
  branches: [
    { name: "main", head: "4f2a9c1e0b7d", short: "4f2a9c1", followed: true },
    { name: "jam/docs", head: "9b81d07a3c22", short: "9b81d07", followed: false },
  ],
  followed: "main", defaultName: "jam/local-admin", canPR: true, repoLink: "https://github.com/acme/jam-config",
  treeLink: "https://github.com/acme/jam-config/tree/", prNoun: "pull request",
};

// The manual's Tracking page (ROOT/configuration/config-branches): acme's
// config repo, in sync, the objects it manages, and the plan Diff shows for
// a commit that landed since the follower's last check.
const managed = (kind: string, name: string, path: string) => ({ kind, name, path, commit: "4f2a9c1", applied: "Nov 13 09:12" });
const tracking: Tracking = {
  source: { label: "https://github.com/acme/jam-config@main", repo: "https://github.com/acme/jam-config", repoLink: "https://github.com/acme/jam-config",
    dir: "", ref: "main", commit: "4f2a9c1", commitLink: "https://github.com/acme/jam-config/commit/4f2a9c1e0b7d", applied: "Nov 13 09:12" },
  rows: [
    managed("destination", "github", "config/destinations/github"),
    managed("project", "acme", acme),
    managed("role", "acme/coder", acme + "/agents/coder"),
    managed("role", "acme/reviewer", acme + "/agents/reviewer"),
    managed("user", "alice", "config/users/usr_1"),
  ],
  lastCommit: "4f2a9c1", lastApplied: "Nov 13 09:12", canGit: true, plan: null,
  follow: { state: "in-sync", head: "4f2a9c1", applied: "4f2a9c1", error: "", checked: "Nov 13 10:41" },
};
const trackingPlan: Plan = {
  source: "https://github.com/acme/jam-config@main", commit: "7c4e2d1", digest: "d1", changes: 1, unchanged: 4,
  ops: [{ action: "update", kind: "role", name: "acme/reviewer", path: acme + "/agents/reviewer", file: "projects/acme/roles/reviewer.yml",
    lines: [{ op: "…", text: "scope:" }, { op: "-", text: "  ttl: 24h" }, { op: "+", text: "  ttl: 12h" }], tooLarge: false, diff: "" }],
  forget: [], unmanaged: [{ kind: "kit", name: "default" }, { kind: "model-spec", name: "claude-default" }], kitSources: [], refusals: [],
  serve: { state: "same", keys: [] },
};

// github is the manual's destination (ROOT/configuration/destination): git,
// the REST API and GraphQL, the last with an expression and header logging.
const github = dest({
  name: "github",
  methods: ["bearer", "basic-password", "x-api-key", "raw"],
  fields: destFields({
    credName: "github-pat", note: "GitHub for the acme org: git, the REST API and GraphQL.",
    env: [{ key: "GH_HOST", template: "{host:rest}" }, { key: "GH_ENTERPRISE_TOKEN", template: "{token}" }],
    endpoints: [
      endpoint({ name: "git", subdomain: "git", route: "", upstream: "https://github.com", identityIn: "basic-password", apply: "basic-password", git: true }),
      endpoint({ name: "rest", subdomain: "gh", route: "/api/v3/", upstream: "https://api.github.com" }),
      endpoint({ name: "graphql", subdomain: "gh", route: "/api/graphql", upstream: "https://api.github.com/graphql", apply: "custom",
        applySpec: { header: "Authorization", template: "token {cred}", encoding: "raw", basicUser: "" }, logHeadersOut: "changed" }),
    ],
  }),
  env: [{ key: "GH_HOST", template: "gh.jam.example.com" }, { key: "GH_ENTERPRISE_TOKEN", template: "{token}" }], gitRouted: true,
  uses: [{ project: "acme", role: "coder", cred: "", credIsRole: false }, { project: "acme", role: "reviewer", cred: "github-review", credIsRole: true }],
});

// The manual's agents and roles (ROOT/configuration/roles and
// ROOT/configuration/agents): acme's coder and reviewer roles, a standing
// reviewer, alice's personal session, a ticket session and an enrolled bot.
const agentRow = (r: Partial<AgentListRow>): AgentListRow => ({ id: "", name: "", kind: "manual", project: "acme", role: "coder", unit: "", status: "running",
  connector: "ok", image: "ok", lastSeen: "Nov 12 10:42:07", moreGrants: 0, hasStudio: true, attn: null, ...r });
const agentRows: AgentListRow[] = [
  agentRow({ id: "ci-bot", kind: "enrolled", role: "reviewer", status: "pending", connector: "", image: "", lastSeen: "", hasStudio: false }),
  agentRow({ id: "ses_01jc2b8k4m", name: "reviewer-bot", kind: "standing", role: "reviewer", status: "waiting", image: "stale" }),
  agentRow({ id: "ses_01jc3d9q7r", name: "coder-01", kind: "personal", status: "running" }),
  agentRow({ id: "ses_01jc4f2t6w", name: "acme-42", kind: "ticket", unit: "ACME-42", status: "idled", lastSeen: "Nov 12 09:15:31" }),
  agentRow({ id: "ses_01jc5h3v8y", name: "acme-57", kind: "ticket", unit: "ACME-57", status: "setting up", connector: "", lastSeen: "" }),
];
const agentFilters = [{ key: "", value: "All" }, { key: "running", value: "Running" }, { key: "waiting", value: "Waiting" },
  { key: "setting-up", value: "Setting up" }, { key: "idled", value: "Idled" }, { key: "attention", value: "Lost / terminating" }];
const agentList: AgentsResponse = { agents: agentRows, filters: agentFilters, status: "", canEdit: true };
const coveRow = (a: AgentListRow): CoveRow => ({ id: a.id, name: a.name, project: a.project, role: a.role, unit: a.unit, status: a.status, connector: a.connector,
  image: a.image, manager: "default", leaseHolder: "jam-1", raisedAt: "Nov 12 08:30", lastSeen: a.lastSeen.slice(7), attn: null });
const reviewerBot = agentRows[1]!;
const roleContext = { ...emptyContext, scope: "role", showResources: false, budget: 1200, empty: false, coreBytes: 92,
  core: "You review pull requests on `acme/app`. Approve only when the gate is green and the change matches its ticket." };
const reviewer = role({
  name: "reviewer", kit: "review-kit", modelSpec: "claude-default", modelSpecDefault: true, ttl: "24h",
  dests: [{ name: "anthropic", cred: "anthropic-key", credIsRole: false, route: "", missing: false },
    { name: "github", cred: "github-review", credIsRole: true, route: "", missing: false }],
  addressing: ["user:*", "channel:eng"], egressManaged: true, egressDomains: ["registry.npmjs.org", ".golang.org"],
  allocation: [
    { label: "Max ephemeral agents", value: "4", unset: false },
    { label: "Max personal agents", value: "3", unset: false },
    { label: "Max personal per owner", value: "1", unset: false },
    { label: "Idle after", value: "default (4h)", unset: true },
    { label: "Nag every", value: "default (24h)", unset: true },
    { label: "Reclaim after", value: "72h", unset: false },
    { label: "Idle timeout", value: "none", unset: true },
    { label: "On idle", value: "wake", unset: false },
    { label: "Alarm time zone", value: "UTC", unset: false },
  ],
  standing: [{ name: "reviewer-bot", prompt: "Review every pull request on acme/app as it opens.", actorId: reviewerBot.id, status: "waiting", image: "stale", upgrade: "" }],
  holders: [{ id: "ci-bot", override: false }, { id: reviewerBot.id, override: false }],
  running: [coveRow(reviewerBot)],
  form: { destinations: "anthropic,github=github-review", addressing: "user:*,channel:eng", ttl: "24h", kit: "review-kit", modelSpec: "",
    maxEphemeral: "4", maxPersonal: "3", maxPersonalPerOwner: "1", idleAfter: "", nagEvery: "", reclaimAfter: "72h", egress: "registry.npmjs.org\n.golang.org" },
  context: roleContext, canRequest: true, setupEnabled: true,
});
const acmeAgents = projectAgents({
  roles: [
    { name: "coder", destinations: [{ name: "anthropic", cred: "" }, { name: "github", cred: "" }], ttl: "24h", kit: "", attn: null },
    { name: "reviewer", destinations: [{ name: "anthropic", cred: "" }, { name: "github", cred: "github-review" }], ttl: "24h", kit: "review-kit", attn: null },
  ],
  agents: agentRows.map((a) => ({ id: a.id, name: a.name, kind: a.kind, unit: a.unit, status: a.status, image: a.image,
    lastSeen: a.lastSeen, roles: [a.role], attn: null })),
});
const coder01: AgentDetail = {
  id: "ses_01jc3d9q7r", running: true, status: "running", kind: "personal", owner: "alice", ownerId: "usr_1", name: "coder-01", project: "acme", role: "coder", unit: "",
  hasActor: true, grants: [{ project: "acme", role: "coder", destinations: ["anthropic", "github"] }], tokenExpiry: "Nov 13 08:30",
  raised: "Nov 12 08:30", lastSeen: "Nov 12 10:42:07", lease: "jam-1", backend: "docker", location: "default", waitingSince: "", waitSeq: 0,
  escalationTier: 0, tierPingedAt: "", escalationCategory: "", nags: 0, lastNagAt: "", commitSeq: 18, egressId: "kit:9f3a1c", egressKnown: true, egressFailures: 0,
  access: { enabled: true, requester: { id: "usr_1", name: "alice" }, membersPreview: "members", membersSsh: "none", roleProject: "acme", roleName: "coder",
    shares: [{ userId: "usr_2", user: "carol", rights: ["ssh"], expiresAt: "2026-11-12 18:00 UTC", expired: false, grantedBy: "op:alice" }],
    live: [{ name: "alice", right: "preview", since: "10:12" }], previewsOn: true,
    previews: [{ name: "web", port: 5173, url: "https://web-ses-01jc3d9q7r.preview.jam.example.com/" }] },
  sessionsEnabled: true, streams: [{ streamId: "3c7e1a90b2d44f6e", started: "Nov 12 08:31", lastEvent: "Nov 12 10:42:07", events: 412 }],
  squawksConfigured: true, squawksMore: false, canEdit: true, setupEnabled: true,
  squawks: [
    { at: "Nov 12 10:40", from: "coder-01", fromId: "ses_01jc3d9q7r", agentId: "ses_01jc3d9q7r", channel: "coder-01", channelId: "chn_7", project: "acme",
      body: "<p>The login form is fixed; the preview <code>web</code> shows it. Shall I open the PR?</p>", plain: false, to: [] },
    { at: "Nov 12 08:31", from: "alice", fromId: "usr_1", agentId: "", channel: "coder-01", channelId: "chn_7", project: "acme",
      body: "<p>The login form loses its focus after an error. Please fix it.</p>", plain: false, to: [] },
  ],
};

// The manual's model specs (ROOT/concepts/model-spec and
// ROOT/configuration/model-specs): the seeded claude-default, which acme's
// roles run, and review-opus, a spec with a pinned model, a policy and a
// header rule that no role runs yet.
const specChoices = { types: ["claude"], principals: ["anthropic-key", "solo-sub"], modes: ["default", "acceptEdits", "bypassPermissions", "dontAsk"],
  providers: ["anthropic", "vertex", "bedrock"] };
const defaultSettings = ["agentPushNotifEnabled", "alwaysThinkingEnabled", "disableAgentView", "inputNeededNotifEnabled", "prefersReducedMotion",
  "showThinkingSummaries", "showTurnDuration", "spinnerTipsEnabled", "theme"];
const claudeDefault = spec({
  name: "claude-default", choices: specChoices,
  fields: specFields({ version: "2.1.322", principal: "anthropic-key", mode: "bypassPermissions",
    note: "Seeded by Jam: the built-in Claude Code defaults every unbound role runs under.", plugins: "superpowers@claude-plugins-official",
    settings: JSON.stringify(Object.fromEntries(defaultSettings.map((k) => [k, k === "theme" ? "dark" : k !== "spinnerTipsEnabled"])), null, 2) }),
  plugins: ["superpowers@claude-plugins-official"], settingsKeys: defaultSettings,
  uses: [{ project: "acme", role: "coder", implicit: true }, { project: "acme", role: "reviewer", implicit: true }],
});
const reviewOpus = spec({
  name: "review-opus", choices: specChoices,
  fields: specFields({ version: "2.1.322", versionConstraint: "2.x", principal: "solo-sub", modelId: "claude-opus-5-5", effort: "high", mode: "dontAsk",
    headers: "anthropic-beta += oauth-2025-04-20", allow: "Bash(gh pr *)\nBash(go test *)\nRead", deny: "WebFetch",
    plugins: "superpowers@claude-plugins-official", note: "For reviewers: Opus at high effort, read-only tools." }),
  headerRules: [{ name: "anthropic-beta", op: "+=", value: "oauth-2025-04-20" }],
  allow: ["Bash(gh pr *)", "Bash(go test *)", "Read"], deny: ["WebFetch"], plugins: ["superpowers@claude-plugins-official"], uses: [],
});
const specRow = (s: typeof claudeDefault): SpecListRow => ({ name: s.name, type: s.fields.type, version: s.fields.version, versionConstraint: s.fields.versionConstraint,
  principal: s.fields.principal, modelId: s.fields.modelId, effort: s.fields.effort, mode: s.fields.mode, provider: s.fields.provider, usedBy: s.uses.length });
const specList: SpecsResponse = { specs: [claudeDefault, reviewOpus].map(specRow), new: specFields({ version: "", principal: "" }), choices: specChoices };

// The Inbox (ROOT/inbox/channels, ROOT/inbox/composer): alice's rail in acme,
// with the ops room open, and whom she may start a conversation with.
const userBoot: Bootstrap = {
  brand: "Acme Jam", user: { id: "usr_1", name: "alice", initial: "A" }, roles: "member", admin: false, signOut: true,
  tabs: [{ key: "inbox", label: "Inbox", href: "/app/user/", badge: 3, badgeRed: false, badgeTitle: "" }],
  attachMax: 25 << 20, pushKey: "", boards: false, pushFocus: false, newSession: false,
};
const row = (over: Partial<ChannelRow>): ChannelRow => ({
  id: "", label: "", project: "acme", kind: "studio", needsYou: false, phase: "", unread: 0, openAsks: 0,
  lastSeq: 0, selected: false, mission: "", session: "", ssh: false, ...over,
});
const rail = {
  unreadTotal: 3,
  groups: [
    { bucket: "needs-you", title: "Needs you", rows: [
      row({ id: "chn_acme42", label: "ACME-42", needsYou: true, phase: "live", openAsks: 1, unread: 1, lastSeq: 90,
        card: card({ session: "ses_spider", name: "spider", state: "idle", pill: "needs you", tool: "", mission: "Fix the flaky login test", counts: { asks: 1, alarms: 0, previews: 0, processes: 0, subagents: 0 } }) }),
    ] },
    { bucket: "active", title: "Sessions", rows: [
      row({ id: "chn_helper", label: "helper", phase: "live", lastSeq: 88,
        card: card({ session: "ses_helper", name: "helper", mission: "Draft the 1.14 release notes", focus: "Reading the changelog" }) }),
      row({ id: "chn_bobcoder", label: "bob, coder", kind: "dm", phase: "live", lastSeq: 80, mission: "Review open pull requests" }),
    ] },
    { bucket: "channels", title: "Channels", rows: [
      row({ id: "chn_ops", label: "ops", kind: "named", lastSeq: 86, selected: true }),
      row({ id: "chn_bob", label: "bob", kind: "dm", unread: 2, lastSeq: 84 }),
      row({ id: "chn_acme37", label: "ACME-37", lastSeq: 40 }),
    ] },
  ],
};
const msg = (over: Partial<MessageRow>): MessageRow => ({
  from: "", body: "", raw: "", plain: false, at: "", time: "", mine: false, seq: 0, system: false, sysText: "", card: "", ...over,
});
const ops: Conversation = {
  channelId: "chn_ops", label: "ops", kind: "named", project: "acme", phase: "", needsYou: false, sendTo: "chn_ops",
  lastSeq: 86, earlier: false, sessionIds: [], sessions: [], openAsks: [], newBoard: false, session: "",
  canJoin: false, canLeave: true,
  callIn: [{ to: "user:usr_3", label: "carol", kind: "person", project: "" }, { to: "user:usr_4", label: "dan", kind: "person", project: "" }],
  messages: [
    msg({ seq: 81, from: "bob", time: "2026-11-10T09:12:00Z", body: "<p>Release 1.14 is tagged. Can someone check the deploy log before we announce it?</p>", raw: "Release 1.14 is tagged. Can someone check the deploy log before we announce it?" }),
    msg({ seq: 83, from: "coder", color: "#2f9e44", time: "2026-11-10T09:20:00Z",
      body: "<p>The deploy finished. One warning, from the cache warm-up:</p><pre><code>WARN cache: 3 keys missed on warm-up\n</code></pre>",
      raw: "The deploy finished. One warning, from the cache warm-up:\n\n```\nWARN cache: 3 keys missed on warm-up\n```",
      files: [{ id: "blb_deploylog", name: "deploy-1.14.log", size: "48 KB", image: false }] }),
    msg({ seq: 84, system: true, sysText: "alice called in carol", time: "2026-11-10T09:24:00Z" }),
    msg({ seq: 86, from: "alice", mine: true, time: "2026-11-10T09:26:00Z", body: "<p>Thanks. That warning is known; I'll post the announcement.</p>", raw: "Thanks. That warning is known; I'll post the announcement." }),
  ],
};
// ACME-37's ticket conversation: alice can see it but isn't in it.
const acme37: Conversation = {
  ...ops, channelId: "chn_acme37", label: "ACME-37", kind: "studio", sendTo: "chn_acme37", lastSeq: 40,
  canJoin: true, canLeave: false, callIn: [],
  messages: [msg({ seq: 40, from: "reviewer", time: "2026-11-09T16:02:00Z", body: "<p>Merged: the retry fix for the webhook relay.</p>", raw: "Merged: the retry fix for the webhook relay." })],
};
const recipients = {
  options: [
    { to: "user:usr_2", label: "bob", kind: "person", project: "acme" },
    { to: "user:usr_3", label: "carol", kind: "person", project: "acme" },
    { to: "session:ses_helper", label: "helper", kind: "agent", project: "acme" },
    { to: "session:ses_spider", label: "spider", kind: "agent", project: "acme" },
    { to: "chn_acme42", label: "ACME-42", kind: "ticket", project: "acme" },
    { to: "chn_ops", label: "ops", kind: "room", project: "acme" },
    { to: "user:usr_5", label: "erin", kind: "person", project: "website" },
    { to: "chn_launch", label: "launch", kind: "room", project: "website" },
  ],
};

export const mocks: Record<string, unknown> = {
  "GET /app/admin/api/bootstrap": adminBoot,
  "GET /app/admin/api/tree": tree,
  "GET /app/admin/api/config/branches": branches,
  "GET /app/admin/api/config/branches/pr": { url: "" },
  "GET /app/admin/api/tracking": tracking,
  "POST /app/admin/api/tracking/diff": trackingPlan,
  "GET /app/admin/api/projects/acme": project({ chatService: "disc", context: filledContext }),
  "GET /app/admin/api/destinations/github": github,
  "GET /app/admin/api/agents": agentList,
  "GET /app/admin/api/agents/ses_01jc3d9q7r": coder01,
  "GET /app/admin/api/projects/acme/agents": acmeAgents,
  "GET /app/admin/api/projects/acme/roles/reviewer": reviewer,
  "GET /app/admin/api/model-specs": specList,
  "GET /app/admin/api/model-specs/claude-default": claudeDefault,
  "GET /app/admin/api/model-specs/review-opus": reviewOpus,
  "GET /app/user/api/bootstrap": userBoot,
  "GET /app/user/api/rail": rail,
  "GET /app/user/api/recipients": recipients,
  "GET /app/user/api/conversations/chn_ops": ops,
  "GET /app/user/api/conversations/chn_ops/presence": { sessions: [] },
  "POST /app/user/api/conversations/chn_ops/read": {},
  "GET /app/user/api/conversations/chn_acme37": acme37,
  "GET /app/user/api/conversations/chn_acme37/presence": { sessions: [] },
  "POST /app/user/api/conversations/chn_acme37/read": {},
};
