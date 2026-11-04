import { beforeEach, describe, expect, it, vi } from "vitest";
import { diagnostics, mock, workspace } from "./vscode";
import { BranchState } from "../src/admin/branch";
import { KitsController, type KitNode } from "../src/kits/view";
import { attachProblems } from "../src/kits/problems";
import { NoAdminError } from "../src/admin/api";
import { kitUri } from "../src/kits/uri";
import type { Session } from "../src/auth";

const URL_ = "https://jam.example";
const row = (name: string, o: Record<string, unknown> = {}) => ({ name, current: 2, count: 2, base: "", egress: 0, usedBy: 0, invalid: false, draft: false, tracked: false, attn: null, ...o });

function setup(o: { admin?: boolean; branches?: string[]; kits?: ReturnType<typeof row>[] } = {}) {
  const api = {
    head: "h1",
    branches: vi.fn(async () => ({
      branches: (o.branches ?? ["main", "jam/alice"]).map((n) => ({ name: n, head: "h1", short: "h1", followed: n === "main" })),
      followed: "main", defaultName: "jam/alice", canPR: true, repoLink: "", treeLink: "", prNoun: "pull request",
    })),
    createBranch: vi.fn(async (name: string) => ({ branch: { name, head: "h2", short: "h2", followed: false }, message: "created" })),
    kits: vi.fn(async () => ({ kits: o.kits ?? [row("web", { draft: true }), row("base", { tracked: true }), row("app")], canSource: true })),
    openDraft: vi.fn(async () => ({ page: {}, message: "opened" })),
    files: vi.fn(async () => ({ etag: '"e1"', base: 2, current: 2, problems: [], maxFile: 1, files: [] })),
    publish: vi.fn(async () => ({ page: {}, message: "published web v3" })),
    discard: vi.fn(async () => ({ page: {}, message: "discarded" })),
  };
  const state = new Map<string, unknown>();
  const branches = new BranchState({ get: <T,>(k: string) => state.get(k) as T, update: async (k: string, v: unknown) => void state.set(k, v) });
  const session: Session = { url: URL_, token: "t", user: "ann", expiresAt: "", admin: o.admin ?? true };
  const makeApi = vi.fn((_url: string, _token: string, _branch: () => string) => api as never);
  const c = new KitsController({ jamUrl: () => URL_, session: async () => session, branches, makeApi, openExternal: vi.fn(async () => true) });
  return { api, c, branches, makeApi };
}
const labels = (ns: KitNode[], c: KitsController) => ns.map((n) => c.getTreeItem(n)).map((i) => [i.label, i.description ?? ""]);

beforeEach(() => {
  workspace.workspaceFolders = undefined;
  mock.answers.length = 0;
  diagnostics.clear();
});

describe("the Kits view", () => {
  it("asks for an operator sign-in without admin access", async () => {
    const { c } = setup({ admin: false });
    const [n, ...rest] = await c.getChildren();
    const item = c.getTreeItem(n!);
    expect(rest).toEqual([]);
    expect(String(item.label)).toMatch(/operator sign-in/);
    expect(item.command?.command).toBe("jam.signIn");
  });
  it("asks again when the Jam refuses the token", async () => {
    const { c, api } = setup();
    api.kits.mockRejectedValueOnce(new NoAdminError());
    const ns = await c.getChildren();
    expect(c.getTreeItem(ns[0]!).command?.command).toBe("jam.signIn");
  });
  it("shows the branch, then the kits", async () => {
    const { c } = setup();
    const ns = await c.getChildren();
    expect(labels(ns, c)).toEqual([
      ["Branch: jam/alice", ""],
      ["app", "v2"],
      ["base", "from its git repo"],
      ["web", "draft"],
    ]);
    expect(c.getTreeItem(ns[0]!).command?.command).toBe("jam.kits.switchBranch");
    expect(c.getTreeItem(ns[2]!).command).toBeUndefined();
    expect(c.getTreeItem(ns[3]!).command?.command).toBe("jam.kits.open");
  });
  it("offers to create the default branch", async () => {
    const { c, api, branches } = setup({ branches: ["main"] });
    const ns = await c.getChildren();
    expect(labels(ns, c)[0]).toEqual(["Create branch jam/alice", ""]);
    expect(api.kits).not.toHaveBeenCalled();
    await c.createBranch();
    expect(api.createBranch).toHaveBeenCalledWith("jam/alice");
    expect(branches.get(URL_)).toBe("jam/alice");
  });
  it("opens a kit's draft as a folder, once", async () => {
    const { c, api } = setup();
    await c.getChildren();
    await c.open("app");
    expect(api.openDraft).toHaveBeenCalledWith("app", 0);
    expect(workspace.updateWorkspaceFolders).toHaveBeenCalled();
    expect(workspace.workspaceFolders?.map((f) => [String(f.uri), f.name])).toEqual([["jamkit://jam.example/jam~alice/app", "app (jam/alice)"]]);
    await c.open("web");
    expect(api.openDraft).toHaveBeenCalledTimes(1); // web already has a draft
    await c.open("web");
    expect(workspace.workspaceFolders).toHaveLength(2);
  });
  it("publishes after asking, and closes the folder", async () => {
    const { c, api } = setup();
    await c.getChildren();
    await c.open("web");
    mock.answers.push(undefined);
    await c.publish("web");
    expect(api.publish).not.toHaveBeenCalled();
    mock.answers.push("Publish");
    await c.publish("web");
    expect(api.publish).toHaveBeenCalledWith("web", '"e1"', "h1");
    expect(workspace.workspaceFolders).toEqual([]);
  });
  it("discards after asking", async () => {
    const { c, api } = setup();
    await c.getChildren();
    mock.answers.push("Discard");
    await c.discard("web");
    expect(api.discard).toHaveBeenCalledWith("web", '"e1"');
  });
  it("marks a kit whose draft is gone", async () => {
    const { c } = setup();
    await c.getChildren();
    c.markGone({ jam: "jam.example", branch: "jam/alice", kit: "web" });
    const ns = await c.getChildren();
    expect(labels(ns, c)).toContainEqual(["web", "draft gone — reopen"]);
  });
  it("opens the kit in the console on its branch", async () => {
    const { c } = setup();
    await c.getChildren();
    const open = vi.fn(async () => true);
    (c as unknown as { d: { openExternal: unknown } }).d.openExternal = open;
    await c.openInConsole("web");
    expect(open).toHaveBeenCalledWith("https://jam.example/app/admin/config/kits/web?branch=jam%2Falice");
  });
});

describe("kit problems", () => {
  it("go on the file they name, else on kit.yml, and clear", () => {
    const set = attachProblems();
    const ref = { jam: "jam.example", branch: "jam/alice", kit: "web" };
    const files = [{ path: "kit.yml", sha256: "a", size: 1, exec: false }, { path: "bin/run.sh", sha256: "b", size: 1, exec: false }];
    set(ref, { etag: "", base: 1, current: 1, maxFile: 1, files, problems: ["bin/run.sh: not executable", "kit web: no base"] });
    const run = String(kitUri("jam.example", "jam/alice", "web", "bin/run.sh"));
    const ky = String(kitUri("jam.example", "jam/alice", "web", "kit.yml"));
    expect(diagnostics.get(run)?.map((d) => d.message)).toEqual(["bin/run.sh: not executable"]);
    expect(diagnostics.get(ky)?.map((d) => d.message)).toEqual(["kit web: no base"]);
    set(ref, { etag: "", base: 1, current: 1, maxFile: 1, files, problems: [] });
    expect(diagnostics.size).toBe(0);
  });
});
