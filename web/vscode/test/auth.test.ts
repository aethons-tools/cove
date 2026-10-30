import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Auth, SESSION_KEY } from "../src/auth";

const URL_ = "https://jam.example.com";

async function setup(
  exchange: () => Response = () => Response.json({ token: "tok1", expires_at: "2027-01-01T00:00:00Z", user: "ann" }),
  o: { tokenFile?: string; deleteFails?: boolean } = {},
) {
  const store = new Map<string, string>();
  const secrets = {
    get: async (k: string) => store.get(k),
    store: async (k: string, v: string) => void store.set(k, v),
    delete: async (k: string) => {
      if (o.deleteFails) throw new Error("keyring locked");
      store.delete(k);
    },
  };
  const opened: string[] = [];
  const fetchFn = vi.fn(async (..._a: unknown[]) => exchange());
  const tokenFile = o.tokenFile ?? path.join(await mkdtemp(path.join(tmpdir(), "jamauth-")), "default-app-token.json");
  const auth = new Auth({
    secrets,
    openExternal: async (u) => (opened.push(u), true),
    fetch: fetchFn as unknown as typeof fetch,
    uriScheme: "vscode",
    hostname: "host",
    tokenFile,
  });
  const stateOf = () => new URL(opened.at(-1)!).searchParams.get("state")!;
  return { store, opened, fetchFn, tokenFile, auth, stateOf };
}

const tick = () => vi.advanceTimersByTimeAsync(0);

beforeEach(() => void vi.useFakeTimers());
afterEach(() => void vi.useRealTimers());

