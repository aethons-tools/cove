// Open intents from a pane become an action the extension host performs.
// Everything here treats the intent as untrusted input.
import type { OpenIntent } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { checkPath, ID_RE } from "./router.js";

export type IntentAction = { openExternal: string } | { command: "jam.focusSession" };

const EXTERNAL_SCHEMES = new Set(["http:", "https:", "mailto:"]);

export function intentAction(i: OpenIntent, jamUrl: string): IntentAction | Error {
  switch (i.kind) {
    case "board":
      if (typeof i.id !== "string" || !ID_RE.brd.test(i.id)) return new Error("Not a board id.");
      return { openExternal: `${jamUrl}/app/user/boards/${i.id}` };
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
    case "focusSession":
      return { command: "jam.focusSession" };
    default:
      return new Error("Unknown intent.");
  }
}
