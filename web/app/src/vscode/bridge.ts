// The pane's end of the webview bridge: every request goes to the extension
// host as a message, answered by id; state and live events come back the
// same way. install points the web app's seams (transport, uploader, blob
// resolver, live source, focus sink) at a Bridge.
import type { BridgeRequest, BridgeResponse, ExtToPane, PaneState, PaneToExt, ThinkingEventName } from "../bridge/protocol";
import { ApiError, setTransport, type Raw, type Transport } from "../api/client";
import { setUploader, uploadOutcome, type Uploader } from "../composer/upload";
import { setBlobResolver, type BlobResolver } from "../blobs";
import { setLiveSource, type LiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { setFeedOpener, type FeedOpener, type FeedSource } from "../session/thinking/useThinkingFeed";
import { SES_RE, STREAM_RE } from "./ids";

export interface VsApi { postMessage(m: PaneToExt): void; setState(s: unknown): void }

type Pending = { resolve: (r: BridgeResponse) => void; reject: (e: unknown) => void; onProgress?: (loaded: number, total: number) => void };

export type ThinkingMsg = { event: ThinkingEventName; data: string } | { error: true; closed: boolean };

const aborted = () => new DOMException("aborted", "AbortError");

export class Bridge {
  private next = 1;
  private pending = new Map<number, Pending>();
  private states = new Set<(s: PaneState) => void>();
  private lives = new Set<(event: string, data: string) => void>();
  private thinkings = new Map<number, Set<(m: ThinkingMsg) => void>>();
  private scrolls = new Set<(anchor: string) => void>();
  private last: PaneState | undefined;
  private nextSub = 1;

  constructor(private api: VsApi, win: Window) {
    win.addEventListener("message", (e) => this.receive((e as MessageEvent<ExtToPane>).data));
  }

  private receive(m: ExtToPane | undefined) {
    if (!m || typeof m !== "object") return;
    switch (m.type) {
      case "state":
        this.last = m.state;
        for (const cb of [...this.states]) cb(m.state);
        break;
      case "response": {
        const p = this.pending.get(m.id);
        if (!p) return;
        this.pending.delete(m.id);
        p.resolve(m.res);
        break;
      }
      case "progress":
        this.pending.get(m.id)?.onProgress?.(m.loaded, m.total);
        break;
      case "live":
        for (const cb of [...this.lives]) cb(m.event, m.data);
        break;
      case "thinking":
        this.toSub(m.sub, { event: m.event, data: m.data });
        break;
      case "thinkingError":
        this.toSub(m.sub, { error: true, closed: m.closed });
        break;
      case "scrollTo":
        for (const cb of [...this.scrolls]) cb(m.anchor);
        break;
    }
  }

  private toSub(sub: number, m: ThinkingMsg) {
    for (const cb of [...(this.thinkings.get(sub) ?? [])]) cb(m);
  }

  // newSub allocates the next Thinking subscription id.
  newSub(): number {
    return this.nextSub++;
  }

  onThinking(sub: number, cb: (m: ThinkingMsg) => void): () => void {
    let set = this.thinkings.get(sub);
    if (!set) this.thinkings.set(sub, (set = new Set()));
    set.add(cb);
    return () => {
      set.delete(cb);
      if (set.size === 0 && this.thinkings.get(sub) === set) this.thinkings.delete(sub);
    };
  }

  onScrollTo(cb: (anchor: string) => void): () => void {
    this.scrolls.add(cb);
    return () => { this.scrolls.delete(cb); };
  }

  // request sends one request; abort tells the host to drop it and rejects
  // res with an AbortError.
  request(req: BridgeRequest, onProgress?: (loaded: number, total: number) => void): { res: Promise<BridgeResponse>; abort: () => void } {
    const id = this.next++;
    const res = new Promise<BridgeResponse>((resolve, reject) => this.pending.set(id, { resolve, reject, onProgress }));
    this.post({ type: "request", id, req });
    const abort = () => {
      const p = this.pending.get(id);
      if (!p) return;
      this.pending.delete(id);
      this.post({ type: "abort", id });
      p.reject(aborted());
    };
    return { res, abort };
  }

  post(m: PaneToExt): void {
    this.api.postMessage(m);
  }

  // onState hears every state, starting with the last one if any.
  onState(cb: (s: PaneState) => void): () => void {
    this.states.add(cb);
    if (this.last) cb(this.last);
    return () => { this.states.delete(cb); };
  }

  onLive(cb: (event: string, data: string) => void): () => void {
    this.lives.add(cb);
    return () => { this.lives.delete(cb); };
  }
}

// failure is the ApiError for a response that isn't the server's.
function failure(res: Exclude<BridgeResponse, { kind: "http" }>): ApiError {
  switch (res.kind) {
    case "unreachable": return new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
    case "signedOut": return new ApiError(401, "signed_out", "Your sign-in has expired.");
    case "refused": return new ApiError(400, "bad_request", "That isn't a Jam user-area request.");
  }
}

export function bridgeTransport(b: Bridge): Transport {
  return async ({ method, url, json, form }) => {
    if (form) throw new ApiError(0, "unavailable", "This isn't available in VS Code yet.");
    const req: BridgeRequest = { method, path: url };
    if (json !== undefined) req.json = json;
    const res = await b.request(req).res;
    if (res.kind !== "http") throw failure(res);
    const raw: Raw = { status: res.status, contentType: res.contentType, redirected: false };
    if (res.json !== undefined) raw.json = res.json;
    if (res.text !== undefined) raw.text = res.text;
    return raw;
  };
}

export function bridgeUploader(b: Bridge): Uploader {
  return (file, onProgress) => {
    let stop = false;
    let abort = () => {};
    const done = (async () => {
      const bytes = await file.arrayBuffer();
      if (stop) throw aborted();
      const r = b.request({
        method: "POST", path: "/app/user/uploads", bytes, progress: true,
        headers: { "Content-Type": file.type || "application/octet-stream", "X-Filename": encodeURIComponent(file.name) },
      }, (loaded, total) => { if (total > 0) onProgress((100 * loaded) / total); });
      abort = r.abort;
      const res = await r.res;
      if (res.kind !== "http") throw new Error(failure(res).message);
      const out = uploadOutcome(res.status, res.text ?? (res.json === undefined ? "" : JSON.stringify(res.json)));
      if (typeof out === "string") return out;
      throw out;
    })();
    return { done, abort: () => { stop = true; abort(); } };
  };
}

// BLOB_CACHE_MAX is how many blob: URLs the pane keeps; the oldest is revoked.
export const BLOB_CACHE_MAX = 200;

// blobResolver fetches a blob's bytes through the bridge as an object URL,
// cached per path (a failure isn't cached). A board's live event drops its
// thumbnail, so the next card to show it fetches the new one.
export function blobResolver(b: Bridge): BlobResolver {
  const cache = new Map<string, Promise<string>>();
  const drop = (path: string) => {
    const p = cache.get(path);
    if (!p) return;
    cache.delete(path);
    p.then((u) => URL.revokeObjectURL(u), () => {});
  };
  b.onLive((event, data) => {
    if (event === "board") drop(`/app/user/boards/${encodeURIComponent(data)}/thumb.png`);
  });
  return (path) => {
    const hit = cache.get(path);
    if (hit) {
      cache.delete(path); // most recently used last
      cache.set(path, hit);
      return hit;
    }
    const p = b.request({ method: "GET", path, accept: "bytes" }).res.then((res) => {
      if (res.kind !== "http") throw failure(res);
      if (res.status < 200 || res.status >= 300 || !res.bytes) throw new Error(`blob ${res.status}`);
      return URL.createObjectURL(new Blob([res.bytes], { type: res.contentType }));
    });
    cache.set(path, p);
    p.catch(() => { if (cache.get(path) === p) cache.delete(path); });
    while (cache.size > BLOB_CACHE_MAX) drop(cache.keys().next().value!);
    return p;
  };
}

// liveSource is the host's fan-out of its one event stream: the pane is
// "open" at once (the host owns reconnects, and forwards each later open).
export function liveSource(b: Bridge): LiveSource {
  return (on) => {
    on("open", "");
    return b.onLive(on);
  };
}

// parseThinkingUrl is a Thinking feed's session and stream (""= newest), or
// undefined for any other URL.
export function parseThinkingUrl(url: string): { session: string; stream: string } | undefined {
  const m = /^\/app\/user\/session\/([^/?#]+)\/thinking\/events(?:\?stream=([^&#]*))?$/.exec(url);
  if (!m || !SES_RE.test(m[1]!)) return undefined;
  const stream = m[2] ?? "";
  if (m[2] !== undefined && !STREAM_RE.test(stream)) return undefined;
  return { session: m[1]!, stream };
}

// bridgeFeedOpener is an EventSource-shaped feed over the bridge: the host
// owns the one SSE connection and its retries; this only relays by sub id.
export function bridgeFeedOpener(b: Bridge): FeedOpener {
  return (url): FeedSource => {
    const listeners = new Map<string, Set<(e: { data: string }) => void>>();
    const fire = (type: string, data: string) => {
      for (const fn of [...(listeners.get(type) ?? [])]) fn({ data });
    };
    let readyState = 0;
    const src: FeedSource = {
      get readyState() { return readyState; },
      addEventListener(type, fn) {
        let set = listeners.get(type);
        if (!set) listeners.set(type, (set = new Set()));
        set.add(fn);
      },
      close() {},
    };
    const target = parseThinkingUrl(url);
    if (!target) {
      readyState = 2;
      queueMicrotask(() => fire("error", ""));
      return src;
    }
    let closed = false;
    const sub = b.newSub();
    const off = b.onThinking(sub, (m) => {
      if ("error" in m) {
        readyState = m.closed ? 2 : 0;
        fire("error", "");
        return;
      }
      if (m.event === "open") readyState = 1;
      fire(m.event, m.data);
    });
    b.post({ type: "thinkingOpen", sub, session: target.session, stream: target.stream });
    src.close = () => {
      if (closed) return;
      closed = true;
      readyState = 2;
      off();
      b.post({ type: "thinkingClose", sub });
    };
    return src;
  };
}

// install points the web app's seams at b. host.signIn is the pane's sign-in
// (the Pane offers it through HostContext itself).
export function install(b: Bridge, host: { signIn: () => void }): void {
  void host;
  setTransport(bridgeTransport(b));
  setUploader(bridgeUploader(b));
  setBlobResolver(blobResolver(b));
  setLiveSource(liveSource(b));
  setFeedOpener(bridgeFeedOpener(b));
  setFocusSink((channel) => b.post({ type: "viewing", channel }));
}
