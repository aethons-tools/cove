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
import type { AppTokensResponse, AskCard, Bootstrap, ChannelRow, Conversation, MessageRow, SessionHead, SessionPane, ThinkingEvent } from "../api/types";
import type { EventStream } from "../../build/mockApi";
import { card } from "../test/card";
import { draftPage, draftView, kit, kitView } from "../admin/kits/fixtures";
import type { KitsResponse } from "../admin/api/types";
import type { ChannelLog, Dashboard, Health, SearchResult } from "../admin/api/types";
import type { ChainRow, MemberRow, ProjectsResponse, SquawkRow, UserDetail, UserRow, UsersResponse } from "../admin/api/types";
import boardRelease from "./board-release.json";
import boardReleaseThumb from "./board-release-thumb.png?inline";

const leaf = (path: string, label: string): TreeNode => ({ path, label, group: false, badge: 0, badgeRed: false, badgeTitle: "", children: [] });
const group = (path: string, label: string, ...children: TreeNode[]): TreeNode => ({ ...leaf(path, label), group: true, children });

// badged is a node with one config item at or under it (the dashboard page,
// ROOT/configuration/dashboard: acme's infra chain names user:sre, who is
// nobody there).
const badged = (n: TreeNode): TreeNode => ({ ...n, badge: 1, badgeTitle: "1 config" });

const acme = "config/projects/acme";
const projectNode = (name: string, base = "config/projects/" + name) => group(base, name,
  group(base + "/members", "Members"), group(base + "/agents", "Agents"), leaf(base + "/intercom", "Intercom"), leaf(base + "/escalation", "Escalation"));