describe("Auth", () => {
  it("signIn opens the consent URL and completes on the matching callback", async () => {
    const t = await setup();
    const p = t.auth.signIn(URL_);
    await tick();
    const u = new URL(t.opened[0]!);
    expect(u.origin + u.pathname).toBe(`${URL_}/app/auth/app-token`);
    expect(u.searchParams.get("client")).toBe("VS Code on host");
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c1", state: t.stateOf() }))).toBe(true);
    const s = await p;
    expect(s).toEqual({ url: URL_, token: "tok1", user: "ann", expiresAt: "2027-01-01T00:00:00Z" });
    const [ex, init] = t.fetchFn.mock.calls[0] as [string, RequestInit];
    expect(ex).toBe(`${URL_}/app/auth/app-token/exchange`);
    const body = JSON.parse(init.body as string);
    expect(body).toMatchObject({ code: "c1", redirect: "vscode://aethons.jam/auth" });
    expect(body.code_verifier).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(JSON.parse(t.store.get(SESSION_KEY)!)).toEqual(s);
    expect(JSON.parse(await readFile(t.tokenFile, "utf8"))).toEqual({ token: "tok1", jam_url: URL_ });
    expect(await t.auth.session(URL_)).toEqual(s);
  });

  it("callback with another state is ignored", async () => {
    const t = await setup();
    const p = t.auth.signIn(URL_);
    await tick();
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c", state: "other" }))).toBe(false);
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }))).toBe(true);
    await p;
  });

  it("callback with no pending sign-in is false", async () => {
    const t = await setup();
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c", state: "x" }))).toBe(false);
  });

  it("error=access_denied rejects as cancelled", async () => {
    const t = await setup();
    const p = t.auth.signIn(URL_);
    const r = expect(p).rejects.toThrow("Sign-in was cancelled");
    await tick();
    t.auth.handleCallback(new URLSearchParams({ error: "access_denied", state: t.stateOf() }));
    await r;
  });

  it("times out after 5 minutes", async () => {
    const t = await setup();
    const p = t.auth.signIn(URL_);
    const r = expect(p).rejects.toThrow("Sign-in timed out");
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    await r;
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }))).toBe(false);
  });

  it("exchange 400 rejects", async () => {
    const t = await setup(() => Response.json({ error: "invalid_grant" }, { status: 400 }));
    const p = t.auth.signIn(URL_);
    const r = expect(p).rejects.toThrow("Jam refused the sign-in code; try again");
    await tick();
    t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
    await r;
    expect(t.store.size).toBe(0);
  });

  it("a second signIn cancels the first", async () => {
    const t = await setup();
    const first = t.auth.signIn(URL_);
    const r = expect(first).rejects.toThrow();
    await tick();
    const firstState = t.stateOf();
    const second = t.auth.signIn(URL_);
    await tick();
    await r;
    expect(t.auth.handleCallback(new URLSearchParams({ code: "c", state: firstState }))).toBe(false);
    t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
    expect((await second).user).toBe("ann");
  });

  it("session for a different jam.url is undefined", async () => {
    const t = await setup();
    t.store.set(SESSION_KEY, JSON.stringify({ url: URL_, token: "t", user: "u", expiresAt: "e" }));
    expect(await t.auth.session("https://other.example.com")).toBeUndefined();
    expect((await t.auth.session(URL_))?.token).toBe("t");
  });

  it("signOut deletes the secret and the token file, and notifies", async () => {
    const t = await setup();
    const seen: unknown[] = [];
    t.auth.onChange((s) => seen.push(s));
    const p = t.auth.signIn(URL_);
    await tick();
    t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
    await p;
    await t.auth.signOut();
    expect(t.store.size).toBe(0);
    await expect(stat(t.tokenFile)).rejects.toThrow();
    expect(seen).toHaveLength(2);
    expect(seen[1]).toBeUndefined();
  });

  it("the token is never passed to openExternal", async () => {
    const t = await setup();
    const p = t.auth.signIn(URL_);
    await tick();
    t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
    await p;
    expect(t.opened.join(" ")).not.toContain("tok1");
  });

  it("a failed token-file write stores no secret", async () => {
    const d = await mkdtemp(path.join(tmpdir(), "jamauth-"));
    await writeFile(path.join(d, "notadir"), "x");
    const t = await setup(undefined, { tokenFile: path.join(d, "notadir", "jam", "default-app-token.json") });
    const p = t.auth.signIn(URL_);
    const r = expect(p).rejects.toThrow();
    await tick();
    t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
    await r;
    expect(t.store.size).toBe(0);
  });

  for (const [name, res] of [
    ["an empty object", () => Response.json({})],
    ["an error envelope", () => Response.json({ error: "x", code: "y" })],
    ["an empty token", () => Response.json({ token: "", user: "ann", expires_at: "e" })],
    ["a missing user", () => Response.json({ token: "t", expires_at: "e" })],
    ["an HTML page", () => new Response("<html>proxy</html>", { status: 200 })],
  ] as [string, () => Response][]) {
    it(`a 200 exchange with ${name} is a clear error and stores nothing`, async () => {
      const t = await setup(res);
      const p = t.auth.signIn(URL_);
      const r = expect(p).rejects.toThrow("Jam sign-in failed (unexpected response)");
      await tick();
      t.auth.handleCallback(new URLSearchParams({ code: "c", state: t.stateOf() }));
      await r;
      expect(t.store.size).toBe(0);
      await expect(stat(t.tokenFile)).rejects.toThrow();
    });
  }

  it("syncTokenFile rewrites a missing or stale token file from the session", async () => {
    const t = await setup();
    const s = { url: URL_, token: "tokS", user: "ann", expiresAt: "e" };
    await t.auth.syncTokenFile(s);
    expect(JSON.parse(await readFile(t.tokenFile, "utf8"))).toEqual({ token: "tokS", jam_url: URL_ });
    await writeFile(t.tokenFile, JSON.stringify({ token: "other", jam_url: URL_ }));
    await t.auth.syncTokenFile(s);
    expect(JSON.parse(await readFile(t.tokenFile, "utf8"))).toEqual({ token: "tokS", jam_url: URL_ });
    // An up-to-date file is left alone (a rewrite renames a new inode in).
    const before = (await stat(t.tokenFile)).ino;
    await t.auth.syncTokenFile(s);
    expect((await stat(t.tokenFile)).ino).toBe(before);
  });

  it("a sign-out during a syncTokenFile leaves no token file", async () => {
    for (let i = 0; i < 20; i++) {
      const t = await setup();
      const s = { url: URL_, token: "tokS", user: "ann", expiresAt: "e" };
      await Promise.all([t.auth.syncTokenFile(s), t.auth.signOut()]);
      await expect(stat(t.tokenFile)).rejects.toThrow();
    }
  });

  it("signOut removes the token file even when the secret delete fails, then rethrows", async () => {
    const t = await setup(undefined, { deleteFails: true });
    const seen: unknown[] = [];
    t.auth.onChange((s) => seen.push(s));
    await writeFile(t.tokenFile, JSON.stringify({ token: "t", jam_url: URL_ }));
    await expect(t.auth.signOut()).rejects.toThrow("keyring locked");
    await expect(stat(t.tokenFile)).rejects.toThrow();
    expect(seen).toEqual([undefined]);
  });
});
