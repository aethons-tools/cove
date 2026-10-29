import * as fs from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { NOTIFY_LOCK_TTL_MS, NOTIFY_SWEEP_MS, NOTIFY_UNFOCUSED_DELAY_MS, NotifyLock } from "../src/notify/lock";
import type { LockDeps } from "../src/notify/lock";

let dir: string;
let t = 1_000_000;
beforeEach(async () => {
  dir = await fs.mkdtemp(join(tmpdir(), "jamlock-"));
  t = 1_000_000;
});
afterEach(async () => {
  await fs.rm(dir, { recursive: true, force: true });
});
const mk = (over: Partial<LockDeps> = {}) =>
  new NotifyLock({ dir: join(dir, "locks"), sleep: async () => {}, now: () => t, fs, ...over });

describe("NotifyLock", () => {
  it("the first claim of an id wins and the second loses", async () => {
    const a = mk();
    const b = mk();
    expect(await a.claim("sqk_1", true)).toBe(true);
    expect(await b.claim("sqk_1", true)).toBe(false);
    expect(await a.claim("sqk_1", true)).toBe(false);
  });

  it("an unfocused claim waits 250 ms first, so a focused claim made during the wait wins", async () => {
    const waits: number[] = [];
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const slow = mk({ sleep: async (ms) => { waits.push(ms); await gate; } });
    const un = slow.claim("sqk_2", false);
    expect(await mk().claim("sqk_2", true)).toBe(true);
    release();
    expect(await un).toBe(false);
    expect(waits).toEqual([NOTIFY_UNFOCUSED_DELAY_MS]);
  });

  it("focused claims do not sleep", async () => {
    const waits: number[] = [];
    await mk({ sleep: async (ms) => { waits.push(ms); } }).claim("sqk_f", true);
    expect(waits).toEqual([]);
  });

  it("a different id is a different lock", async () => {
    const a = mk();
    expect(await a.claim("sqk_a", true)).toBe(true);
    expect(await mk().claim("sqk_b", true)).toBe(true);
  });

  it("the filename is a hash, so an id like ../../x stays inside the dir", async () => {
    expect(await mk().claim("../../x", true)).toBe(true);
    const names = await fs.readdir(join(dir, "locks"));
    expect(names).toHaveLength(1);
    expect(names[0]).toMatch(/^[0-9a-f]{32}\.lock$/);
    expect((await fs.readdir(dir)).sort()).toEqual(["locks"]);
  });

  it("an unexpected fs error claims", async () => {
    const bad = { ...fs, open: async () => { throw Object.assign(new Error("no"), { code: "EACCES" }); } } as unknown as LockDeps["fs"];
    expect(await mk({ fs: bad }).claim("sqk_e", true)).toBe(true);
  });

  it("an unexpected fs error is reported once per window, and every claim still wins", async () => {
    const bad = { ...fs, open: async () => { throw Object.assign(new Error("read-only"), { code: "EROFS" }); } } as unknown as LockDeps["fs"];
    const errs: unknown[] = [];
    const l = mk({ fs: bad, onError: (e) => void errs.push(e) });
    expect(await l.claim("sqk_1", true)).toBe(true);
    expect(await l.claim("sqk_2", true)).toBe(true);
    expect(errs).toHaveLength(1);
    expect((errs[0] as NodeJS.ErrnoException).code).toBe("EROFS");
    // EEXIST is the lock working, not an error.
    const ok: unknown[] = [];
    const a = mk({ onError: (e) => void ok.push(e) });
    await a.claim("sqk_3", true);
    expect(await a.claim("sqk_3", true)).toBe(false);
    expect(ok).toEqual([]);
  });

  it("old locks are swept at most once a minute", async () => {
    const a = mk();
    await a.claim("sqk_old", true);
    const locks = join(dir, "locks");
    const [name] = await fs.readdir(locks);
    const old = new Date(Date.now() - NOTIFY_LOCK_TTL_MS - 60_000);
    await fs.utimes(join(locks, name!), old, old);
    t += 10;
    // real mtime is far from the fake clock; sweep compares mtimeMs against now()
    t = Date.now();
    await a.claim("sqk_new", true);
    expect(await fs.readdir(locks)).toHaveLength(1); // old swept
    await fs.utimes(join(locks, (await fs.readdir(locks))[0]!), old, old);
    t += NOTIFY_SWEEP_MS - 1;
    await a.claim("sqk_new2", true);
    expect(await fs.readdir(locks)).toHaveLength(2); // within a minute: not swept
    t += 2;
    await a.claim("sqk_new3", true);
    expect((await fs.readdir(locks)).length).toBe(2); // swept one stale
  });
});
