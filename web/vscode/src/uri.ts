// Routes for the extension's URI handler: <scheme>://aethons.jam/<path>.

// SESSION_ID_RE is internal/ident's session id.
export const SESSION_ID_RE = /^ses_[0-7][0-9a-hjkmnp-tv-z]{25}$/;
// CHANNEL_RE is internal/ident's channel id.
export const CHANNEL_RE = /^chn_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

export type Route =
  | { kind: "auth"; query: URLSearchParams }
  | { kind: "session"; id: string }
  | { kind: "hub"; jam: string } // jam: the launcher's Jam URL ("" = none)
  | { kind: "go"; channel: string; session: string }
  | { kind: "bad"; reason: string };

export function route(path: string, query: string): Route {
  if (path === "/auth") return { kind: "auth", query: new URLSearchParams(query) };
  if (path === "/hub") return { kind: "hub", jam: new URLSearchParams(query).get("jam") ?? "" };
  const go = /^\/go\/([^/]+)$/.exec(path);
  if (go) {
    const channel = go[1]!;
    const session = new URLSearchParams(query).get("session") ?? "";
    if (!CHANNEL_RE.test(channel)) return { kind: "bad", reason: "not a conversation id" };
    if (session !== "" && !SESSION_ID_RE.test(session)) return { kind: "bad", reason: "not a session id" };
    return { kind: "go", channel, session };
  }
  const m = /^\/session\/([^/]+)$/.exec(path);
  if (m) {
    const id = m[1]!;
    return SESSION_ID_RE.test(id) ? { kind: "session", id } : { kind: "bad", reason: "not a session id" };
  }
  return { kind: "bad", reason: `unknown path ${path}` };
}
