// Parsing of the server's `notify` SSE event, the per-window burst gate and the
// click target. Pure: no vscode import. The payload is someone's message, so
// every field is checked and nothing is trusted as a link unless it matches an id regexp.
import { ID_RE } from "../bridge/router.js";
import { SESSION_ID_RE } from "../uri.js";

export const NOTIFY_CHANNEL_GAP_MS = 5000;
export const NOTIFY_TITLE_MAX = 120;
export const NOTIFY_BODY_MAX = 200;
const ID_MAX = 200;

export type NotifyKind = "needs" | "messages";
export type NotifyEvent = { id: string; title: string; body: string; channel: string; session: string; kind: NotifyKind };

// Control characters other than \n, and line/paragraph separators, become a
// space. Bidi controls (marks, embeddings, overrides, isolates) are dropped, so a
// sender can't visually reorder a title such as "alice · eng".
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u2028\u2029]/g;
const BIDI = /[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g;

// plainText strips what clean strips, without a length cap.
export function plainText(s: string): string {
  return s.replace(BIDI, "").replace(CONTROL, " ");
}

// inertText is s with no link VS Code can parse: its notifications turn
// "[label](command:…)" (also https:, file:) into a clickable link, and nothing
// escapes it (a backslash is not honoured), so a zero-width space goes between
// every "](" and the text still reads the same.
export function inertText(s: string): string {
  return plainText(s).replace(/\](?=\()/g, "]\u200b");
}

function clean(s: string, max: number): string {
  const t = plainText(s).trim();
  const runes = [...t];
  return runes.length <= max ? t : runes.slice(0, max - 1).join("") + "…";
}

export function parseNotify(data: string): NotifyEvent | undefined {
  let o: unknown;
  try {
    o = JSON.parse(data);
  } catch {
    return undefined;
  }
  if (typeof o !== "object" || o === null || Array.isArray(o)) return undefined;
  const r = o as Record<string, unknown>;
  const { id, title, body, channel, session, kind } = r;
  if (typeof id !== "string" || id === "" || id.length > ID_MAX) return undefined;
  if (typeof title !== "string" || typeof body !== "string") return undefined;
  if (typeof channel !== "string" || !ID_RE.chn.test(channel)) return undefined;
  if (typeof session !== "string") return undefined;
  return {
    id,
    title: clean(title, NOTIFY_TITLE_MAX),
    body: clean(body, NOTIFY_BODY_MAX),
    channel,
    session: SESSION_ID_RE.test(session) ? session : "",
    kind: kind === "needs" ? "needs" : "messages",
  };
}

// ChannelGate keeps bursts down. It is fed every parsed event before the lock, so
// every window drops the same ones.
export class ChannelGate {
  private last = new Map<string, number>();

  admit(e: NotifyEvent, now: number): boolean {
    if (e.kind === "needs") return true;
    const prev = this.last.get(e.channel);
    if (prev !== undefined && now - prev < NOTIFY_CHANNEL_GAP_MS) return false;
    this.last.set(e.channel, now);
    return true;
  }
}

// NotifyTarget is the conversation a click goes to (its window, else the hub).
export type NotifyTarget = { channel: string; session: string };

export function notifyTarget(e: NotifyEvent): NotifyTarget {
  return { channel: e.channel, session: e.session };
}

// targetLink is an OS notification's click link: the extension's go route,
// naming the Jam when it isn't the default.
export function targetLink(t: NotifyTarget, scheme: string, jam?: string): string {
  const q = new URLSearchParams();
  if (t.session) q.set("session", t.session);
  if (jam) q.set("jam", jam);
  const qs = q.toString();
  return `${scheme}://aethons.jam/go/${t.channel}${qs ? `?${qs}` : ""}`;
}
