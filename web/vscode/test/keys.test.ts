import { chmod, mkdir, mkdtemp, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { ensureKey } from "../src/ssh/keys";
import type { ExecFile } from "../src/ssh/keys";

const PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenerated jam";
const mode = async (p: string) => (await stat(p)).mode & 0o777;

function fake(calls: string[][], pub = PUB): ExecFile {
  return async (_file, args) => {
    calls.push(args);
    if (args.includes("-y")) return { stdout: pub + "\n", stderr: "" };
    await writeFile(args[args.length - 1], "PRIVATE", { mode: 0o644 });
    return { stdout: "", stderr: "" };
  };
}
const setup = async () => {
  const dir = path.join(await mkdtemp(path.join(tmpdir(), "jamkeys-")), "ssh");
  return { dir, key: path.join(dir, "id_ed25519") };
};

describe("ensureKey", () => {
  it("generates with ssh-keygen when absent", async () => {
    const { dir, key } = await setup();
    const calls: string[][] = [];
    expect(await ensureKey(key, "ssh-keygen", fake(calls))).toBe(PUB);
    const gen = calls[0];
    expect(gen.slice(0, 8)).toEqual(["-t", "ed25519", "-N", "", "-C", "jam", "-q", "-f"]);
    expect(path.basename(gen[8])).toBe("id_ed25519");
    expect(path.dirname(path.dirname(gen[8]))).toBe(dir);
    expect(gen).toHaveLength(9);
    expect(calls[1]).toEqual(["-y", "-f", key]);
    expect(await readFile(key, "utf8")).toBe("PRIVATE");
    expect(await mode(key)).toBe(0o600);
    expect((await readdir(dir)).sort()).toEqual(["id_ed25519"]);
  });

  it("existing key is not regenerated", async () => {
    const { dir, key } = await setup();
    await mkdir(dir, { recursive: true });
    await writeFile(key, "MINE", { mode: 0o600 });
    const calls: string[][] = [];
    await ensureKey(key, "ssh-keygen", fake(calls));
    expect(calls).toEqual([["-y", "-f", key]]);
    expect(await readFile(key, "utf8")).toBe("MINE");
  });

  it("0644 key is tightened to 0600", async () => {
    const { dir, key } = await setup();
    await mkdir(dir, { recursive: true });
    await writeFile(key, "MINE");
    await chmod(key, 0o644);
    await ensureKey(key, "ssh-keygen", fake([]));
    expect(await mode(key)).toBe(0o600);
  });

  it("non-ed25519 key errors", async () => {
    const { dir, key } = await setup();
    await mkdir(dir, { recursive: true });
    await writeFile(key, "MINE", { mode: 0o600 });
    await expect(ensureKey(key, "ssh-keygen", fake([], "ssh-rsa AAAA x"))).rejects.toThrow(
      `${key} is not an ed25519 key (remove it to generate a new one)`,
    );
  });
});
