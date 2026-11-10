import type { ContextPanel, ProjectDetail } from "../api/types";

export const emptyContext: ContextPanel = { scope: "project", core: "", coreBytes: 0, budget: 4096, over: false, empty: true, showResources: true, leaves: [], resources: [], yaml: "",
  limits: { coreBudget: 4096, maxReadWhen: 160, maxLeaves: 20, maxLeafBytes: 65536, maxResources: 50 }, leafBytes: 0, deliveredBytes: 0 };

export const project = (p: Partial<ProjectDetail> = {}): ProjectDetail => ({
  name: "acme", inUseBy: "", renamable: true, chatService: "", chatServices: ["", "disc"],
  members: [{ userId: "usr_1", name: "alice", handle: "ali", delivery: [{ service: "discord", address: "chan-1" }], deliverySpec: "discord:chan-1" }],
  rooms: [{ name: "eng", connection: "disc", kind: "discord", ref: "123", postOnly: false }],
  roles: 2, agents: 3, liveAgents: 1, escalation: [], defaultChain: { category: "", tiers: [], spec: "" },
  context: emptyContext, logConfigured: true, squawks: [], notes: { markdown: "", html: "" }, ...p,
});

// filledContext is a project's agent context with a core, two progressive
// instructions (leaves) and two resources (the manual's Agent context
// screenshots show it).
const core = "Cove is the Jam service. Ship **user-visible** changes with their manual page.\n\n- Every change lands as one PR against `main`; the gate must be green.\n- Ask in channel:ops before touching prod.";
const leaves = [
  { name: "release.md", readWhen: "you are cutting a release",
    body: "# Cutting a release\n\n1. Make sure **main** is green.\n2. Tag `vX.Y.Z` and push the tag.\n3. Watch the release workflow.\n" },
  { name: "manual.md", readWhen: "you change anything a user can see",
    body: "# The manual\n\nEvery user-visible change updates `manual/` in the **same PR**.\n\n- The *as-is* manual lives on `main`.\n- The *goal* manual lives on the `goal` branch.\n" },
];
// The counts and YAML are what the server answers for this context.
export const filledContext: ContextPanel = {
  scope: "project", core, coreBytes: 275, budget: 1200,
  over: false, empty: false, showResources: true,
  leaves: leaves.map((l) => ({ ...l, bytes: new TextEncoder().encode(l.body).length })),
  resources: [
    { name: "cove", kind: "repo", ref: "aethons-tools/cove", note: "main repo" },
    { name: "board", kind: "tracker", ref: "linear:COV", note: "tickets" },
  ],
  limits: { coreBudget: 1200, maxReadWhen: 160, maxLeaves: 20, maxLeafBytes: 65536, maxResources: 50 }, leafBytes: 283, deliveredBytes: 490,
  yaml: "core: |-\n    Cove is the Jam service. Ship **user-visible** changes with their manual page.\n\n    - Every change lands as one PR against `main`; the gate must be green.\n    - Ask in channel:ops before touching prod.\nleaves:\n    - name: release.md\n      read-when: you are cutting a release\n      body: |\n        # Cutting a release\n\n        1. Make sure **main** is green.\n        2. Tag `vX.Y.Z` and push the tag.\n        3. Watch the release workflow.\n    - name: manual.md\n      read-when: you change anything a user can see\n      body: |\n        # The manual\n\n        Every user-visible change updates `manual/` in the **same PR**.\n\n        - The *as-is* manual lives on `main`.\n        - The *goal* manual lives on the `goal` branch.\nresources:\n    - name: cove\n      kind: repo\n      ref: aethons-tools/cove\n      note: main repo\n    - name: board\n      kind: tracker\n      ref: linear:COV\n      note: tickets\n",
};
