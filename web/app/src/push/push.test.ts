import { describe, expect, it, vi } from "vitest";
import { decodeKey, signOutDevice, syncOnLoad, turnOff, turnOn, unsupportedView, view, type Env } from "./push";

const KEY = "BAAAAAAAAAAAAAA"; // decodes to 11 bytes

// fakeEnv: a browser with Push support, a fake push service, and a fake Jam.
function fakeEnv(over: { perm?: NotificationPermission; grant?: NotificationPermission; sub?: { key?: Uint8Array } | null; ok?: boolean } = {}) {
  const calls: string[] = [];
  type Sub = { endpoint: string; options: { applicationServerKey: ArrayBuffer | null }; unsubscribe: () => Promise<boolean>; toJSON: () => unknown };
  let sub: Sub | null = null;
  const make = (key: Uint8Array | null): Sub => ({
    endpoint: "https://push.example/dev-1",
    options: { applicationServerKey: key ? (key.buffer as ArrayBuffer) : null },
    unsubscribe: vi.fn(async () => { calls.push("unsubscribe"); sub = null; return true; }),
    toJSON: () => ({ endpoint: "https://push.example/dev-1", keys: { p256dh: "p", auth: "a" } }),
  });
  if (over.sub) sub = make(over.sub.key ?? null);
  const reg = { pushManager: {
    getSubscription: async () => sub,
    subscribe: async (o: { applicationServerKey: Uint8Array }) => { calls.push(`subscribe ${o.applicationServerKey.length}`); sub = make(o.applicationServerKey); return sub; },
  } } as unknown as ServiceWorkerRegistration;
  const store = new Map<string, string>();
  const perm = { value: over.perm ?? ("default" as NotificationPermission) };
  const env: Env = {
    nav: { serviceWorker: { ready: Promise.resolve(reg), getRegistration: async () => reg }, userAgent: "Mozilla/5.0 (X11; Linux x86_64)", platform: "Linux", maxTouchPoints: 0 },
    Notification: { get permission() { return perm.value; }, requestPermission: async () => { perm.value = over.grant ?? "granted"; return perm.value; } },
    PushManager: function () {},
    standalone: () => false,
    fetch: (async (url: string, init?: RequestInit) => { calls.push(`${init?.method ?? "GET"} ${url}`); return new Response(null, { status: over.ok === false ? 500 : 204 }); }) as unknown as typeof fetch,
    session: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v), removeItem: (k) => void store.delete(k) },
  };
  return { env, calls, store, perm };
}

describe("Web Push on this device", () => {
  it("turns on: permission, subscribe under the key, store it; off: unsubscribe and delete", async () => {
    const { env, calls, store } = fakeEnv();
    expect(await turnOn(env, "usr_1", KEY)).toBe(true);
    expect(store.get("jam.push.synced:usr_1")).toBe("https://push.example/dev-1");
    await turnOff(env, "usr_1");
    expect(calls).toEqual(["subscribe 11", "POST /app/user/push/subscriptions", "unsubscribe", "DELETE /app/user/push/subscriptions"]);
    expect(store.has("jam.push.synced:usr_1")).toBe(false);
  });

  it("doesn't subscribe when permission isn't granted", async () => {
    const { env, calls } = fakeEnv({ grant: "denied" });
    expect(await turnOn(env, "usr_1", KEY)).toBe(false);
    expect(calls).toEqual([]);
  });

  it("drops an old subscription before subscribing under the key", async () => {
    const { env, calls } = fakeEnv({ sub: { key: new Uint8Array([9]) } });
    expect(await turnOn(env, "usr_1", KEY)).toBe(true);
    expect(calls).toEqual(["unsubscribe", "DELETE /app/user/push/subscriptions", "subscribe 11", "POST /app/user/push/subscriptions"]);
  });

  it("re-sends a live subscription once a page session; replaces one under a rotated key", async () => {
    const same = fakeEnv({ perm: "granted", sub: { key: decodeKey(KEY) } });
    expect(await syncOnLoad(same.env, "usr_1", KEY)).toBe(true);
    expect(await syncOnLoad(same.env, "usr_1", KEY)).toBe(true); // marked: not re-sent
    expect(same.calls).toEqual(["POST /app/user/push/subscriptions"]);
    const rotated = fakeEnv({ perm: "granted", sub: { key: new Uint8Array([9, 9, 9]) } });
    expect(await syncOnLoad(rotated.env, "usr_1", KEY)).toBe(true);
    expect(rotated.calls).toEqual(["unsubscribe", "DELETE /app/user/push/subscriptions", "subscribe 11", "POST /app/user/push/subscriptions"]);
    expect(await syncOnLoad(fakeEnv({ perm: "granted" }).env, "usr_1", KEY)).toBe(false);
    expect(await syncOnLoad(fakeEnv({ perm: "default", sub: {} }).env, "usr_1", KEY)).toBe(false); // not granted: Off
  });

  it("labels the item; blocked is disabled; iOS outside the Home Screen gets the hint", () => {
    const { env, perm } = fakeEnv();
    expect(view(env, false, false)).toEqual({ label: "Notifications: Off", disabled: false, on: false });
    expect(view(env, true, true)).toEqual({ label: "Notifications: On", disabled: true, on: true });
    perm.value = "denied";
    expect(view(env, false, false)).toEqual({ label: "Notifications: Blocked in browser settings", disabled: true, on: false });
    const ios: Env = { ...env, PushManager: undefined, nav: { ...env.nav, userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)" } };
    expect(unsupportedView(ios)).toEqual({ label: "Add Jam to your Home Screen to get notifications", disabled: true, on: false });
    expect(view(ios, false, false)).toEqual(unsupportedView(ios));
    expect(unsupportedView({ ...env, PushManager: undefined })).toBeNull();
    expect(unsupportedView({ ...ios, standalone: () => true })).toBeNull();
  });

  it("signs a shared device out of push: unsubscribe and delete first; best effort", async () => {
    const { env, calls } = fakeEnv({ perm: "granted", sub: {} });
    await signOutDevice(env, "usr_1");
    expect(calls).toEqual(["unsubscribe", "DELETE /app/user/push/subscriptions"]);
    await expect(signOutDevice(fakeEnv({ ok: false, sub: {} }).env, "usr_1")).resolves.toBeUndefined();
    await expect(signOutDevice(fakeEnv().env, "usr_1")).resolves.toBeUndefined(); // nothing to drop
  });

  it("decodes the VAPID key from base64url", () => {
    expect(decodeKey(KEY)).toHaveLength(11);
    expect(Array.from(decodeKey("_-8"))).toEqual([255, 239]);
  });
});
