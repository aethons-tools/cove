// The webview request proxy. The pane is less trusted than the extension
// host, which holds the token: every request is checked against the /app/user/
// allow-list and sent with only the headers the host chooses.
import type { BridgeRequest, ExtToPane, PaneToExt } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };

const ID_BODY = "_[0-7][0-9a-hjkmnp-tv-z]{25}$";
export const ID_RE: Record<"ses" | "chn" | "brd" | "blb", RegExp> = {
  ses: new RegExp(`^ses${ID_BODY}`),
  chn: new RegExp(`^chn${ID_BODY}`),
  brd: new RegExp(`^brd${ID_BODY}`),
  blb: new RegExp(`^blb${ID_BODY}`),
};

const PREFIX = "/app/user/";
const MAX_PATH = 2048;
// eslint-disable-next-line no-control-regex
const BAD_CHARS = /[\\#\u0000-\u001f\u007f]|\/\/|%2f|%2e|%5c/i;

// checkPath returns the absolute URL to fetch, or an Error naming why not.
// opts.streams allows the event streams, which only the host itself opens.
export function checkPath(path: string, jamUrl: string, opts: { streams?: boolean } = {}): string | Error {
  if (typeof path !== "string" || path.length > MAX_PATH) return new Error("Path too long.");
  if (!path.startsWith(PREFIX)) return new Error("Not a Jam user-area path.");
  if (BAD_CHARS.test(path)) return new Error("Path has a forbidden character.");
  const pathPart = path.split("?", 1)[0]!;
  if (pathPart.split("/").some((s) => s === "." || s === "..")) return new Error("Path has a dot segment.");
  let u: URL;
  let base: URL;
  try {
    base = new URL(jamUrl);
    u = new URL(path, base);
  } catch {
    return new Error("Not a URL.");
  }
  if (u.origin !== base.origin || u.pathname !== pathPart || !u.pathname.startsWith(PREFIX)) {
    return new Error("Path escapes the Jam user area.");
  }
  if (!opts.streams) {
    const p = u.pathname.replace(/\/+$/, "");
    if (p === "/app/user/events" || p.endsWith("/thinking/events")) return new Error("Streams aren't proxied.");
  }
  return u.toString();
}

export const UPLOAD_CHUNK = 64 * 1024;
const TEXT_CAP = 64 * 1024;

export type RouterDeps = {
  session: () => Promise<{ url: string; token: string } | undefined>;
  fetch: typeof fetch;
  onSignedOut: () => void;
};

const METHODS = new Set(["GET", "POST", "DELETE"]);

export class Router {
  private inflight = new Map<number, AbortController>();
  private notified = new Set<string>();

  constructor(private d: RouterDeps) {}

  handle(m: PaneToExt, reply: (m: ExtToPane) => void): void {
    if (m.type === "abort") {
      this.inflight.get(m.id)?.abort();
      this.inflight.delete(m.id);
    } else if (m.type === "request") {
      void this.request(m.id, m.req, reply);
    }
  }

  private async request(id: number, req: BridgeRequest, reply: (m: ExtToPane) => void): Promise<void> {
    const ac = new AbortController();
    this.inflight.set(id, ac);
    const done = (res: Extract<ExtToPane, { type: "response" }>["res"]): void => {
      if (this.inflight.get(id) === ac) this.inflight.delete(id);
      if (!ac.signal.aborted) reply({ type: "response", id, res });
    };
    try {
      if (!req || !METHODS.has(req.method)) return done({ kind: "refused", reason: "method" });
      const s = await this.d.session();
      if (ac.signal.aborted) return;
      if (!s) return done({ kind: "signedOut" });
      const url = checkPath(req.path, s.url);
      if (url instanceof Error) return done({ kind: "refused", reason: url.message });

      const headers: Record<string, string> = {
        Authorization: `Bearer ${s.token}`,
        Accept: req.accept === "bytes" ? "*/*" : "application/json",
      };
      for (const [k, v] of Object.entries(req.headers ?? {})) {
        const lk = k.toLowerCase();
        if ((lk === "content-type" || lk === "x-filename") && typeof v === "string") headers[lk] = v;
      }
      const init: RequestInit & { duplex?: "half" } = {
        method: req.method,
        headers,
        redirect: "manual",
        signal: ac.signal,
      };
      if (req.bytes !== undefined) {
        const bytes = new Uint8Array(req.bytes);
        headers["Content-Length"] = String(bytes.byteLength);
        init.body = this.stream(bytes, req.progress ? (loaded) => reply({ type: "progress", id, loaded, total: bytes.byteLength }) : undefined, ac);
        init.duplex = "half";
      } else if (req.json !== undefined) {
        if (!Object.keys(headers).some((k) => k.toLowerCase() === "content-type")) headers["content-type"] = "application/json";
        init.body = JSON.stringify(req.json);
      }

      let res: Response;
      try {
        res = await this.d.fetch(url, init);
      } catch {
        return done({ kind: "unreachable" });
      }
      if (res.status === 401) {
        if (!this.notified.has(s.token)) {
          this.notified.add(s.token);
          this.d.onSignedOut();
        }
        return done({ kind: "signedOut" });
      }
      const contentType = res.headers.get("content-type") ?? "";
      try {
        if (contentType.toLowerCase().startsWith("application/json")) {
          const json = (await res.json()) as unknown;
          return done({ kind: "http", status: res.status, contentType, json });
        }
        if (req.accept === "bytes") {
          return done({ kind: "http", status: res.status, contentType, bytes: await res.arrayBuffer() });
        }
        const text = (await res.text()).slice(0, TEXT_CAP);
        return done({ kind: "http", status: res.status, contentType, text });
      } catch {
        return done({ kind: "unreachable" });
      }
    } catch {
      done({ kind: "unreachable" });
    }
  }

  private stream(bytes: Uint8Array, progress: ((loaded: number) => void) | undefined, ac: AbortController): ReadableStream<Uint8Array> {
    let off = 0;
    return new ReadableStream<Uint8Array>(
      {
        pull: (c) => {
          if (ac.signal.aborted) return c.error(new Error("aborted"));
          if (off >= bytes.byteLength) return c.close();
          const end = Math.min(off + UPLOAD_CHUNK, bytes.byteLength);
          c.enqueue(bytes.slice(off, end));
          off = end;
          progress?.(off);
        },
      },
      { highWaterMark: 0 },
    );
  }
}
