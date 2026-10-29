// Parsing of the server's `notify` SSE event, the per-window burst gate and the
// click target. Pure: no vscode import. The payload is someone's message, so
// every field is checked and nothing is trusted as a link unless it matches an id regexp.
import { ID_RE } from "../bridge/router.js";
import { webUrl } from "../open.js";
import { SESSION_ID_RE } from "../uri.js";

export const NOTIFY_CHANNEL_GAP_MS = 5000;
export const NOTIFY_TITLE_MAX = 120;
export const NOTIFY_BODY_MAX = 200;
const ID_MAX = 200;

export type NotifyKind = "needs" | "messages";
export type NotifyEvent = { id: string; title: string; body: string; channel: string; session: string; kind: NotifyKind };

// Control characters other than \n become a space.
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/g;

function clean(s: string, max: number): string {
  const t = s.replace(CONTROL, " ").trim();
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

export type NotifyTarget = { session: string } | { web: string };

export function notifyTarget(e: NotifyEvent, jamUrl: string): NotifyTarget {
  return e.session !== "" ? { session: e.session } : { web: webUrl(jamUrl, e.channel) };
}

export function targetLink(t: NotifyTarget, scheme: string): string {
  return "session" in t ? `${scheme}://aethons.jam/session/${t.session}` : t.web;
}
