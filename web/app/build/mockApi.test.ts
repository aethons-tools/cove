import { describe, expect, it, vi } from "vitest";
import { mockApi } from "./mockApi";

type Handler = (req: { method: string; url: string }, res: Res, next: () => void) => Promise<void>;
type Res = { statusCode: number; body: string | Buffer; headers: Record<string, string>; setHeader: (k: string, v: string) => void; end: (b: string | Buffer) => void };

// serve feeds one request through the plugin's middleware, with mocks as the
// module's, and answers the response.
async function serve(plugin: ReturnType<typeof mockApi>, mocks: Record<string, unknown>, method: string, url: string) {
  let handler: Handler | undefined;
  const server = {
    middlewares: { use: (h: Handler) => { handler = h; } },
    ssrLoadModule: () => Promise.resolve({ mocks }),
    config: { logger: { warn: vi.fn() } },
  };
  (plugin.configureServer as (s: unknown) => void)(server);
  const res: Res = { statusCode: 200, body: "", headers: {}, setHeader(k, v) { this.headers[k] = v; }, end(b) { this.body = b; } };
  const next = vi.fn();
  await handler!({ method, url }, res, next);
  return { res, next, warn: server.config.logger.warn };
}

describe("the mock API", () => {
  it("answers a mocked route, with or without its query", async () => {
    const p = mockApi("/m.ts");
    const mocks = { "GET /app/admin/api/tree": { nodes: [] }, "GET /app/user/api/x?a=1": { a: 1 } };
    expect((await serve(p, mocks, "GET", "/app/admin/api/tree?depth=2")).res.body).toBe('{"nodes":[]}');
    expect((await serve(p, mocks, "GET", "/app/user/api/x?a=1")).res.body).toBe('{"a":1}');
    expect(p.api.misses).toEqual([]);
  });

  it("records a route with no mock, so the shot fails on it", async () => {
    const p = mockApi("/m.ts");
    const { res, warn } = await serve(p, {}, "GET", "/app/admin/api/projects/zeta?section=intercom");
    expect(res.statusCode).toBe(404);
    expect(warn).toHaveBeenCalledWith("mock-api: no mock for GET /app/admin/api/projects/zeta?section=intercom");
    expect(p.api.misses).toEqual(["GET /app/admin/api/projects/zeta?section=intercom"]);
  });

  it("answers an EventStream mock as Server-Sent Events", async () => {
    const p = mockApi("/m.ts");
    const mocks = { "GET /app/user/session/s1/thinking/events": { eventStream: [{ event: "stream", data: "a" }, { event: "ev", data: "{}" }] } };
    const { res } = await serve(p, mocks, "GET", "/app/user/session/s1/thinking/events");
    expect(res.body).toBe("retry: 86400000\n\nevent: stream\ndata: a\n\nevent: ev\ndata: {}\n\n");
    expect(p.api.misses).toEqual([]);
  });

  it("answers a board's elements and thumbnail, a data: URL as its bytes", async () => {
    const p = mockApi("/m.ts");
    const mocks = { "GET /app/user/boards/b1/elements": { seq: 1, elements: [] }, "GET /app/user/boards/b1/thumb.png": "data:image/png;base64,iVBORw==" };
    expect((await serve(p, mocks, "GET", "/app/user/boards/b1/elements?since=0")).res.body).toBe('{"seq":1,"elements":[]}');
    const { res } = await serve(p, mocks, "GET", "/app/user/boards/b1/thumb.png");
    expect(res.headers["Content-Type"]).toBe("image/png");
    expect(res.body).toEqual(Buffer.from("iVBORw==", "base64"));
  });

  it("leaves everything outside the API to Vite", async () => {
    const p = mockApi("/m.ts");
    const { next } = await serve(p, {}, "GET", "/app/admin/config/projects/zeta");
    expect(next).toHaveBeenCalled();
    expect((await serve(p, {}, "GET", "/app/user/boards/b1")).next).toHaveBeenCalled();
    expect(p.api.misses).toEqual([]);
  });
});
