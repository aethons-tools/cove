import type { ProjectAgents, RoleDetail } from "../api/types";
import { emptyContext } from "../projects/fixtures";

export const agents = (a: Partial<ProjectAgents> = {}): ProjectAgents => ({
  project: "acme", canRequest: true,
  roles: [{ name: "coder", destinations: [{ name: "gh", cred: "gh-pat" }, { name: "anthropic", cred: "" }], ttl: "1h", kit: "web", attn: { kind: "config", why: "role coder: kit web not found" } }],
  agents: [{ id: "ses_1", name: "ic", kind: "standing", unit: "", status: "running", image: "stale", lastSeen: "Oct 28 10:00:00", roles: ["coder"], attn: null }],
  ...a,
});

export const role = (r: Partial<RoleDetail> = {}): RoleDetail => ({
  project: "acme", name: "coder", managed: null, kit: "web", modelSpec: "claude-default", modelSpecDefault: true, ttl: "1h",
  dests: [{ name: "gh", cred: "gh-pat", credIsRole: true, route: "/gh/", missing: false }, { name: "old", cred: "", credIsRole: false, route: "", missing: true }],
  addressing: ["user:*"], egressManaged: true, egressDomains: ["a.example.com"],
  allocation: [{ label: "Max personal agents", value: "2", unset: false }, { label: "Reclaim after", value: "never", unset: true }],
  standing: [{ name: "ic", prompt: "Mind the shop.", actorId: "ses_1", status: "waiting", image: "stale", upgrade: "" }],
  holders: [{ id: "ses_9", override: true }],
  running: [{ id: "ses_1", name: "ic", project: "acme", role: "coder", unit: "", status: "waiting", connector: "ok", image: "stale", manager: "", leaseHolder: "", raisedAt: "Oct 28 09:00", lastSeen: "10:00:00", attn: null }],
  form: { destinations: "gh=gh-pat,old", addressing: "user:*", ttl: "1h", kit: "web", modelSpec: "", maxEphemeral: "", maxPersonal: "2", maxPersonalPerOwner: "", idleAfter: "", nagEvery: "", reclaimAfter: "", egress: "a.example.com" },
  context: { ...emptyContext, scope: "role", showResources: false }, canRequest: true, setupEnabled: false, attn: null, ...r,
});
