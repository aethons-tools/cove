import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LIVE_STALL_MS, LiveStream, SseParser, type LiveEvent } from "../src/live.js";

describe("SseParser", () => {
  it("parses an event", () => {
    expect(new SseParser().push("event: changed\ndata: 1\n\n")).toEqual([{ event: "changed", data: "1" }]);
  });
  it("parses across chunks", () => {
    const p = new SseParser();
    expect(p.push("event: chan")).toEqual([]);
    expect(p.push("ged\ndata: 1\n")).toEqual([]);
    expect(p.push("\n")).toEqual([{ event: "changed", data: "1" }]);
  });
  it("handles CRLF and CR", () => {
    expect(new SseParser().push("event: a\r\ndata: x\r\n\r\n")).toEqual([{ event: "a", data: "x" }]);
    expect(new SseParser().push("event: a\rdata: x\r\revent: b\n")).toEqual([{ event: "a", data: "x" }]);
  });
  it("ignores comments and retry, defaults event to message", () => {
    const p = new SseParser();
    expect(p.push(": keep-alive\n\n")).toEqual([]);
    expect(p.push("retry: 100\ndata: hi\n\n")).toEqual([{ event: "message", data: "hi" }]);
  });
  it("joins multi-line data", () => {
    expect(new SseParser().push("data: a\ndata: b\n\n")).toEqual([{ event: "message", data: "a\nb" }]);
  });
});

type Conn = { push: (s: string) => void; end: () => void; init: RequestInit | undefined };

function harness(statuses: number[] = []) {
  const conns: Conn[] = [];
  const urls: string[] = [];
  const fetchFn = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    urls.push(String(url));
    const status = statuses.shift() ?? 200;
    if (status !== 200) return new Response(null, { status });
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({ start: (c) => (ctl = c) });
    init?.signal?.addEventListener("abort", () => {
      try {
        ctl.error(new Error("aborted"));
      } catch {
        /* closed */
      }
    });
    const enc = new TextEncoder();
    conns.push({ push: (s) => ctl.enqueue(enc.encode(s)), end: () => ctl.close(), init });
    return new Response(body, { status: 200, headers: { "content-type": "text/event-stream" } });
  });
  let token: string | undefined = "tok";
  const onSignedOut = vi.fn();
  const live = new LiveStream({
    session: async () => (token ? { url: "https://jam.example.com", token } : undefined),
    fetch: fetchFn as unknown as typeof fetch,
    onSignedOut,
    setTimeout: ((f: () => void, ms: number) => setTimeout(f, ms)) as unknown as typeof setTimeout,
    clearTimeout: ((t: ReturnType<typeof setTimeout>) => clearTimeout(t)) as typeof clearTimeout,
    random: () => 0.5,
  });
  return { live, fetchFn, conns, urls, onSignedOut, setToken: (t: string | undefined) => (token = t) };
}

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

