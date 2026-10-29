import { describe, expect, it, vi } from "vitest";
import type { ExtToPane } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { checkPath, Router, UPLOAD_CHUNK } from "../src/bridge/router.js";

const JAM = "https://jam.example.com";
const SES = "ses_0123456789abcdefghjkmnpqrs";
const TOK = "tok-1";

describe("checkPath", () => {
  const bad = [
    `/me/studios/${SES}/ssh/cert`,
    "/app/admin/api/users",
    "/app/user/../../me/studios",
    "/app/user/./api",
    "/app/user/%2e%2e/admin",
    "/app/user/%2E%2E/admin",
    "/app/user/%2F",
    "/app/user/%5C",
    "/app/user/a\\b",
    "//evil.example/x",
    "https://evil.example/app/user/x",
    "/app/user/api/x#y",
    "/app/user/api/\u0000",
    "/app/user/api/\n",
    "/app/user//x",
    "/app/user/events",
    "/app/user/events?x=1",
    `/app/user/session/${SES}/thinking/events`,
    "/app/user/" + "a".repeat(2049 - "/app/user/".length),
  ];
  for (const p of bad) {
    it(`refuses ${JSON.stringify(p.slice(0, 60))}`, () => {
      expect(checkPath(p, JAM)).toBeInstanceOf(Error);
    });
  }
  it("accepts a conversation read", () => {
    expect(checkPath("/app/user/api/conversations/chn_a?before=5", JAM)).toBe(
      "https://jam.example.com/app/user/api/conversations/chn_a?before=5",
    );
  });
  it("allows streams only when asked", () => {
    expect(checkPath("/app/user/events", JAM, { streams: true })).toBe("https://jam.example.com/app/user/events");
  });
});

function setup(fetchImpl: (url: string, init: RequestInit) => Promise<Response>, token = TOK) {
  const f = vi.fn(fetchImpl);
  const onSignedOut = vi.fn();
  const sent: ExtToPane[] = [];
  const router = new Router({
    session: async () => ({ url: JAM, token }),
    fetch: f as unknown as typeof fetch,
    onSignedOut,
  });
  const reply = (m: ExtToPane) => sent.push(m);
  const settle = () => new Promise((r) => setTimeout(r, 10));
  return { f, onSignedOut, sent, router, reply, settle };
}
const json = (b: unknown, status = 200) =>
  new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });

