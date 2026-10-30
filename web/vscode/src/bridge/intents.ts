// Open intents from a pane become an action the extension host performs.
// Everything here treats the intent as untrusted input.
import type { OpenIntent } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { checkPath, ID_RE } from "./router.js";

export const ANCHOR_RE = /^ask-[A-Za-z0-9_-]{1,80}$/;
// LEGACY_RE is a History row's channel id (userui's legacyPrefix): read-only, never a session's.
export const LEGACY_RE = /^legacy:[\x21-\x7e]{1,256}$/;

// forwardPort's url is the fallback: the preview's own web URL.
export type IntentAction =
  | { openExternal: string }
  | { openBoard: string }
  | { forwardPort: number; inBrowser: boolean; url: string }
  | { revealConversation: string | null }
  | { openSession: string }
  | { goTo: { channel: string; session: string } };

const EXTERNAL_SCHEMES = new Set(["http:", "https:", "mailto:"]);

export function intentAction(i: OpenIntent, jamUrl: string, windowStudio?: string): IntentAction | Error {
  switch (i.kind) {
    case "board":
      if (typeof i.id !== "string" || !ID_RE.brd.test(i.id)) return new Error("Not a board id.");
      return { openBoard: i.id };
    case "conversation":
      if (typeof i.channel !== "string" || !ID_RE.chn.test(i.channel)) return new Error("Not a conversation id.");
      return { openExternal: `${jamUrl}/app/user/c/${i.channel}` };
    case "web": {
      if (typeof i.path !== "string") return new Error("Not a path.");
      const u = checkPath(i.path, jamUrl);
      return u instanceof Error ? u : { openExternal: u };
    }
    case "external": {
      if (typeof i.url !== "string") return new Error("Not a URL.");
      let u: URL;
      try {
        u = new URL(i.url);
      } catch {
        return new Error("Not a URL.");
      }
      if (!EXTERNAL_SCHEMES.has(u.protocol)) return new Error(`Refusing to open a ${u.protocol} link.`);
      return { openExternal: u.toString() };
    }
    case "preview": {
      if (typeof i.session !== "string" || !ID_RE.ses.test(i.session)) return new Error("Not a session id.");
      if (typeof i.port !== "number" || !Number.isInteger(i.port) || i.port < 1 || i.port > 65535) return new Error("Not a port.");
      if (typeof i.url !== "string") return new Error("Not a URL.");
      let u: URL;
      try {
        u = new URL(i.url);
      } catch {
        return new Error("Not a URL.");
      }
      if (u.protocol !== "http:" && u.protocol !== "https:") return new Error(`Refusing to open a ${u.protocol} link.`);
      const url = u.toString();
      if (windowStudio !== undefined && i.session === windowStudio) return { forwardPort: i.port, inBrowser: i.inBrowser === true, url };
      return { openExternal: url };
    }
    case "reveal":
      if (i.anchor === undefined || i.anchor === null) return { revealConversation: null };
      if (typeof i.anchor !== "string" || !ANCHOR_RE.test(i.anchor)) return new Error("Not an anchor.");
      return { revealConversation: i.anchor };
    case "go": {
      const legacy = typeof i.channel === "string" && LEGACY_RE.test(i.channel);
      if (typeof i.channel !== "string" || !(ID_RE.chn.test(i.channel) || legacy)) return new Error("Not a conversation id.");
      if (typeof i.session !== "string" || (i.session !== "" && (legacy || !ID_RE.ses.test(i.session)))) return new Error("Not a session id.");
      return { goTo: { channel: i.channel, session: i.session } };
    }
    case "session":
      if (typeof i.id !== "string" || !ID_RE.ses.test(i.id)) return new Error("Not a session id.");
      return { openSession: i.id };
    default:
      return new Error("Unknown intent.");
  }
}