describe("LiveStream", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("one fetch for three subscribers, with bearer and Accept text/event-stream", async () => {
    const h = harness();
    const got: LiveEvent[][] = [[], [], []];
    got.forEach((g) => void h.live.subscribe((e) => g.push(e)));
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    expect(h.urls[0]).toBe("https://jam.example.com/app/user/events");
    expect(h.conns[0]!.init?.headers).toEqual({ Authorization: "Bearer tok", Accept: "text/event-stream" });
    h.conns[0]!.push("event: changed\ndata: 7\n\n");
    await tick();
    for (const g of got) expect(g).toEqual([{ event: "open", data: "" }, { event: "changed", data: "7" }]);
    expect(h.live.state).toBe("open");
  });

  it("a throwing subscriber doesn't drop events or leak a connection", async () => {
    const h = harness();
    const got: LiveEvent[] = [];
    h.live.subscribe(() => {
      throw new Error("disposed");
    });
    h.live.subscribe((e) => got.push(e));
    await tick();
    h.conns[0]!.push("event: changed\ndata: 1\n\n");
    await tick(5000);
    expect(got).toEqual([{ event: "open", data: "" }, { event: "changed", data: "1" }]);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    expect(h.conns[0]!.init?.signal?.aborted).toBe(false);
  });

  it("an event with no data line is not dispatched", () => {
    expect(new SseParser().push("event: x\n\n")).toEqual([]);
  });

  it("last unsubscribe aborts", async () => {
    const h = harness();
    const u1 = h.live.subscribe(() => {});
    const u2 = h.live.subscribe(() => {});
    await tick();
    u1();
    expect(h.conns[0]!.init?.signal?.aborted).toBe(false);
    u2();
    expect(h.conns[0]!.init?.signal?.aborted).toBe(true);
    expect(h.live.state).toBe("idle");
    await tick(120_000);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
  });

  it("5xx retries at 1 s, 2 s, 4 s ... capped at 30 s", async () => {
    const h = harness([500, 500, 500, 500, 500, 500, 500]);
    h.live.subscribe(() => {});
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    for (const [i, delay] of [1000, 2000, 4000, 8000, 16000, 30000, 30000].entries()) {
      await tick(delay - 1);
      expect(h.fetchFn).toHaveBeenCalledTimes(i + 1);
      await tick(1);
      expect(h.fetchFn).toHaveBeenCalledTimes(i + 2);
    }
  });

  it("a successful open resets the backoff", async () => {
    const h = harness([500, 500]);
    h.live.subscribe(() => {});
    await tick(); // 500
    await tick(1000); // 500
    await tick(2000); // 200
    expect(h.conns).toHaveLength(1);
    h.conns[0]!.end();
    await tick(999);
    expect(h.fetchFn).toHaveBeenCalledTimes(3);
    await tick(1);
    expect(h.fetchFn).toHaveBeenCalledTimes(4);
  });

  it("60 s of silence aborts and reconnects; a keep-alive comment resets the timer", async () => {
    const h = harness();
    const got: LiveEvent[] = [];
    h.live.subscribe((e) => got.push(e));
    await tick();
    await tick(LIVE_STALL_MS - 1000);
    h.conns[0]!.push(": keep-alive\n\n");
    await tick(LIVE_STALL_MS - 1000);
    expect(h.conns[0]!.init?.signal?.aborted).toBe(false);
    await tick(1000);
    expect(h.conns[0]!.init?.signal?.aborted).toBe(true);
    await tick(1000);
    expect(h.fetchFn).toHaveBeenCalledTimes(2);
    expect(got.filter((e) => e.event === "open")).toHaveLength(2);
  });

  it("204 goes off and never retries", async () => {
    const h = harness([204]);
    h.live.subscribe(() => {});
    await tick(300_000);
    expect(h.live.state).toBe("off");
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
  });

  it("401 calls onSignedOut once and stops; restart reconnects", async () => {
    const h = harness([401, 401]);
    h.live.subscribe(() => {});
    h.live.subscribe(() => {});
    await tick(300_000);
    expect(h.live.state).toBe("signedOut");
    expect(h.onSignedOut).toHaveBeenCalledTimes(1);
    expect(h.onSignedOut).toHaveBeenLastCalledWith("tok");
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    h.setToken("tok2");
    h.live.restart();
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(2);
    expect(h.onSignedOut).toHaveBeenCalledTimes(2);
    expect(h.onSignedOut).toHaveBeenLastCalledWith("tok2");
    h.live.restart();
    await tick();
    expect(h.live.state).toBe("open");
  });

  it("not signed in stays signedOut without a fetch", async () => {
    const h = harness();
    h.setToken(undefined);
    h.live.subscribe(() => {});
    await tick(300_000);
    expect(h.live.state).toBe("signedOut");
    expect(h.fetchFn).not.toHaveBeenCalled();
    h.setToken("tok");
    h.live.restart();
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
  });
});
