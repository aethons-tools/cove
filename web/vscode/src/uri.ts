// Routes for the extension's URI handler: <scheme>://aethons.jam/<path>.

// SESSION_ID_RE is internal/ident's session id.
export const SESSION_ID_RE = /^ses_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

export type Route =
  | { kind: "auth"; query: URLSearchParams }
  | { kind: "session"; id: string }
  | { kind: "hub" }
  | { kind: "bad"; reason: string };

export function route(path: string, query: string): Route {
  if (path === "/auth") return { kind: "auth", query: new URLSearchParams(query) };
  if (path === "/hub") return { kind: "hub" };
  const m = /^\/session\/([^/]+)$/.exec(path);
  if (m) {
    const id = m[1]!;
    return SESSION_ID_RE.test(id) ? { kind: "session", id } : { kind: "bad", reason: "not a session id" };
  }
  return { kind: "bad", reason: `unknown path ${path}` };
}
