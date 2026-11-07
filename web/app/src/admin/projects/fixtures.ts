import type { ContextPanel, ProjectDetail } from "../api/types";

export const emptyContext: ContextPanel = { scope: "project", core: "", coreBytes: 0, budget: 4096, over: false, empty: true, showResources: true, leaves: [], resources: [], yaml: "" };

export const project = (p: Partial<ProjectDetail> = {}): ProjectDetail => ({
  name: "acme", inUseBy: "", renamable: true, chatService: "", chatServices: ["", "disc"],
  members: [{ userId: "usr_1", name: "alice", handle: "ali", delivery: [{ service: "discord", address: "chan-1" }], deliverySpec: "discord:chan-1" }],
  rooms: [{ name: "eng", connection: "disc", kind: "discord", ref: "123", postOnly: false }],
  roles: 2, agents: 3, liveAgents: 1, escalation: [], defaultChain: { category: "", tiers: [], spec: "" },
  context: emptyContext, logConfigured: true, squawks: [], notes: { markdown: "", html: "" }, ...p,
});

// filledContext is a project's agent context with a core, leaves and
// resources (the manual's project-page screenshot shows it).
export const filledContext: ContextPanel = {
  scope: "project", core: "Ship the release by Friday.\nEvery change lands as one PR against main; the gate must be green.\n", coreBytes: 95, budget: 1200,
  over: false, empty: false, showResources: true,
  leaves: [
    { name: "release.md", readWhen: "you are cutting a release" },
    { name: "style.md", readWhen: "you are writing user-facing text" },
  ],
  resources: [
    { name: "cove", kind: "repo", ref: "aethons-tools/cove", note: "main repo" },
    { name: "board", kind: "tracker", ref: "linear:COV", note: "tickets" },
  ],
  yaml: "core: |\n  Ship the release by Friday.\n  Every change lands as one PR against main; the gate must be green.\nleaves:\n  - name: release.md\n    read-when: you are cutting a release\n    body: |\n      # Cutting a release\n  - name: style.md\n    read-when: you are writing user-facing text\n    body: |\n      # Style\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}\n  - {name: board, kind: tracker, ref: \"linear:COV\", note: tickets}\n",
};