const tree: TreeResponse = {
  nodes: [
    leaf("config/tracking", "Tracking"),
    badged(group("config/projects", "Projects", badged(group(acme, "acme",
      group(acme + "/members", "Members", leaf(acme + "/members/usr_1", "alice")),
      group(acme + "/agents", "Agents", leaf(acme + "/agents/coder", "coder"), leaf(acme + "/agents/reviewer", "reviewer")),
      leaf(acme + "/intercom", "Intercom"),
      badged(leaf(acme + "/escalation", "Escalation")),
    )), projectNode("default"), projectNode("website"))),
    group("config/users", "Users", leaf("config/users/usr_1", "alice"), leaf("config/users/usr_2", "bob"), leaf("config/users/usr_3", "carol")),
    group("config/kits", "Kits", ...["default", "go-tools", "review-kit", "web-dev"].map((k) => leaf("config/kits/" + k, k))),
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
  tabs: [{ key: "inbox", label: "Inbox", href: "/app/user/", badge: 0, badgeRed: false, badgeTitle: "" }],
  attachMax: 25 << 20, pushKey: "BKmanual-shot-vapid-key", boards: true, pushFocus: false, newSession: false,
};
const row = (over: Partial<ChannelRow>): ChannelRow => ({
  id: "", label: "", project: "acme", kind: "work", needsYou: false, phase: "", unread: 0, openAsks: 0,
  lastSeq: 0, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const rail = {
  unreadTotal: 3,
  groups: [
    { bucket: "needs-you", title: "Needs you", rows: [
      row({ id: "chn_acme42", label: "ACME-42", needsYou: true, phase: "live", openAsks: 1, unread: 1, lastSeq: 90, mission: "Fix the flaky login test" }),
    ] },
    { bucket: "active", title: "Agents", rows: [
      row({ id: "chn_helperdm", label: "helper", kind: "dm", phase: "live", lastSeq: 88, sessionView: "chn_helper",
        card: card({ session: "ses_helper", name: "helper", mission: "Draft the 1.14 release notes", focus: "Reading the changelog" }) }),
      row({ id: "chn_bobcoder", label: "bob, coder", kind: "group", phase: "live", lastSeq: 80, mission: "Review open pull requests" }),
    ] },
    { bucket: "channels", title: "Conversations", rows: [
      row({ id: "chn_ops", label: "ops", kind: "room", lastSeq: 86, selected: true }),
      row({ id: "chn_bob", label: "bob", kind: "dm", unread: 2, lastSeq: 84 }),
      row({ id: "chn_acme37", label: "ACME-37", lastSeq: 40 }),
    ] },
  ],
};
const msg = (over: Partial<MessageRow>): MessageRow => ({
  from: "", body: "", raw: "", plain: false, at: "", time: "", mine: false, seq: 0, system: false, sysText: "", card: "", ...over,
});
const ops: Conversation = {
  channelId: "chn_ops", label: "ops", kind: "room", project: "acme", phase: "", needsYou: false, sendTo: "chn_ops",
  lastSeq: 86, earlier: false, sessionIds: [], sessions: [], openAsks: [], newBoard: false, session: "",
  canJoin: false, canLeave: true,
  callIn: [{ to: "user:usr_3", label: "carol", kind: "person", project: "" }, { to: "user:usr_4", label: "dan", kind: "person", project: "" },
    { to: "session:ses_helper", label: "helper", kind: "agent", project: "" }],
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
// ACME-37's work log: alice can see it but isn't in it.
const acme37: Conversation = {
  ...ops, channelId: "chn_acme37", label: "ACME-37", kind: "work", sendTo: "chn_acme37", lastSeq: 40,
  canJoin: true, canLeave: false, callIn: [],
  messages: [msg({ seq: 40, from: "reviewer", time: "2026-11-09T16:02:00Z", body: "<p>Merged: the retry fix for the webhook relay.</p>", raw: "Merged: the retry fix for the webhook relay." })],
};
const recipients = {
  options: [
    { to: "user:usr_2", label: "bob", kind: "person", project: "acme" },
    { to: "user:usr_3", label: "carol", kind: "person", project: "acme" },
    { to: "session:ses_helper", label: "helper", kind: "agent", project: "acme" },
    { to: "session:ses_spider", label: "spider", kind: "agent", project: "acme" },
    { to: "chn_acme42", worker: "worker:chn_acme42", label: "ACME-42", kind: "work", project: "acme" },
    { to: "chn_acme37", worker: "worker:chn_acme37", label: "ACME-37", kind: "work", project: "acme" },
    { to: "chn_ops", label: "ops", kind: "room", project: "acme" },
    { to: "user:usr_5", label: "erin", kind: "person", project: "website" },
    { to: "chn_launch", label: "launch", kind: "room", project: "website" },
  ],
};

// The manual's kits (ROOT/concepts/kit and ROOT/configuration/kits): the
// seeded default kit, web-dev with its own Dockerfile, review-kit built on
// web-dev (the reviewer role's kit, a draft open on jam/docs), and go-tools,
// imported from its own git repo and tracked.
const kitList: KitsResponse = {
  canSource: true,
  kits: [
    { name: "default", current: 1, count: 1, base: "blessed default", egress: 3, usedBy: 1, invalid: false, draft: false, tracked: false, attn: null },
    { name: "go-tools", current: 2, count: 2, base: "Dockerfile", egress: 4, usedBy: 0, invalid: false, draft: false, tracked: true, attn: null },
    { name: "review-kit", current: 4, count: 4, base: "kit: web-dev", egress: 1, usedBy: 1, invalid: false, draft: true, tracked: false, attn: null },
    { name: "web-dev", current: 3, count: 3, base: "Dockerfile", egress: 3, usedBy: 0, invalid: false, draft: false, tracked: false, attn: null },
  ],
};
const reviewYaml = (prompt: string) => `kind: studio\nbase:\n  kit: web-dev\negress: [api.github.com]\nprompt: ${prompt}\n`;
const reviewPrompt = "You review pull requests on acme/app. Read the ticket first.";
const reviewView = kitView({
  n: 4, pushText: reviewYaml(reviewPrompt), yaml: reviewYaml(reviewPrompt), base: "kit: web-dev",
  egress: ["api.github.com"], ceiling: ["api.github.com"], prompt: reviewPrompt, digest: "7d0c41e9b2a5",
});
const webDevEgress = ["github.com", "proxy.golang.org", "registry.npmjs.org"];
const kitVersion = (n: number, pushed: string, digest: string, current = false) => ({ n, prev: n - 1, current, digest, pushed, source: null });
const reviewKit = kit({
  name: "review-kit", current: 4,
  versions: [kitVersion(1, "2026-10-02 14:10", "51a9e0c3d7f2"), kitVersion(2, "2026-10-19 09:42", "51a9e0c3d7f2"),
    kitVersion(3, "2026-11-04 16:05", "c3e88a10f6b4"), kitVersion(4, "2026-11-12 11:20", "7d0c41e9b2a5", true)],
  view: reviewView,
  uses: [{ project: "acme", role: "reviewer", implicit: false }],
  chain: [{ name: "web-dev", version: 3, follow: true }],
  effective: kitView({
    n: 4, base: "kit: web-dev", egress: ["api.github.com", ...webDevEgress], ceiling: ["api.github.com", ...webDevEgress],
    buildArgs: [{ key: "GO_VERSION", value: "1.25" }, { key: "NODE_VERSION", value: "22" }],
    secrets: [{ key: "GH_TOKEN", value: "clone and review access" }],
    mcpServers: [{ key: "linear", value: "http ${LINEAR_MCP_URL}" }],
    notes: [{ key: "review.md", value: "you review a pull request" }, { key: "web.md", value: "you run the web app" }],
    prompt: "You work on acme's web services.\n\n" + reviewPrompt, digest: "7d0c41e9b2a5",
  }),
  files: { n: 4, size: "1.2 KiB", rows: [{ path: "kit.yml", label: "kit.yml", mode: "0644", size: 131 }, { path: "notes/review.md", label: "notes/review.md", mode: "0644", size: 1102 }], hidden: 0 },
});
const reviewDiff = kit({
  ...reviewKit,
  diff: { from: 3, to: 4, digestChanged: true, tooLarge: false, lines: [
    { op: " ", text: "kind: studio" }, { op: " ", text: "base:" }, { op: " ", text: "  kit: web-dev" },
    { op: "+", text: "egress: [api.github.com]" }, { op: "-", text: "prompt: You review pull requests on acme/app." }, { op: "+", text: "prompt: " + reviewPrompt },
  ] },
  filesDiff: { from: 3, to: 4, rows: [{ change: "modified", path: "kit.yml", label: "kit.yml", mode: "0644", size: "84 B → 131 B" },
    { change: "added", path: "notes/review.md", label: "notes/review.md", mode: "0644", size: "1102 B" }], more: 0, unchanged: 0 },
});
const reviewDockerfile = "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\nRUN apt-get update && apt-get install -y --no-install-recommends shellcheck\n";
const reviewDraft = draftPage({
  name: "review-kit", current: 4, versions: [1, 2, 3, 4],
  draft: draftView({
    base: 4, current: 4, etag: "t9", actor: "alice", updated: "2026-11-13 09:50", size: "1.3 KiB",
    tree: [
      { depth: 0, name: "Dockerfile", dir: false, path: "Dockerfile", label: "Dockerfile", mode: "0644", size: 120, change: "added" },
      { depth: 0, name: "kit.yml", dir: false, path: "kit.yml", label: "kit.yml", mode: "0644", size: 131, change: "" },
      { depth: 0, name: "notes", dir: true, path: "", label: "notes", mode: "", size: 0, change: "" },
      { depth: 1, name: "review.md", dir: false, path: "notes/review.md", label: "notes/review.md", mode: "0644", size: 1102, change: "" },
    ],
    removed: [],
    editor: { path: "Dockerfile", label: "Dockerfile", mode: "0644", size: 120, exec: false, etag: "t9", editable: true, text: reviewDockerfile, invisible: false, note: "" },
    changes: { from: 4, to: 0, rows: [{ change: "added", path: "Dockerfile", label: "Dockerfile", mode: "0644", size: "120 B" }], more: 0, unchanged: 2 },
  }),
});
const goToolsSource = { repo: "https://github.com/acme/kits", repoLink: "https://github.com/acme/kits", dir: "go", ref: "main",
  commit: "0123456789ab", commitLink: "https://github.com/acme/kits/commit/0123456789ab" };
const goToolsYaml = "kind: studio\negress: [proxy.golang.org, sum.golang.org, go.dev, github.com]\nbuild-args: {GO_VERSION: \"1.25\"}\nprompt: You build and test Go code.\n";
const goTools = kit({
  name: "go-tools", current: 2,
  versions: [{ n: 1, prev: 0, current: false, digest: "e4f1a07c9d32", pushed: "2026-10-21 08:00", source: { ...goToolsSource, commit: "9f8e7d6c5b4a", commitLink: "https://github.com/acme/kits/commit/9f8e7d6c5b4a" } },
    { n: 2, prev: 1, current: true, digest: "b2d93c5e18f0", pushed: "2026-11-10 15:31", source: goToolsSource }],
  view: kitView({ n: 2, pushText: goToolsYaml, yaml: goToolsYaml, base: "Dockerfile", egress: ["github.com", "go.dev", "proxy.golang.org", "sum.golang.org"],
    ceiling: ["github.com", "go.dev", "proxy.golang.org", "sum.golang.org"], buildArgs: [{ key: "GO_VERSION", value: "1.25" }], prompt: "You build and test Go code.", digest: "b2d93c5e18f0" }),
  uses: [], source: goToolsSource,
  files: { n: 2, size: "640 B", rows: [{ path: "Dockerfile", label: "Dockerfile", mode: "0644", size: 402 }, { path: "kit.yml", label: "kit.yml", mode: "0644", size: 138 }], hidden: 0 },
});

// The session view (ROOT/inbox/session): helper, alice's personal session in
// acme, at work on the 1.14 release notes, with its header and each tab of
// its pane.
const helperId = "ses_helper";
const helperColor = "#1c7ed6";
const helperConv: Conversation = {
  ...ops, channelId: "chn_helper", label: "helper", kind: "session", sendTo: "chn_helper", lastSeq: 88,
  canJoin: false, canLeave: false, callIn: [], session: helperId, color: helperColor,
  sessionIds: [helperId], sessions: [{ label: "helper", text: "is running", tool: "Bash", class: "busy" }],
  messages: [
    msg({ seq: 85, from: "alice", mine: true, time: "2026-11-10T10:02:00Z",
      body: "<p>Please draft the release notes for 1.14 from the changelog, and show me a preview.</p>",
      raw: "Please draft the release notes for 1.14 from the changelog, and show me a preview." }),
    msg({ seq: 87, from: "helper", color: helperColor, time: "2026-11-10T10:09:00Z",
      body: "<p>The first draft is up at the <code>notes</code> preview. It covers <a href=\"https://github.com/acme/app/pull/57\">acme/app#57</a> and <a href=\"https://github.com/acme/app/pull/61\">acme/app#61</a>.</p>",
      raw: "The first draft is up at the `notes` preview. It covers https://github.com/acme/app/pull/57 and https://github.com/acme/app/pull/61.",
      files: [{ id: "blb_notes", name: "release-notes-1.14.md", size: "6 KB", image: false }] }),
    msg({ seq: 88, from: "alice", mine: true, time: "2026-11-10T10:12:00Z",
      body: "<p>Good. Add the upgrade steps for the cache change too.</p>", raw: "Good. Add the upgrade steps for the cache change too." }),
  ],
};
const helperHead: SessionHead = {
  id: helperId, label: "helper", channel: "chn_helper", kind: "personal", color: helperColor, ticket: "", role: "coder", project: "acme", owner: "alice",
  state: { label: "helper", text: "is running", tool: "Bash", class: "busy" }, since: "2m", openAsks: 0,
  cost: "$1.84", turnCost: "$0.21", tokens: "412,305 in / 18,940 out tokens",
  context: "ctx 84k / 200k · 42%", contextTip: "context: 84,112 of 200,000 tokens (claude-opus-5-5)", uptime: "1h 12m",
  mission: "Draft the 1.14 release notes", card: card({ session: helperId, name: "helper", color: helperColor, mission: "Draft the 1.14 release notes", focus: "Adding the upgrade steps" }),
  ssh: "jam ssh ses_helper", canRelease: true, canRestart: false, canEnd: false, canUpgrade: false, ops: true,
  canInterrupt: true, canCompact: true, canClear: false, restartLabel: "Restart", upgradeState: "",
};
const helperPane = (tab: string, over: Partial<SessionPane>): SessionPane => ({
  id: helperId, tab, previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], streams: [], eventsOff: false, ...over,
});
const stream = "4b1e7c20a9d35f18";
const at = (hms: string) => Date.parse(`2026-11-10T${hms}Z`);
const ev = (seq: number, hms: string, over: Partial<ThinkingEvent>): ThinkingEvent => ({
  stream, seq, turn: 3, at: at(hms), kind: "text", tool: "", toolUseId: "", text: "", detail: "", detailTruncated: 0, isError: false,
  truncatedBytes: 0, costUsd: 0, durationMs: 0, claudeSession: "c1", ...over,
});
const helperEvents: ThinkingEvent[] = [
  ev(201, "10:12:04", { kind: "prompt", text: "alice: Good. Add the upgrade steps for the cache change too." }),
  ev(202, "10:12:07", { kind: "thinking", text: "The cache change is #61. I need its upgrade notes: check the PR's description and the migration it adds." }),
  ev(203, "10:12:09", { kind: "tool_call", tool: "Bash", toolUseId: "t1", text: "gh pr view 61 --json body -q .body", detail: "gh pr view 61 --json body -q .body" }),
  ev(204, "10:12:11", { kind: "tool_result", tool: "Bash", toolUseId: "t1", text: "Moves the cache to Redis…", detail: "Moves the cache to Redis.\n\nUpgrade: set CACHE_URL before you restart." }),
  ev(205, "10:12:14", { kind: "tool_call", tool: "Read", toolUseId: "t2", text: "docs/upgrade.md", detail: "docs/upgrade.md" }),
  ev(206, "10:12:14", { kind: "tool_result", tool: "Read", toolUseId: "t2", text: "# Upgrading…", detail: "# Upgrading\n\n…" }),
  ev(207, "10:12:20", { kind: "text", text: "I'll add an Upgrading section with the CACHE_URL step." }),
  ev(208, "10:12:22", { kind: "tool_call", tool: "Edit", toolUseId: "t3", text: "notes/1.14.md", detail: "notes/1.14.md" }),
  ev(209, "10:12:23", { kind: "tool_result", tool: "Edit", toolUseId: "t3", text: "ok", detail: "The file notes/1.14.md has been updated." }),
  ev(210, "10:12:25", { kind: "tool_call", tool: "Bash", toolUseId: "t4", text: "npm run build --prefix notes", detail: "npm run build --prefix notes" }),
];
const helperThinking: EventStream = { eventStream: [
  { event: "stream", data: stream },
  ...helperEvents.map((e) => ({ event: "ev", data: JSON.stringify(e) })),
  { event: "more", data: "201" },
] };

// The manual's projects and users (ROOT/concepts/project,
// ROOT/concepts/escalation, ROOT/configuration/projects and
// ROOT/configuration/users): acme with three members, two rooms, a default
// escalation chain and an infra chain that names someone who isn't a member.
const member = (userId: string, name: string, handle: string, inbox: string): MemberRow => ({ userId, name, handle,
  delivery: inbox ? [{ service: "discord", address: inbox }] : [], deliverySpec: inbox ? "discord:" + inbox : "" });
const acmeMembers = [member("usr_1", "alice", "alice", "1188201945"), member("usr_2", "bob", "bob", ""), member("usr_3", "carol", "carol", "1188202271")];
const chainRow = (category: string, tiers: [string[], string][], unknown: string[] = []): ChainRow => ({ category,
  tiers: tiers.map(([targets, timeout]) => ({ targets: targets.map((text) => ({ text, unknown: unknown.includes(text) })), timeout })),
  spec: tiers.map(([targets, timeout]) => targets.join(",") + "@" + timeout).join("\n") });
const acmeDefaultChain = chainRow("", [[["user:alice", "user:bob"], "15m"], [["user:carol"], "1h"]]);
const acmeChains = [acmeDefaultChain, chainRow("infra", [[["user:carol", "user:sre"], "10m"]], ["user:sre"])];
const acmeProject = project({ chatService: "disc", context: filledContext, members: acmeMembers, roles: 2, agents: 5, liveAgents: 3,
  rooms: [{ name: "eng", connection: "disc", kind: "discord", ref: "1188190033", postOnly: false },
    { name: "ops", connection: "acme-linear", kind: "linear", ref: "ACME-1", postOnly: false }],
  escalation: acmeChains, defaultChain: acmeDefaultChain, inUseBy: "3 member(s)" });
const squawk = (at: string, from: string, fromId: string, channel: string, body: string): SquawkRow => ({ at, from, fromId,
  agentId: fromId.startsWith("ses_") ? fromId : "", channel, channelId: "chn_" + channel, project: "acme", body, plain: false, to: [] });
const acmeSquawks = [
  squawk("Nov 12 10:44", "acme-42", "ses_01jc4f2t6w", "acme-42", "<p>Needs input from acme-42 on ACME-42 — called in alice, bob (escalation tier 0).</p>"),
  squawk("Nov 12 10:42", "acme-42", "ses_01jc4f2t6w", "acme-42", "<p>The migration needs a maintenance window. Which night suits you?</p>"),
  squawk("Nov 12 10:40", "coder-01", "ses_01jc3d9q7r", "coder-01", "<p>The login form is fixed; the preview <code>web</code> shows it. Shall I open the PR?</p>"),
  squawk("Nov 12 09:58", "carol", "usr_3", "ops", "<p>Deploy of <code>v1.8.2</code> is done.</p>"),
];
const projectList: ProjectsResponse = { projects: [{ name: "acme", members: 3, roles: 2, rooms: 2 }, { name: "default", members: 0, roles: 0, rooms: 0 },
  { name: "website", members: 1, roles: 1, rooms: 0 }] };
const userRow = (id: string, name: string, logins: string[], accounts: UserRow["accounts"], projects: string[]): UserRow => ({ id, name, logins, accounts, projects });
const aliceUser = userRow("usr_1", "alice", ["auth0|6512a9e0"], [{ id: "acc_1", connection: "acme-linear", serviceUid: "", handle: "alice" },
  { id: "acc_2", connection: "disc", serviceUid: "801234567890123456", handle: "" }], ["acme", "website"]);
const userList: UsersResponse = { users: [aliceUser,
  userRow("usr_2", "bob", ["auth0|6512aa14"], [{ id: "acc_3", connection: "acme-linear", serviceUid: "", handle: "bob" }], ["acme"]),
  userRow("usr_3", "carol", [], [{ id: "acc_4", connection: "acme-linear", serviceUid: "", handle: "carol" }], ["acme"])] };
const aliceDetail: UserDetail = { user: aliceUser, removed: false, loginSpec: aliceUser.logins.join("\n"),
  oidcSpec: "https://acme.eu.auth0.com/:auth0|6512a9e0", connections: [{ name: "acme-linear", kind: "linear" }, { name: "disc", kind: "discord" }],
  appTokens: [], // none: a token's "last used" is relative to now, so the shot would drift
  notes: { markdown: "", html: "" } };

// Asks (ROOT/inbox/asks): ACME-42's ticket conversation, where spider asked
// alice to pick a fix (answered), withdrew one ask, reworded the merge ask
// (open) and asked bob about staging (open, for bob only).
const ask = (over: Partial<AskCard>): AskCard => ({
  id: "", question: "", options: [], version: 1, state: "open", answeredBy: "", choice: "", comment: "", reason: "", edited: false, canAnswer: true, for: "", ...over,
});
const askFix = ask({ id: "ask_fix", question: "Which fix do you want?", state: "answered", canAnswer: false, answeredBy: "alice", choice: "Poll for the form",
  comment: "keep the wait under 10 s", options: [{ id: "poll", label: "Poll for the form", detail: "Wait until the form shows" }, { id: "clock", label: "Mock the clock" }] });
const askRerun = ask({ id: "ask_rerun", question: "Shall I rerun the whole suite?", state: "withdrawn", canAnswer: false, reason: "CI reran it on its own",
  options: [{ id: "yes", label: "Yes" }, { id: "no", label: "No" }] });
const askMerge = ask({ id: "ask_merge", question: "Merge PR #212 into main?", version: 2, edited: true,
  options: [{ id: "merge", label: "Merge", detail: "CI is green; squash and merge" }, { id: "wait", label: "Wait", detail: "Leave it open for review" }] });
const askStaging = ask({ id: "ask_staging", question: "Deploy the fix to staging tonight?", canAnswer: false, for: "bob",
  options: [{ id: "yes", label: "Yes" }, { id: "no", label: "No" }] });
const spiderSays = (seq: number, time: string, text: string) => msg({ seq, from: "spider", color: "#e8590c", time, body: `<p>${text}</p>`, raw: text });
const acme42: Conversation = {
  ...ops, channelId: "chn_acme42", label: "ACME-42", kind: "work", needsYou: true, sendTo: "chn_acme42", lastSeq: 90,
  canJoin: false, canLeave: true, callIn: [], openAsks: [askMerge, askStaging],
  messages: [
    spiderSays(60, "2026-11-12T09:02:00Z", "The login test waits on a fixed 2 s timer, so it fails on a slow runner. I can fix it two ways."),
    msg({ seq: 61, card: "ask", ask: askFix, from: "spider", time: "2026-11-12T09:02:00Z" }),
    msg({ seq: 62, system: true, sysText: "alice chose Poll for the form on: Which fix do you want? — keep the wait under 10 s", time: "2026-11-12T09:10:00Z" }),
    msg({ seq: 70, card: "ask", ask: askRerun, from: "spider", time: "2026-11-12T09:31:00Z" }),
    msg({ seq: 71, system: true, sysText: "spider withdrew ask: Shall I rerun the whole suite? (CI reran it on its own)", time: "2026-11-12T09:44:00Z" }),
    msg({ seq: 80, card: "ask", ask: askMerge, from: "spider", time: "2026-11-12T10:05:00Z" }),
    msg({ seq: 81, system: true, sysText: "spider edited ask: Merge PR #212 into main? — Merge, Wait", time: "2026-11-12T10:07:00Z" }),
    msg({ seq: 84, card: "ask", ask: askStaging, from: "spider", time: "2026-11-12T10:08:00Z" }),
    msg({ seq: 90, from: "alice", mine: true, time: "2026-11-12T10:12:00Z", reAsk: { id: "ask_merge", question: "Merge PR #212 into main?" },
      body: "<p>re: Merge PR #212 into main?<br>Did the test pass three runs in a row?</p>", raw: "re: Merge PR #212 into main?\nDid the test pass three runs in a row?" }),
  ],
};

// Whiteboards (ROOT/inbox/whiteboards): alice's chat with bob and the coder
// session, with the board coder started; its elements are what board_apply adds and its
// thumbnail Jam's own render of them (both generated once with
// jam.ApplyBoardOps and boardrender.Render).
const bobCoder: Conversation = {
  ...ops, channelId: "chn_bobcoder", label: "bob, coder", kind: "group", sendTo: "chn_bobcoder", lastSeq: 80,
  canJoin: false, canLeave: false, callIn: [], newBoard: true,
  messages: [
    msg({ seq: 76, from: "coder", color: "#2f9e44", time: "2026-11-12T11:20:00Z", body: "<p>I drew the release steps from the ops thread. Please check the order.</p>",
      raw: "I drew the release steps from the ops thread. Please check the order." }),
    msg({ seq: 77, from: "coder", color: "#2f9e44", time: "2026-11-12T11:20:00Z", card: "board", board: { id: "brd_release", title: "Release 1.14" } }),
    msg({ seq: 80, from: "alice", mine: true, time: "2026-11-12T11:31:00Z", body: "<p>Looks right. I added a note on the board.</p>", raw: "Looks right. I added a note on the board." }),
  ],
};

// Your account (ROOT/inbox/account): the apps alice signed in to Jam, with
// times relative to now so the dialog reads the same on every shot.
const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
const appTokens: AppTokensResponse = {
  tokens: [
    { id: "apt_1", label: "VS Code", created: hoursAgo(72), lastUsed: hoursAgo(2), expires: hoursAgo(-24 * 30), operator: true },
    { id: "apt_2", label: "VS Code Insiders", created: hoursAgo(24 * 20), lastUsed: hoursAgo(24 * 6), expires: hoursAgo(-24 * 24), operator: false },
  ],
};

// The dashboard, search and health (ROOT/configuration/dashboard, and the
// title bar on ROOT/start/signing-in): two open conditions, the stale
// reviewer-bot and acme's unknown escalation target, badged on the tabs;
// the dashboard's tiles count the agents above.
const adminTabs = adminBoot.tabs.map((t) => ({ ...t, ...{
  dashboard: { badge: 4, badgeRed: true, badgeTitle: "1 broken · 1 out of date · 1 config · 1 warning" },
  config: { badge: 1, badgeRed: false, badgeTitle: "1 config" },
  agents: { badge: 1, badgeRed: false, badgeTitle: "1 out of date" },
  health: { badge: 2, badgeRed: true, badgeTitle: "1 broken · 1 warning" },
}[t.key] }));
const credDown = "credential anthropic-key cannot be resolved; brokered requests using it fail (502)";
const diskFull = "manager mac-mini disk 91% full (182.0GiB of 200.0GiB used)";
const dashboard: Dashboard = {
  stats: { studios: 4, running: 1, waiting: 1, settingUp: 1, idled: 1, attention: 0, projects: 3, agents: 5, users: 3, specs: 7 },
  attention: [{ kind: "broken", why: credDown, path: "health" }, { kind: "alert", why: diskFull, path: "health" }],
  jamContext: { ...emptyContext, scope: "Jam-wide", showResources: false }, jamNotes: { markdown: "", html: "" },
  hasManagers: true,
  managers: [{ name: "mac-mini", connected: true, version: "1.14.0", studios: 4, capacity: 8, lastSeen: "10:44:02", strays: [],
    host: { at: "10:44:02", mem: "11.2GiB / 32.0GiB", memPct: 35, disk: "182.0GiB / 200.0GiB", diskPct: 91, reclaimable: "24.5GiB",
      reclaimableDetail: "images 18.0GiB · build cache 6.5GiB · volumes 0B", lastCleanup: "Nov 12 06:00: freed 12.3GiB (above high mark)" },
    activities: ["preparing kit review-kit@v4: building step 7/12 (2m)"] }],
  coves: agentRows.filter((a) => a.hasStudio).map((a) => ({ ...coveRow(a), manager: "mac-mini",
    attn: a.image === "stale" ? { kind: "stale", why: a.id + ": image stale" } : null })),
};
const health: Health = {
  open: [
    { key: "cred.unavailable:anthropic-key", severity: "critical", summary: credDown, age: "12m", lasted: "", detail: "",
      fix: "check the credentials file has anthropic-key, then restart jam serve" },
    { key: "manager.disk:mac-mini", severity: "warning", summary: diskFull, age: "3h", lasted: "",
      detail: "reclaimable: images 18.0GiB, build cache 6.5GiB, volumes 0B",
      fix: "free disk in the manager's VM (jam-colima cleans up automatically above its high mark), or grow the VM disk (colima stop; colima start --disk <GiB>)" },
  ],
  resolved: [
    { key: "config.follow:jam", severity: "warning", summary: "the Jam could not apply its config repo", fix: "", detail: "", age: "1d", lasted: "25m" },
    { key: "pool.account.refresh:solo", severity: "warning", summary: "pool account solo cannot refresh its token", fix: "", detail: "", age: "3d", lasted: "2h" },
  ],
  silences: "https://alerts.acme.example/#/silences",
};
const hit = (title: string, sub: string, path: string) => ({ title, sub, path, exact: false });
const searchReview: SearchResult = { q: "review", tooShort: false, total: 6, jump: "", groups: [
  { key: "agents", name: "Agents", count: 2, more: "", hits: [
    hit("ci-bot", "grants: acme/reviewer", "agents/ci-bot"),
    hit("ses_01jc2b8k4m", "acme/reviewer · waiting", "agents/ses_01jc2b8k4m")] },
  { key: "roles", name: "Roles", count: 1, more: "", hits: [hit("acme/reviewer", "destinations: github · kit review-kit", acme + "/agents/reviewer")] },
  { key: "kits", name: "Kits", count: 1, more: "", hits: [hit("review-kit", "current v4 · " + reviewPrompt, "config/kits/review-kit")] },
  { key: "model-specs", name: "Model-specs", count: 1, more: "", hits: [hit("review-opus", "claude · anthropic-key · claude-opus-5-5", "config/model-specs/review-opus")] },
  { key: "squawks", name: "Squawks", count: 1, more: "health/intercom?q=review", hits: [
    hit("Can you review the login fix before I open the PR?", "Nov 12 10:38 · coder-01 → coder-01", "health/intercom?q=review")] },
] };
const channelLog: ChannelLog = { configured: true, legacy: false, filtered: false, badDate: false, rows: acmeSquawks };

export const mocks: Record<string, unknown> = {
  "GET /app/admin/api/bootstrap": { ...adminBoot, name: "Local admin", tabs: adminTabs },
  "GET /app/admin/api/dashboard": dashboard,
  "GET /app/admin/api/health": health,
  "GET /app/admin/api/intercom": channelLog,
  "GET /app/admin/api/search?q=review": searchReview,
  "GET /app/admin/api/tree": tree,
  "GET /app/admin/api/config/branches": branches,
  "GET /app/admin/api/config/branches/pr": { url: "" },
  "GET /app/admin/api/tracking": tracking,
  "POST /app/admin/api/tracking/diff": trackingPlan,
  "GET /app/admin/api/projects": projectList,
  "GET /app/admin/api/projects/acme": acmeProject,
  "GET /app/admin/api/projects/acme?section=intercom": { ...acmeProject, squawks: acmeSquawks },
  "GET /app/admin/api/users": userList,
  "GET /app/admin/api/users/usr_1": aliceDetail,
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
  "GET /app/admin/api/kits": kitList,
  "GET /app/admin/api/kits/review-kit": reviewKit,
  "GET /app/admin/api/kits/review-kit?v=4&diff=3": reviewDiff,
  "GET /app/admin/api/kits/review-kit/draft": reviewDraft,
  "GET /app/admin/api/kits/go-tools": goTools,

  "GET /app/user/api/conversations/chn_helper": helperConv,
  "GET /app/user/api/conversations/chn_helper/presence": { sessions: helperConv.sessions },
  "POST /app/user/api/conversations/chn_helper/read": {},
  "GET /app/user/api/sessions/ses_helper": helperHead,
  "GET /app/user/api/sessions/ses_helper/pane/thinking": helperPane("thinking", {
    streams: [{ id: stream, firstAt: "2026-11-10T09:00:00Z", events: 210 }, { id: "9e03d6b1c4a27f50", firstAt: "2026-11-09T15:40:00Z", events: 96 }] }),
  "GET /app/user/session/ses_helper/thinking/events": helperThinking,
  "GET /app/user/api/sessions/ses_helper/pane/previews": helperPane("previews", { previews: [
    { name: "notes", url: "https://notes--helper.jam-previews.example/", port: 4000, frame: true },
    { name: "api", url: "https://api--helper.jam-previews.example/", port: 8080, frame: true },
  ] }),
  "GET /app/user/api/sessions/ses_helper/pane/artifacts": helperPane("artifacts", {
    files: [{ id: "blb_notes", name: "release-notes-1.14.md", size: "6 KB", image: false }],
    asks: [{ id: "ask_1", question: "Ship the notes with 1.14 or wait for 1.14.1?", state: "answered" }],
    links: [{ url: "https://github.com/acme/app/pull/61", label: "app PR #61" }, { url: "https://github.com/acme/app/pull/57", label: "app PR #57" }],
  }),
  "GET /app/user/api/sessions/ses_helper/pane/status": helperPane("status", { status: [
    { name: "State", value: "is running Bash" }, { name: "In this state", value: "2m" }, { name: "Up", value: "1h 12m" },
    { name: "Model spec", value: "claude-default" }, { name: "Kit", value: "default@v3" }, { name: "Image", value: "jam-studio:1.14.0" },
    { name: "Alarm pr-check", value: "*/10 * * * * · next Nov 10 10:20" },
    { name: "Turns", value: "3 (2 episodes)" }, { name: "Tool calls", value: "58 (1 error)" }, { name: "Tokens", value: "412,305 in / 18,940 out" },
    { name: "Context", value: "84,112 of 200,000 tokens (claude-opus-5-5)" }, { name: "Cost", value: "$1.84" }, { name: "This turn", value: "$0.21" },
    { name: "Last event", value: "just now" },
  ] }),

  "GET /app/user/api/conversations/chn_acme42": acme42,
  "GET /app/user/api/conversations/chn_acme42/presence": { sessions: [] },
  "POST /app/user/api/conversations/chn_acme42/read": {},
  "GET /app/user/api/conversations/chn_bobcoder": bobCoder,
  "GET /app/user/api/conversations/chn_bobcoder/presence": { sessions: [] },
  "POST /app/user/api/conversations/chn_bobcoder/read": {},
  "GET /app/user/api/boards/brd_release": { id: "brd_release", title: "Release 1.14", channel: "chn_bobcoder", readOnly: false },
  "GET /app/user/boards/brd_release/elements": { seq: 10, elements: boardRelease },
  "GET /app/user/boards/brd_release/thumb.png": boardReleaseThumb,
  "GET /app/user/api/app-tokens": appTokens,
};