describe("Router", () => {
  it("forwards bearer, Accept and only allowed headers", async () => {
    const t = setup(async () => json({ ok: 1 }));
    t.router.handle(
      {
        type: "request",
        id: 1,
        req: {
          method: "POST",
          path: "/app/user/api/x",
          json: { a: 1 },
          headers: { Authorization: "x", Cookie: "y", "X-Filename": "a", Host: "evil" },
        },
      },
      t.reply,
    );
    await t.settle();
    const init = t.f.mock.calls[0]![1];
    const h = new Headers(init.headers);
    expect(h.get("authorization")).toBe(`Bearer ${TOK}`);
    expect(h.get("x-filename")).toBe("a");
    expect(h.get("cookie")).toBeNull();
    expect(h.get("host")).toBeNull();
    expect(h.get("accept")).toBe("application/json");
    expect(init.redirect).toBe("manual");
    expect(t.f.mock.calls[0]![0]).toBe(`${JAM}/app/user/api/x`);
  });

  it("method PUT is refused", async () => {
    const t = setup(async () => json({}));
    t.router.handle({ type: "request", id: 1, req: { method: "PUT" as "GET", path: "/app/user/api/x" } }, t.reply);
    await t.settle();
    expect(t.f).not.toHaveBeenCalled();
    expect(t.sent[0]).toMatchObject({ type: "response", id: 1, res: { kind: "refused" } });
  });

  it("refused path never calls fetch", async () => {
    const t = setup(async () => json({}));
    for (const [i, p] of [`/me/studios/${SES}/ssh/cert`, "/app/admin/api/users", "//evil.example/x", "https://evil.example/"].entries()) {
      t.router.handle({ type: "request", id: i, req: { method: "GET", path: p } }, t.reply);
    }
    await t.settle();
    expect(t.f).not.toHaveBeenCalled();
    expect(t.sent).toHaveLength(4);
    expect(t.sent.every((m) => m.type === "response" && m.res.kind === "refused")).toBe(true);
  });

  it("no session replies signedOut without fetch", async () => {
    const f = vi.fn();
    const sent: ExtToPane[] = [];
    const r = new Router({ session: async () => undefined, fetch: f as unknown as typeof fetch, onSignedOut: vi.fn() });
    r.handle({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/x" } }, (m) => sent.push(m));
    await new Promise((x) => setTimeout(x, 5));
    expect(f).not.toHaveBeenCalled();
    expect(sent).toEqual([{ type: "response", id: 1, res: { kind: "signedOut" } }]);
  });

  it("401 replies signedOut and calls onSignedOut once for two concurrent requests", async () => {
    const t = setup(async () => new Response("no", { status: 401 }));
    t.router.handle({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/a" } }, t.reply);
    t.router.handle({ type: "request", id: 2, req: { method: "GET", path: "/app/user/api/b" } }, t.reply);
    await t.settle();
    expect(t.onSignedOut).toHaveBeenCalledTimes(1);
    expect(t.sent.map((m) => m.type === "response" && m.res.kind)).toEqual(["signedOut", "signedOut"]);
  });

  it("fetch throw replies unreachable", async () => {
    const t = setup(async () => {
      throw new Error("down");
    });
    t.router.handle({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/a" } }, t.reply);
    await t.settle();
    expect(t.sent).toEqual([{ type: "response", id: 1, res: { kind: "unreachable" } }]);
  });

  it("json, text and bytes bodies", async () => {
    const t = setup(async (url) => {
      if (url.endsWith("/j")) return json({ a: 1 }, 404);
      if (url.endsWith("/t")) return new Response("x".repeat(100_000), { status: 500, headers: { "content-type": "text/plain" } });
      return new Response(new Uint8Array([1, 2, 3]), { headers: { "content-type": "image/png" } });
    });
    t.router.handle({ type: "request", id: 1, req: { method: "GET", path: "/app/user/j" } }, t.reply);
    t.router.handle({ type: "request", id: 2, req: { method: "GET", path: "/app/user/t" } }, t.reply);
    t.router.handle({ type: "request", id: 3, req: { method: "GET", path: "/app/user/b", accept: "bytes" } }, t.reply);
    await t.settle();
    const by = (id: number) => t.sent.find((m) => m.type === "response" && m.id === id) as Extract<ExtToPane, { type: "response" }>;
    expect(by(1).res).toMatchObject({ kind: "http", status: 404, json: { a: 1 } });
    const r2 = by(2).res as { text: string; status: number };
    expect(r2.status).toBe(500);
    expect(r2.text.length).toBe(64 * 1024);
    const r3 = by(3).res as { bytes: ArrayBuffer };
    expect([...new Uint8Array(r3.bytes)]).toEqual([1, 2, 3]);
  });

  it("upload posts progress per 64 KiB chunk and a final 201", async () => {
    const t = setup(async (_u, init) => {
      const reader = (init.body as ReadableStream<Uint8Array>).getReader();
      let n = 0;
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        n += value.byteLength;
      }
      expect(n).toBe(200 * 1024);
      expect(new Headers(init.headers).get("content-length")).toBe(String(200 * 1024));
      expect((init as { duplex?: string }).duplex).toBe("half");
      return json({ id: "blb" }, 201);
    });
    const bytes = new ArrayBuffer(200 * 1024);
    t.router.handle(
      { type: "request", id: 7, req: { method: "POST", path: "/app/user/api/blobs", bytes, progress: true, headers: { "x-filename": "a.png" } } },
      t.reply,
    );
    await t.settle();
    expect(UPLOAD_CHUNK).toBe(65536);
    const prog = t.sent.filter((m) => m.type === "progress").map((m) => (m as { loaded: number }).loaded);
    expect(prog).toEqual([65536, 131072, 196608, 204800]);
    const last = t.sent.at(-1)!;
    expect(last).toMatchObject({ type: "response", id: 7, res: { kind: "http", status: 201 } });
  });

  it("abort aborts the fetch signal and sends no response", async () => {
    let signal: AbortSignal | undefined;
    const t = setup(
      (_u, init) =>
        new Promise((_res, rej) => {
          signal = init.signal as AbortSignal;
          signal.addEventListener("abort", () => rej(new Error("aborted")));
        }),
    );
    t.router.handle({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/a" } }, t.reply);
    await t.settle();
    t.router.handle({ type: "abort", id: 1 }, t.reply);
    await t.settle();
    expect(signal!.aborted).toBe(true);
    expect(t.sent).toEqual([]);
  });
});
