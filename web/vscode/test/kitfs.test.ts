import { describe, expect, it, vi } from "vitest";
import { FileChangeType, FileSystemError, FileType, Uri } from "./vscode";
import { ApiError } from "../src/api";
import { KitFileSystem, type KitApi } from "../src/kits/fs";
import { kitUri, parseKitUri } from "../src/kits/uri";
import type { DraftFiles } from "../../app/src/admin/api/types.js" with { "resolution-mode": "import" };

const enc = (s: string) => new TextEncoder().encode(s);
const listing = (files: { path: string; sha256: string; size?: number; exec?: boolean }[], problems: string[] = []): DraftFiles => ({
  etag: '"t"', base: 1, current: 1, problems, maxFile: 1 << 20,
  files: files.map((f) => ({ size: 1, exec: false, ...f })),
});

function memo() {
  const m = new Map<string, unknown>();
  return { get: <T,>(k: string) => m.get(k) as T | undefined, update: async (k: string, v: unknown) => void m.set(k, v) };
}

function fake(initial = listing([{ path: "kit.yml", sha256: "k1" }, { path: "bin/run.sh", sha256: "r1", exec: true }]), o: ConstructorParameters<typeof KitFileSystem>[1] = {}) {
  let now = initial;
  const api = {
    files: vi.fn(async () => now),
    readFile: vi.fn(async (_k: string, p: string) => ({ bytes: enc(`content of ${p}`), sha256: now.files.find((f) => f.path === p)?.sha256 ?? "" })),
    writeFile: vi.fn(async (_k: string, p: string, _b: Uint8Array) => {
      now = listing([...now.files.filter((f) => f.path !== p), { path: p, sha256: "new" }]);
      return now;
    }),
    deleteFile: vi.fn(async (_k: string, p: string) => {
      now = listing(now.files.filter((f) => f.path !== p));
      return now;
    }),
    renameFile: vi.fn(async (_k: string, from: string, to: string) => {
      now = listing(now.files.map((f) => (f.path === from ? { ...f, path: to } : f)));
      return now;
    }),
  } satisfies KitApi;
  const apiFor = vi.fn((_jam: string, _branch: string) => api as KitApi);
  const fs = new KitFileSystem(apiFor, o);
  const set = (l: DraftFiles) => {
    now = l;
    fs.refresh(kitUri("jam.example", "jam/alice", "web"));
  };
  return { api, apiFor, fs, set };
}
const u = (p = "") => kitUri("jam.example", "jam/alice", "web", p);

describe("kit URIs", () => {
  it("round-trip a branch with a slash", () => {
    const s = kitUri("jam.example", "jam/alice", "web", "bin/run.sh").toString();
    expect(s).toBe("jamkit://jam.example/jam~alice/web/bin/run.sh");
    expect(parseKitUri(Uri.parse(s) as never)).toEqual({ jam: "jam.example", branch: "jam/alice", kit: "web", path: "bin/run.sh" });
  });
});

