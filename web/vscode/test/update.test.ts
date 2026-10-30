import { describe, expect, it, vi } from "vitest";
import { UPDATE_CHECK_MS, UPDATE_LATER_KEY, UPDATE_LATER_MS, Updater, offers, parseVersion } from "../src/update";
import type { UpdateDeps } from "../src/update";

const JAM = "https://jam.example.com";

describe("parseVersion and offers", () => {
  it("reads x.y.z only", () => {
    expect(parseVersion("0.123.0")).toEqual([0, 123, 0]);
    for (const bad of ["", "1.2", "1.2.3-beta", "01.2.3", "v1.2.3", 1, undefined]) expect(parseVersion(bad)).toBeUndefined();
  });

  it("offers only a newer version, never to or from a dev build", () => {
    expect(offers("0.122.0", "0.123.0")).toBe(true);
    expect(offers("0.123.0", "0.123.1")).toBe(true);
    expect(offers("0.9.0", "0.10.0")).toBe(true); // numeric, not lexical
    expect(offers("0.123.0", "0.123.0")).toBe(false);
    expect(offers("0.124.0", "0.123.0")).toBe(false); // a rolled-back Jam
    expect(offers("0.0.0", "0.123.0")).toBe(false);
    expect(offers("0.123.0", "0.0.0")).toBe(false);
    expect(offers("0.123.0", "junk")).toBe(false);
  });
});

function setup(over: { version?: string; info?: unknown; infoStatus?: number; vsixStatus?: number; signedIn?: boolean; claim?: boolean; picks?: (string | undefined)[] } = {}) {
  const state = new Map<string, unknown>();
  const calls: string[] = [];
  let now = 1_000_000_000;
  const picks = [...(over.picks ?? ["Update", "Reload Window"])];
  const fetch = vi.fn(async (u: string | URL | Request, init?: RequestInit) => {
    const url = String(u);
    calls.push(`GET ${url.slice(JAM.length)} ${(init?.headers as Record<string, string>).Authorization}`);
    if (url.endsWith("/me/vscode")) {
      return new Response(JSON.stringify(over.info ?? { available: true, version: "0.123.0" }), { status: over.infoStatus ?? 200 });
    }
    return new Response(new Uint8Array([1, 2, 3]), { status: over.vsixStatus ?? 200 });
  });
  const d = {
    version: over.version ?? "0.122.0",
    session: async () => (over.signedIn === false ? undefined : { url: JAM, token: "tok" }),
    fetch: fetch as unknown as typeof globalThis.fetch,
    claim: vi.fn(async () => over.claim ?? true),
    state: { get: <T>(k: string) => state.get(k) as T | undefined, update: async (k: string, v: unknown) => void state.set(k, v) },
    ask: vi.fn(async () => picks.shift()),
    install: vi.fn(async () => {}),
    reload: vi.fn(async () => {}),
    log: vi.fn(),
    now: () => now,
    setInterval: vi.fn(() => 7),
    clearInterval: vi.fn(),
  } satisfies UpdateDeps;
  return { d, u: new Updater(d), calls, state, advance: (ms: number) => (now += ms) };
}

describe("Updater", () => {
  it("offers a newer version, installs the .vsix and reloads", async () => {
    const { d, u, calls } = setup();
    await u.check();
    expect(calls).toEqual(["GET /me/vscode Bearer tok", "GET /me/vscode/jam.vsix Bearer tok"]);
    expect(d.ask).toHaveBeenNthCalledWith(1, "A Jam extension update is available: 0.123.0 (you have 0.122.0).", "Update", "Later");
    expect(d.install).toHaveBeenCalledWith(new Uint8Array([1, 2, 3]), "0.123.0");
    expect(d.reload).toHaveBeenCalledTimes(1);
  });

  it("does nothing when current, signed out, a dev build, a Jam without it, or another window claims the offer", async () => {
    for (const over of [
      { info: { available: true, version: "0.122.0" } },
      { info: { available: true, version: "0.121.0" } },
      { info: { available: false } },
      { signedIn: false },
      { version: "0.0.0" },
      { infoStatus: 404 },
      { claim: false },
    ]) {
      const { d, u } = setup(over);
      await u.check();
      expect(d.ask, JSON.stringify(over)).not.toHaveBeenCalled();
      expect(d.install).not.toHaveBeenCalled();
    }
  });

  it("claims one offer per version per check period", async () => {
    const { d, u, advance } = setup({ picks: [undefined, undefined, undefined] });
    await u.check();
    advance(UPDATE_CHECK_MS);
    await u.check();
    const ids = (d.claim.mock.calls as unknown as [string][]).map((c) => c[0]);
    expect(ids).toHaveLength(2);
    expect(ids[0]).toMatch(/^update:0\.123\.0:\d+$/);
    expect(ids[1]).not.toBe(ids[0]);
  });

  it("Later holds that version back for a day; dismissing just waits for the next check", async () => {
    const { d, u, state, advance } = setup({ picks: ["Later", undefined, undefined] });
    await u.check();
    expect(state.get(UPDATE_LATER_KEY)).toEqual({ version: "0.123.0", until: 1_000_000_000 + UPDATE_LATER_MS });
    await u.check();
    expect(d.ask).toHaveBeenCalledTimes(1);
    advance(UPDATE_LATER_MS);
    await u.check();
    expect(d.ask).toHaveBeenCalledTimes(2);
    expect(d.install).not.toHaveBeenCalled();
  });

  it("a newer version than the one put off is offered at once", async () => {
    const { d, u, state } = setup();
    state.set(UPDATE_LATER_KEY, { version: "0.123.0", until: Infinity });
    await u.check();
    expect(d.ask).not.toHaveBeenCalled();
    state.set(UPDATE_LATER_KEY, { version: "0.122.5", until: Infinity });
    await u.check();
    expect(d.ask).toHaveBeenCalled();
  });

  it("a failed check or download is logged, never thrown, and installs nothing", async () => {
    for (const over of [{ infoStatus: 500 }, { infoStatus: 401 }, { vsixStatus: 502 }]) {
      const { d, u } = setup(over);
      await expect(u.check()).resolves.toBeUndefined();
      expect(d.install).not.toHaveBeenCalled();
      expect(d.log).toHaveBeenCalledWith(expect.stringMatching(/^update: the check failed: /));
    }
  });

  it("starts with a check and repeats every UPDATE_CHECK_MS until disposed", async () => {
    const { d, u } = setup({ info: { available: false } });
    u.start();
    expect(d.setInterval).toHaveBeenCalledWith(expect.any(Function), UPDATE_CHECK_MS);
    await vi.waitFor(() => expect(d.fetch).toHaveBeenCalledTimes(1));
    u.dispose();
    expect(d.clearInterval).toHaveBeenCalledWith(7);
  });

  it("runs one check at a time", async () => {
    const { d, u } = setup({ info: { available: false } });
    await Promise.all([u.check(), u.check()]);
    expect(d.fetch).toHaveBeenCalledTimes(1);
  });
});
