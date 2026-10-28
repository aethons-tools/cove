import type { ContextPanel, ProjectDetail } from "../api/types";

export const emptyContext: ContextPanel = { scope: "project", core: "", coreBytes: 0, budget: 4096, over: false, empty: true, showResources: true, leaves: [], resources: [], yaml: "", managed: null };

export const project = (p: Partial<ProjectDetail> = {}): ProjectDetail => ({
  name: "acme", managed: null, inUseBy: "", renamable: true, chatService: "", chatServices: ["", "disc"],
  members: [{ userId: "usr_1", name: "alice", handle: "ali", delivery: [{ service: "discord", address: "chan-1" }], deliverySpec: "discord:chan-1" }],
  rooms: [{ name: "eng", connection: "disc", kind: "discord", ref: "123", postOnly: false }],
  roles: 2, agents: 3, liveAgents: 1, escalation: [], defaultChain: { category: "", tiers: [], spec: "" },
  context: emptyContext, logConfigured: true, squawks: [], ...p,
});
