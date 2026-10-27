// Web Push on this device, ported from uiassets/static/push.js (the old
// Inbox's principal-menu toggle): On subscribes this browser with the push
// service under Jam's VAPID key and stores the subscription with Jam; Off
// unsubscribes and deletes it. Everything goes through an Env, so the rules
// test without a browser's push service.

export interface Env {
  nav: {
    serviceWorker?: { ready: Promise<ServiceWorkerRegistration>; getRegistration(scope: string): Promise<ServiceWorkerRegistration | undefined> };
    userAgent: string;
    platform: string;
    maxTouchPoints: number;
    standalone?: boolean;
    clearAppBadge?: () => Promise<void>;
  };
  Notification?: { permission: NotificationPermission; requestPermission(): Promise<NotificationPermission> };
  PushManager?: unknown;
  standalone: () => boolean; // display-mode: standalone
  fetch: typeof fetch;
  session: Pick<Storage, "getItem" | "setItem" | "removeItem">;
}

// PushView is the menu item to show (null: none).
export type PushView = { label: string; disabled: boolean; on: boolean } | null;

const SYNC = "jam.push.synced:"; // the endpoint this page session last registered, per user

type Sub = PushSubscription;

const ios = (n: Env["nav"]) => /iPad|iPhone|iPod/.test(n.userAgent) || (n.platform === "MacIntel" && n.maxTouchPoints > 1);

export const supported = (e: Env) => !!(e.nav.serviceWorker && e.PushManager && e.Notification);

// unsupportedView: iOS offers Web Push only to the Home Screen app; elsewhere
// there is nothing this device can do, so the item goes away.
export const unsupportedView = (e: Env): PushView =>
  ios(e.nav) && !(e.nav.standalone === true || e.standalone())
    ? { label: "Add Jam to your Home Screen to get notifications", disabled: true, on: false }
    : null;

export function view(e: Env, on: boolean, busy: boolean): PushView {
  if (!supported(e)) return unsupportedView(e);
  if (e.Notification!.permission === "denied") return { label: "Notifications: Blocked in browser settings", disabled: true, on: false };
  return { label: on ? "Notifications: On" : "Notifications: Off", disabled: busy, on };
}

// decodeKey decodes the VAPID public key (base64url) for subscribe().
export function decodeKey(s: string): Uint8Array {
  const b64 = (s + "=".repeat((4 - (s.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

const synced = (e: Env, user: string) => { try { return e.session.getItem(SYNC + user); } catch { return null; } };

async function send(e: Env, user: string, method: "POST" | "DELETE", body: { endpoint: string }) {
  const r = await e.fetch("/app/user/push/subscriptions", {
    method, credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${method} subscription: ${r.status}`);
  try {
    if (method === "POST") e.session.setItem(SYNC + user, body.endpoint);
    else e.session.removeItem(SYNC + user);
  } catch { /* storage off: re-send next page */ }
}

const subJSON = (s: Sub) => (typeof s.toJSON === "function" ? s.toJSON() : s) as { endpoint: string };

// sameKey reports whether s was made under key (one that can't say is taken
// to match).
function sameKey(s: Sub, key: string): boolean {
  const have = s.options?.applicationServerKey;
  if (!have) return true;
  const a = new Uint8Array(have), b = decodeKey(key);
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

// drop unsubscribes s and tells Jam (best effort: the push service's 410
// prunes a row Jam missed).
async function drop(e: Env, user: string, s: Sub) {
  const endpoint = s.endpoint;
  await s.unsubscribe();
  try { await send(e, user, "DELETE", { endpoint }); } catch { /* pruned later */ }
}

async function subscribe(e: Env, user: string, reg: ServiceWorkerRegistration, key: string) {
  const s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: decodeKey(key) as BufferSource });
  await send(e, user, "POST", subJSON(s));
}

export async function turnOn(e: Env, user: string, key: string): Promise<boolean> {
  if ((await e.Notification!.requestPermission()) !== "granted") return false;
  const reg = await e.nav.serviceWorker!.ready;
  // subscribe() refuses while a subscription under another key exists.
  const old = await reg.pushManager.getSubscription();
  if (old) await drop(e, user, old);
  await subscribe(e, user, reg, key);
  return true;
}

export async function turnOff(e: Env, user: string): Promise<void> {
  const reg = await e.nav.serviceWorker!.ready;
  const s = await reg.pushManager.getSubscription();
  if (s) {
    const endpoint = s.endpoint;
    await s.unsubscribe();
    await send(e, user, "DELETE", { endpoint });
  }
}

// syncOnLoad reflects this device's state: a live subscription is re-sent
// (once per page session per user), so the endpoint belongs to whoever is
// signed in here now; one made under a since-rotated key is replaced.
export async function syncOnLoad(e: Env, user: string, key: string): Promise<boolean> {
  const reg = await e.nav.serviceWorker!.ready;
  const s = await reg.pushManager.getSubscription();
  if (!s || e.Notification!.permission !== "granted") return false;
  if (synced(e, user) === s.endpoint) return true;
  if (sameKey(s, key)) await send(e, user, "POST", subJSON(s));
  else {
    await drop(e, user, s);
    await subscribe(e, user, reg, key);
  }
  return true;
}

// signOutDevice unsubscribes this device and deletes its subscription before
// signing out, so the next person on a shared device doesn't get this user's
// notifications; best effort.
export async function signOutDevice(e: Env, user: string): Promise<void> {
  try {
    const reg = await e.nav.serviceWorker?.getRegistration("/app/");
    const s = reg && (await reg.pushManager.getSubscription());
    if (s) {
      const endpoint = s.endpoint;
      await s.unsubscribe();
      await send(e, user, "DELETE", { endpoint });
    }
  } catch { /* sign out anyway */ }
}

export const realEnv = (): Env => ({
  nav: navigator as unknown as Env["nav"],
  Notification: typeof Notification === "undefined" ? undefined : Notification,
  PushManager: (window as { PushManager?: unknown }).PushManager,
  standalone: () => !!window.matchMedia?.("(display-mode: standalone)").matches,
  fetch: (...a) => fetch(...a),
  session: sessionStorage,
});
