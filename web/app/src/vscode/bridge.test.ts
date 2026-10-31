import { afterEach, describe, expect, it, vi } from "vitest";
import type { BridgeResponse, PaneToExt } from "../bridge/protocol";
import { apiAt, apiGet, ApiError, setTransport } from "../api/client";
import { setUploader, upload } from "../composer/upload";
import { setBlobResolver } from "../blobs";
import { setLiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { setFeedOpener } from "../session/thinking/useThinkingFeed";
import { BLOB_CACHE_MAX, blobResolver, Bridge, bridgeFeedOpener, install, liveSource, parseThinkingUrl } from "./bridge";
import { fakeHost, json } from "./fakeHost";

// manual is a host that answers only when the test says.
function manual() {
  const posted: PaneToExt[] = [];
  const win = new EventTarget() as unknown as Window;
  const b = new Bridge({ postMessage: (m) => posted.push(m), setState() {} }, win);
  const reply = (id: number, res: BridgeResponse) => win.dispatchEvent(new MessageEvent("message", { data: { type: "response", id, res } }));
  const progress = (id: number, loaded: number, total: number) => win.dispatchEvent(new MessageEvent("message", { data: { type: "progress", id, loaded, total } }));
  const ids = () => posted.flatMap((m) => (m.type === "request" ? [m.id] : []));
  return { b, posted, reply, progress, ids, win };
}

afterEach(() => {
  setTransport();
  setUploader();
  setBlobResolver();
  setLiveSource();
  setFocusSink();
  setFeedOpener();
  vi.restoreAllMocks();
});

describe("Bridge", () => {
  it("request correlates responses by id", async () => {
    const h = manual();
    const one = h.b.request({ method: "GET", path: "/app/user/api/a" });
    const two = h.b.request({ method: "GET", path: "/app/user/api/b" });
    const [i1, i2] = h.ids();
    expect(i1).not.toBe(i2);
    h.reply(i2!, json("b"));
    h.reply(i1!, json("a"));
    expect(await one.res).toEqual(json("a"));
    expect(await two.res).toEqual(json("b"));
  });

  it("progress reaches its request's callback only", () => {
    const h = manual();
    const p1 = vi.fn();
    const p2 = vi.fn();
    h.b.request({ method: "POST", path: "/app/user/uploads", progress: true }, p1);
    h.b.request({ method: "POST", path: "/app/user/uploads", progress: true }, p2);
    const [, i2] = h.ids();
    h.progress(i2!, 5, 10);
    expect(p2).toHaveBeenCalledWith(5, 10);
    expect(p1).not.toHaveBeenCalled();
  });

  it("abort posts {type:\"abort\", id}", async () => {
    const h = manual();
    const r = h.b.request({ method: "POST", path: "/app/user/uploads" });
    const [id] = h.ids();
    r.abort();
    expect(h.posted).toContainEqual({ type: "abort", id });
    await expect(r.res).rejects.toMatchObject({ name: "AbortError" });
  });

  it("hands the last state to a late subscriber, and live events to listeners until they stop", () => {
    const h = fakeHost();
    const b = new Bridge(h.acquireVsCodeApi(), h.win);
    const state = { signedIn: true, target: null };
    h.state(state);
    const seen = vi.fn();
    b.onState(seen);
    expect(seen).toHaveBeenCalledWith(state);
    const live = vi.fn();
    const off = b.onLive(live);
    h.live("changed", "x");
    off();
    h.live("changed", "y");
    expect(live.mock.calls).toEqual([["changed", "x"]]);
  });
});

describe("install", () => {
  it("transport maps unreachable, signedOut, refused and FormData to the ApiErrors in the copy", async () => {
    const h = fakeHost({
      "GET /app/user/api/down": { kind: "unreachable" },
      "GET /app/user/api/out": { kind: "signedOut" },
      "GET /app/user/api/no": { kind: "refused", reason: "path" },
      "GET /app/user/api/ok": json({ ok: true }),
    });
    install(new Bridge(h.acquireVsCodeApi(), h.win), { signIn: () => {} });
    const err = (p: Promise<unknown>) => p.then(() => { throw new Error("resolved"); }, (e: unknown) => e as ApiError);
    expect(await err(apiGet("down"))).toMatchObject({ status: 0, code: "unavailable", message: "Jam can't be reached. Check your connection." });
    expect(await err(apiGet("out"))).toMatchObject({ status: 401, code: "signed_out", message: "Your sign-in has expired." });
    expect(await err(apiGet("no"))).toMatchObject({ status: 400, code: "bad_request", message: "That isn't a Jam user-area request." });
    const form = await err(apiAt("/app/user/api/").postForm("x", new FormData()));
    expect(form).toBeInstanceOf(ApiError);
    expect(form).toMatchObject({ status: 0, code: "unavailable", message: "This isn't available in VS Code yet." });
    expect(await apiGet("ok")).toEqual({ ok: true });
    expect(h.requests.map((r) => r.path)).toEqual(["/app/user/api/down", "/app/user/api/out", "/app/user/api/no", "/app/user/api/ok"]);
  });

  it("the focus sink posts viewing", async () => {
    const h = fakeHost();
    install(new Bridge(h.acquireVsCodeApi(), h.win), { signIn: () => {} });
    const { renderHook } = await import("@testing-library/react");
    const { useFocusReport } = await import("../push/useFocusReport");
    const r = renderHook(() => useFocusReport("chn_a", true));
    r.unmount();
    expect(h.posted).toEqual([{ type: "viewing", channel: "chn_a" }, { type: "viewing", channel: null }]);
  });

  it("uploads the file's bytes with its type and name, reporting progress", async () => {
    let id = 0;
    const posted: PaneToExt[] = [];
    const win = new EventTarget() as unknown as Window;
    const b = new Bridge({ postMessage: (m) => { posted.push(m); if (m.type === "request") id = m.id; }, setState() {} }, win);
    install(b, { signIn: () => {} });
    const pct = vi.fn();
    const u = upload(new File(["hello"], "a b.txt", { type: "text/plain" }), pct);
    await vi.waitFor(() => expect(id).toBeGreaterThan(0));
    const req = posted.find((m) => m.type === "request")!;
    expect(req).toMatchObject({ req: { method: "POST", path: "/app/user/uploads", progress: true, headers: { "Content-Type": "text/plain", "X-Filename": "a%20b.txt" } } });
    expect(new TextDecoder().decode((req as { req: { bytes: ArrayBuffer } }).req.bytes)).toBe("hello");
    win.dispatchEvent(new MessageEvent("message", { data: { type: "progress", id, loaded: 1, total: 4 } }));
    expect(pct).toHaveBeenCalledWith(25);
    win.dispatchEvent(new MessageEvent("message", { data: { type: "response", id, res: json({ ref: "blb_1" }, 201) } }));
    expect(await u.done).toBe("blb_1");
  });

  it("an upload's abort posts abort and rejects AbortError", async () => {
    const posted: PaneToExt[] = [];
    const b = new Bridge({ postMessage: (m) => posted.push(m), setState() {} }, new EventTarget() as unknown as Window);
    install(b, { signIn: () => {} });
    const u = upload(new File(["x"], "x"), () => {});
    await vi.waitFor(() => expect(posted.some((m) => m.type === "request")).toBe(true));
    u.abort();
    await expect(u.done).rejects.toMatchObject({ name: "AbortError" });
    expect(posted.some((m) => m.type === "abort")).toBe(true);
  });

  it("resolves blobs to cached object URLs, revoking the oldest and a changed board's thumbnail", async () => {
    let n = 0;
    const create = vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:${++n}`);
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const bytes = { kind: "http", status: 200, contentType: "image/png", bytes: new ArrayBuffer(1) } as const;
    const h = fakeHost();
    const thumb = "/app/user/boards/brd_1/thumb.png";
    h.routes[`GET ${thumb}`] = bytes;
    for (let i = 0; i < BLOB_CACHE_MAX; i++) h.routes[`GET /app/user/blobs/blb_${i}`] = bytes;
    const resolve = blobResolver(new Bridge(h.acquireVsCodeApi(), h.win));
    const first = await resolve(thumb);
    expect(await resolve(thumb)).toBe(first);
    expect(h.requests).toEqual([{ method: "GET", path: thumb, accept: "bytes" }]);
    h.live("board", "brd_1");
    await vi.waitFor(() => expect(revoke).toHaveBeenCalledWith(first));
    const second = await resolve(thumb);
    expect(second).not.toBe(first);
    expect(h.requests).toHaveLength(2);
    // BLOB_CACHE_MAX more paths push the thumbnail (now the oldest) out.
    for (let i = 0; i < BLOB_CACHE_MAX; i++) await resolve(`/app/user/blobs/blb_${i}`);
    await vi.waitFor(() => expect(revoke).toHaveBeenCalledWith(second));
    expect(revoke).toHaveBeenCalledTimes(2);
    expect(create).toHaveBeenCalledTimes(BLOB_CACHE_MAX + 2);
  });

  it("a failed blob isn't cached", async () => {
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:1");
    const h = fakeHost();
    const resolve = blobResolver(new Bridge(h.acquireVsCodeApi(), h.win));
    await expect(resolve("/app/user/blobs/blb_x")).rejects.toThrow();
    h.routes["GET /app/user/blobs/blb_x"] = { kind: "http", status: 200, contentType: "image/png", bytes: new ArrayBuffer(1) };
    expect(await resolve("/app/user/blobs/blb_x")).toBe("blob:1");
  });

  it("the live source opens at once, then forwards events", () => {
    const h = fakeHost();
    const source = liveSource(new Bridge(h.acquireVsCodeApi(), h.win));
    const got: [string, string][] = [];
    const off = source((e, d) => got.push([e, d]));
    expect(got).toEqual([["open", ""]]);
    h.live("changed", "x");
    h.live("open");
    off();
    h.live("changed", "y");
    expect(got).toEqual([["open", ""], ["changed", "x"], ["open", ""]]);
  });
});

const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const STREAM = "0123456789abcdef0123456789abcdef";
const FEED = `/app/user/session/${SES}/thinking/events`;
const send = (win: Window, data: unknown) => win.dispatchEvent(new MessageEvent("message", { data }));

describe("parseThinkingUrl", () => {
  it.each<[string, { session: string; stream: string } | undefined]>([
    [FEED, { session: SES, stream: "" }],
    [`${FEED}?stream=${STREAM}`, { session: SES, stream: STREAM }],
    ["/app/user/session/x/thinking/events", undefined],
    [`${FEED}?stream=../x`, undefined],
    ["/app/user/events", undefined],
  ])("%s", (url, want) => {
    expect(parseThinkingUrl(url)).toEqual(want);
  });
});

describe("the feed opener", () => {
  it("posts thinkingOpen and dispatches by sub", () => {
    const { b, posted, win } = manual();
    const open = bridgeFeedOpener(b);
    const a = open(FEED);
    const c = open(`${FEED}?stream=${STREAM}`);
    expect(posted).toEqual([
      { type: "thinkingOpen", sub: 1, session: SES, stream: "" },
      { type: "thinkingOpen", sub: 2, session: SES, stream: STREAM },
    ]);
    expect(a.readyState).toBe(0);
    const ga = vi.fn(), gc = vi.fn();
    a.addEventListener("ev", ga);
    c.addEventListener("ev", gc);
    send(win, { type: "thinking", sub: 1, event: "open", data: "" });
    expect(a.readyState).toBe(1);
    expect(c.readyState).toBe(0);
    send(win, { type: "thinking", sub: 1, event: "ev", data: "{}" });
    expect(ga).toHaveBeenCalledWith({ data: "{}" });
    expect(gc).not.toHaveBeenCalled();
  });

  it("a closed error sets readyState 2 and fires error; a transient one sets 0", () => {
    const { b, win } = manual();
    const s = bridgeFeedOpener(b)(FEED);
    const err = vi.fn();
    s.addEventListener("error", err);
    send(win, { type: "thinking", sub: 1, event: "open", data: "" });
    send(win, { type: "thinkingError", sub: 1, closed: false });
    expect(s.readyState).toBe(0);
    send(win, { type: "thinkingError", sub: 1, closed: true });
    expect(s.readyState).toBe(2);
    expect(err).toHaveBeenCalledTimes(2);
  });

  it("close posts thinkingClose once and ignores later events", () => {
    const { b, posted, win } = manual();
    const s = bridgeFeedOpener(b)(FEED);
    const ev = vi.fn();
    s.addEventListener("ev", ev);
    s.close();
    s.close();
    expect(posted.filter((m) => m.type === "thinkingClose")).toEqual([{ type: "thinkingClose", sub: 1 }]);
    expect(s.readyState).toBe(2);
    send(win, { type: "thinking", sub: 1, event: "ev", data: "{}" });
    expect(ev).not.toHaveBeenCalled();
  });

  it("an unparseable URL is closed, errors on the next microtask and posts nothing", async () => {
    const { b, posted } = manual();
    const s = bridgeFeedOpener(b)("/app/user/events");
    expect(s.readyState).toBe(2);
    const err = vi.fn();
    s.addEventListener("error", err);
    await Promise.resolve();
    expect(err).toHaveBeenCalledTimes(1);
    expect(posted).toEqual([]);
  });
});

describe("scrollTo", () => {
  it("reaches onScrollTo", () => {
    const { b, win } = manual();
    const cb = vi.fn();
    const off = b.onScrollTo(cb);
    send(win, { type: "scrollTo", anchor: "ask-1" });
    expect(cb).toHaveBeenCalledWith("ask-1");
    off();
    send(win, { type: "scrollTo", anchor: "ask-2" });
    expect(cb).toHaveBeenCalledTimes(1);
  });
});
