import { afterEach, describe, expect, it } from "vitest";
import type { TreeNode } from "../api/types";
import { ancestors, filterTree, loadOpen, saveOpen } from "./treeState";

const n = (path: string, label: string, children: TreeNode[] = [], group = children.length > 0): TreeNode =>
  ({ path, label, group, badge: 0, badgeRed: false, badgeTitle: "", children });

const tree = [
  n("config/tracking", "Tracking"),
  n("config/projects", "Projects", [
    n("config/projects/acme", "acme", [n("config/projects/acme/agents", "Agents", [n("config/projects/acme/agents/coder", "coder")])]),
  ]),
  n("config/users", "Users", [n("config/users/usr_1", "alice")]),
];

describe("tree state", () => {
  afterEach(() => localStorage.clear());

  it("lists a path's ancestors", () => {
    expect(ancestors("config/projects/acme/agents/coder")).toEqual(["config/projects", "config/projects/acme", "config/projects/acme/agents"]);
    expect(ancestors("config/tracking")).toEqual([]);
  });

  it("filters to matches and their ancestors", () => {
    const got = filterTree(tree, "COD");
    expect(got.map((x) => x.label)).toEqual(["Projects"]);
    expect(got[0]!.children[0]!.children[0]!.children.map((x) => x.label)).toEqual(["coder"]);
    expect(filterTree(tree, "")).toBe(tree);
  });

  it("keeps a matching group's children", () => {
    expect(filterTree(tree, "users")[0]!.children.map((x) => x.label)).toEqual(["alice"]);
  });

  it("remembers open branches, tolerating junk", () => {
    localStorage.setItem("jam-admin-tree", "{bad");
    expect(loadOpen().size).toBe(0);
    saveOpen(new Set(["config/users"]));
    expect([...loadOpen()]).toEqual(["config/users"]);
  });
});