describe("KitFileSystem", () => {
  it("lists files and implicit directories, and stats them", async () => {
    const { fs, apiFor } = fake();
    expect(await fs.readDirectory(u())).toEqual([["bin", FileType.Directory], ["kit.yml", FileType.File]]);
    expect((await fs.stat(u("kit.yml"))).type).toBe(FileType.File);
    expect((await fs.stat(u("bin"))).type).toBe(FileType.Directory);
    await expect(fs.stat(u("nope"))).rejects.toMatchObject({ code: "FileNotFound" });
    expect(apiFor).toHaveBeenCalledWith("jam.example", "jam/alice");
  });
  it("saves against the content it last read", async () => {
    const { fs, api } = fake();
    await fs.readFile(u("kit.yml"));
    await fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true });
    expect(api.writeFile).toHaveBeenCalledWith("web", "kit.yml", enc("x"), { sha256: "k1" });
    await fs.writeFile(u("kit.yml"), enc("y"), { create: false, overwrite: true });
    expect(api.writeFile).toHaveBeenLastCalledWith("web", "kit.yml", enc("y"), { sha256: "new" });
    await fs.writeFile(u("docs/a.md"), enc("z"), { create: true, overwrite: false });
    expect(api.writeFile).toHaveBeenLastCalledWith("web", "docs/a.md", enc("z"), "create");
  });
  it("fails a save visibly when the file changed on the Jam, and says it changed", async () => {
    const { fs, api } = fake();
    await fs.readFile(u("kit.yml"));
    api.writeFile.mockRejectedValueOnce(new ApiError("the file changed", 412, "changed"));
    const seen: string[] = [];
    fs.onDidChangeFile((e) => seen.push(...e.map((c) => `${c.type} ${c.uri.toString()}`)));
    const err = await fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true }).catch((e) => e);
    expect(err).toBeInstanceOf(FileSystemError);
    expect(String(err.message)).toMatch(/changed on the Jam/);
    expect(seen).toContain(`${FileChangeType.Changed} jamkit://jam.example/jam~alice/web/kit.yml`);
  });
  it("says the draft is gone, without deleting anything open", async () => {
    const { fs, api } = fake();
    await fs.readFile(u("kit.yml"));
    api.writeFile.mockRejectedValueOnce(new ApiError("kit web has no draft", 404, "no_draft"));
    const gone: unknown[] = [];
    fs.onDraftGone((g) => gone.push(g));
    const seen: { type: number }[] = [];
    fs.onDidChangeFile((e) => seen.push(...e));
    const err = await fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true }).catch((e) => e);
    expect(err).toMatchObject({ code: "Unavailable" });
    expect(String(err.message)).toMatch(/draft is gone/);
    expect(gone).toEqual([{ jam: "jam.example", branch: "jam/alice", kit: "web" }]);
    expect(seen.filter((e) => e.type === FileChangeType.Deleted)).toEqual([]);
  });
  it("keeps new empty directories, renames and deletes through the API", async () => {
    const { fs, api } = fake();
    await fs.createDirectory(u("empty"));
    expect(await fs.readDirectory(u())).toContainEqual(["empty", FileType.Directory]);
    await fs.rename(u("kit.yml"), u("kit2.yml"), { overwrite: false });
    expect(api.renameFile).toHaveBeenCalledWith("web", "kit.yml", "kit2.yml", "k1");
    await fs.delete(u("bin"), { recursive: true });
    expect(api.deleteFile).toHaveBeenCalledWith("web", "bin/run.sh", "r1");
  });
  it("keeps guarding a save after a restart (the content read is remembered)", async () => {
    const state = memo();
    const a = fake(undefined, { state });
    await a.fs.readFile(u("kit.yml"));
    const b = fake(listing([{ path: "kit.yml", sha256: "k9" }]), { state });
    await b.fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true });
    expect(b.api.writeFile).toHaveBeenCalledWith("web", "kit.yml", enc("x"), { sha256: "k1" });
  });
  it("refuses to save a file it never read, rather than overwrite blind", async () => {
    const { fs, api } = fake();
    const err = await fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true }).catch((e) => e);
    expect(err).toBeInstanceOf(FileSystemError);
    expect(String(err.message)).toMatch(/Compare Active File with Saved/);
    expect(api.writeFile).not.toHaveBeenCalled();
  });
  it("advances a file's mtime whenever its content changes", async () => {
    let t = 1000;
    const { fs, set } = fake(undefined, { state: memo(), now: () => t });
    const m1 = (await fs.stat(u("kit.yml"))).mtime;
    t = 500; // a clock going back still moves mtime forward
    set(listing([{ path: "kit.yml", sha256: "k2" }]));
    const m2 = (await fs.stat(u("kit.yml"))).mtime;
    expect(m2).toBeGreaterThan(m1);
    expect((await fs.stat(u("kit.yml"))).mtime).toBe(m2);
  });
  it("tells how to recover from a conflict", async () => {
    const { fs, api } = fake();
    await fs.readFile(u("kit.yml"));
    api.writeFile.mockRejectedValueOnce(new ApiError("changed", 412, "changed"));
    const err = await fs.writeFile(u("kit.yml"), enc("x"), { create: false, overwrite: true }).catch((e) => e);
    expect(String(err.message)).toMatch(/Compare Active File with Saved/);
  });
  it("shows the open kits under one root folder, by branch", async () => {
    const { fs } = fake(undefined, { openKits: () => [{ jam: "jam.example", branch: "jam/alice", kit: "web" }, { jam: "other", branch: "x", kit: "y" }] });
    const root = Uri.parse("jamkit://jam.example/") as never;
    expect((await fs.stat(root)).type).toBe(FileType.Directory);
    expect(await fs.readDirectory(root)).toEqual([["jam~alice", FileType.Directory]]);
    expect(await fs.readDirectory(Uri.parse("jamkit://jam.example/jam~alice") as never)).toEqual([["web", FileType.Directory]]);
  });
  it("toggles a file's executable bit, keeping its content", async () => {
    const { fs, api } = fake();
    expect(await fs.toggleExec(u("bin/run.sh"))).toBe(false);
    expect(api.writeFile).toHaveBeenCalledWith("web", "bin/run.sh", enc("content of bin/run.sh"), { sha256: "r1" }, false);
  });
  it("reports the draft's problems after each listing", async () => {
    const { fs, set } = fake();
    const got: { files: DraftFiles }[] = [];
    fs.onProblems((p) => got.push(p));
    set(listing([{ path: "kit.yml", sha256: "k1" }], ["kit.yml: bad"]));
    await fs.readDirectory(u());
    expect(got.at(-1)?.files.problems).toEqual(["kit.yml: bad"]);
  });
});
