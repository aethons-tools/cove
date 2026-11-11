// The mock API that `just manual-shot` serves the app on
// (vite.shot.config.ts, build/mockApi.ts; docs/modules/ROOT/pages/manual.adoc): each key is
// "METHOD /path" (with its query, or without to match any), each value the
// JSON answer, built from the tests' fixtures. A request with no entry
// answers 404, is logged and fails the shot, naming what it still needs.
import type { ConfigBranches, TreeNode, TreeResponse } from "../admin/api/types";
import { adminBoot } from "../admin/fixtures";
import { filledContext, project } from "../admin/projects/fixtures";

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
    group("config/destinations", "Destinations"),
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

export const mocks: Record<string, unknown> = {
  "GET /app/admin/api/bootstrap": adminBoot,
  "GET /app/admin/api/tree": tree,
  "GET /app/admin/api/config/branches": branches,
  "GET /app/admin/api/projects/acme": project({ chatService: "disc", context: filledContext }),
};
