// The mock API that `just manual-shot` serves the app on
// (vite.shot.config.ts, build/mockApi.ts; docs/modules/ROOT/pages/manual.adoc): each key is
// "METHOD /path" (with its query, or without to match any), each value the
// JSON answer, built from the tests' fixtures. A request with no entry
// answers 404, is logged and fails the shot, naming what it still needs.
import type { ConfigBranches, TreeNode, TreeResponse } from "../admin/api/types";
import { adminBoot } from "../admin/fixtures";
import { filledContext, project } from "../admin/projects/fixtures";
import { dest, destFields, endpoint } from "../admin/specs/fixtures";
import type { AgentDetail, AgentListRow, AgentsResponse, CoveRow } from "../admin/api/types";
import { agents as projectAgents, role } from "../admin/roles/fixtures";
import { emptyContext } from "../admin/projects/fixtures";

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
    group("config/model-specs", "Model Specs"),
  ],
};

const branches: ConfigBranches = {
  branches: [
    { name: "main", head: "4f2a9c1e0b7d", short: "4f2a9c1", followed: true },
    { name: "jam/docs", head: "9b81d07a3c22", short: "9b81d07", followed: false },
  ],
  followed: "main", defaultName: "jam/local-admin", canPR: false, repoLink: "", treeLink: "", prNoun: "pull request",
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

export const mocks: Record<string, unknown> = {
  "GET /app/admin/api/bootstrap": adminBoot,
  "GET /app/admin/api/tree": tree,
  "GET /app/admin/api/config/branches": branches,
  "GET /app/admin/api/projects/acme": project({ chatService: "disc", context: filledContext }),
  "GET /app/admin/api/destinations/github": github,
  "GET /app/admin/api/agents": agentList,
  "GET /app/admin/api/agents/ses_01jc3d9q7r": coder01,
  "GET /app/admin/api/projects/acme/agents": acmeAgents,
  "GET /app/admin/api/projects/acme/roles/reviewer": reviewer,
};
