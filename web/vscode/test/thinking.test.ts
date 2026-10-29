import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LIVE_BACKOFF_MIN_MS, LIVE_STALL_MS } from "../src/live.js";
import { THINKING_REPLAY_MAX, ThinkingStreams, type ThinkingSignal } from "../src/thinking.js";

const SES = "ses_0123456789abcdefghjkmnpqrs";
const SES2 = "ses_0123456789abcdefghjkmnpqrt";
const STR = "a".repeat(32);

type Conn = { push: (s: string) => void; end: () => void; init: RequestInit | undefined; url: string };

function harness(statuses: number[] = []) {
  const conns: Conn[] = [];
  const fetchFn = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
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
    conns.push({ push: (s) => ctl.enqueue(enc.encode(s)), end: () => ctl.close(), init, url: String(url) });
    return new Response(body, { status: 200 });
  });
  let token: string | undefined = "tok";
  const onSignedOut = vi.fn();
  const ts = new ThinkingStreams({
    session: async () => (token ? { url: "https://jam.example.com", token } : undefined),
    fetch: fetchFn as unknown as typeof fetch,
    onSignedOut,
    setTimeout: ((f: () => void, ms: number) => setTimeout(f, ms)) as unknown as typeof setTimeout,
    clearTimeout: ((t: ReturnType<typeof setTimeout>) => clearTimeout(t)) as typeof clearTimeout,
    random: () => 0.5,
  });
  return { ts, fetchFn, conns, onSignedOut, setToken: (t: string | undefined) => (token = t) };
}

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);
const sse = (event: string, data: string, id?: string) => `${id ? `id: ${id}\n` : ""}event: ${event}\ndata: ${data}\n\n`;
const ev = (seq: number) => sse("ev", JSON.stringify({ seq }), `s1:${seq}`);
const names = (g: ThinkingSignal[]) => g.map((s) => ("error" in s ? (s.closed ? "error!" : "error") : s.event));

describe("ThinkingStreams", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("two subscribers of one key share one fetch with bearer and Accept", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    const b: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => a.push(s));
    h.ts.subscribe(SES, "", (s) => b.push(s));
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    expect(h.conns[0]!.url).toBe(`https://jam.example.com/app/user/session/${SES}/thinking/events`);
    const hd = h.conns[0]!.init!.headers as Record<string, string>;
    expect(hd.Authorization).toBe("Bearer tok");
    expect(hd.Accept).toBe("text/event-stream");
    h.conns[0]!.push(sse("stream", "x"));
    await tick();
    expect(names(a)).toEqual(["open", "stream"]);
    expect(names(b)).toEqual(["open", "stream"]);
    expect(h.ts.connections).toBe(1);
  });

  it("different streams of one session are different keys", async () => {
    const h = harness();
    h.ts.subscribe(SES, "", () => undefined);
    h.ts.subscribe(SES, STR, () => undefined);
    h.ts.subscribe(SES2, "", () => undefined);
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(3);
    expect(h.conns[1]!.url).toBe(`https://jam.example.com/app/user/session/${SES}/thinking/events?stream=${STR}`);
    expect(h.ts.connections).toBe(3);
  });

  it("a late subscriber gets open, stream, the evs and the latest totals", async () => {
    const h = harness();
    h.ts.subscribe(SES, "", () => undefined);
    await tick();
    h.conns[0]!.push(sse("stream", "s1") + sse("totals", "1") + ev(1) + sse("skipped", "3") + ev(2) + sse("totals", "2"));
    await tick();
    const late: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => late.push(s));
    await tick();
    expect(late).toEqual([
      { event: "open", data: "" },
      { event: "stream", data: "s1" },
      { event: "ev", data: '{"seq":1}' },
      { event: "skipped", data: "3" },
      { event: "ev", data: '{"seq":2}' },
      { event: "totals", data: "2" },
    ]);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
  });

  it("a late subscriber gets no live event before its replay", async () => {
    const h = harness();
    const late: ThinkingSignal[] = [];
    let joined = false;
    h.ts.subscribe(SES, "", (s) => {
      if (joined || !("event" in s) || s.event !== "ev") return;
      joined = true;
      h.ts.subscribe(SES, "", (t) => late.push(t));
    });
    await tick();
    h.conns[0]!.push(sse("stream", "s1") + ev(1) + ev(2));
    await tick();
    expect(late).toEqual([
      { event: "open", data: "" },
      { event: "stream", data: "s1" },
      { event: "ev", data: '{"seq":1}' },
      { event: "ev", data: '{"seq":2}' },
    ]);
    h.conns[0]!.push(ev(3));
    await tick();
    expect(late.at(-1)).toEqual({ event: "ev", data: '{"seq":3}' });
    expect(late).toHaveLength(5);
  });

  it("replay past 2000 evs keeps the newest 2000 and a more at the oldest kept seq", async () => {
    const h = harness();
    h.ts.subscribe(SES, "", () => undefined);
    await tick();
    let chunk = "";
    for (let i = 1; i <= THINKING_REPLAY_MAX + 5; i++) chunk += ev(i);
    h.conns[0]!.push(chunk);
    await tick();
    const late: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => late.push(s));
    await tick();
    const evs = late.filter((s) => "event" in s && s.event === "ev");
    expect(evs).toHaveLength(THINKING_REPLAY_MAX);
    expect((evs[0] as { data: string }).data).toBe('{"seq":6}');
    const more = late.filter((s) => "event" in s && s.event === "more");
    expect(more).toEqual([{ event: "more", data: "6" }]);
  });

  it("a stall reconnects with Last-Event-ID and the subscribers see a transient error", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => a.push(s));
    await tick();
    h.conns[0]!.push(ev(1) + ev(2));
    await tick();
    await tick(LIVE_STALL_MS);
    expect(a.at(-1)).toEqual({ error: true, closed: false });
    await tick(LIVE_BACKOFF_MIN_MS);
    expect(h.fetchFn).toHaveBeenCalledTimes(2);
    expect((h.conns[1]!.init!.headers as Record<string, string>)["Last-Event-ID"]).toBe("s1:2");
    await tick();
    expect(names(a)).toEqual(["open", "ev", "ev", "error", "open"]);
    expect(h.ts.connections).toBe(1);
  });

  it("a 429 is a closed error with no retry", async () => {
    const h = harness([429]);
    const a: ThinkingSignal[] = [];
    const onEnd = vi.fn();
    h.ts.subscribe(SES, "", (s) => a.push(s), onEnd);
    await tick(LIVE_STALL_MS * 3);
    expect(a).toEqual([{ error: true, closed: true }]);
    expect(onEnd).toHaveBeenCalledTimes(1);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    expect(h.ts.connections).toBe(0);
  });

  it("401 calls onSignedOut with the token once and closes", async () => {
    const h = harness([401, 401]);
    const a: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => a.push(s));
    await tick();
    expect(a).toEqual([{ error: true, closed: true }]);
    expect(h.onSignedOut).toHaveBeenCalledWith("tok");
    h.ts.subscribe(SES, "", () => undefined);
    await tick();
    expect(h.onSignedOut).toHaveBeenCalledTimes(1);
    expect(h.ts.connections).toBe(0);
  });

  it("not signed in is a closed error without a fetch", async () => {
    const h = harness();
    h.setToken(undefined);
    const a: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => a.push(s));
    await tick();
    expect(a).toEqual([{ error: true, closed: true }]);
    expect(h.fetchFn).not.toHaveBeenCalled();
  });

  it("a late subscriber during a retry gets no open until the reconnect succeeds", async () => {
    const h = harness();
    h.ts.subscribe(SES, "", () => undefined);
    await tick();
    h.conns[0]!.push(ev(1));
    await tick();
    h.conns[0]!.end();
    await tick();
    const late: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => late.push(s));
    await tick();
    expect(names(late)).toEqual(["ev"]);
    await tick(LIVE_BACKOFF_MIN_MS);
    expect(names(late)).toEqual(["ev", "open"]);
  });

  it("newstream is forwarded, then the key ends without reconnecting", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    const onEnd = vi.fn();
    h.ts.subscribe(SES, "", (s) => a.push(s), onEnd);
    await tick();
    h.conns[0]!.push(sse("newstream", "n") + ev(9));
    await tick();
    expect(names(a)).toEqual(["open", "newstream"]);
    expect(onEnd).toHaveBeenCalledTimes(1);
    expect(h.ts.connections).toBe(0);
    await tick(LIVE_STALL_MS * 2);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
    h.ts.subscribe(SES, "", () => undefined);
    await tick();
    expect(h.fetchFn).toHaveBeenCalledTimes(2);
  });

  it("last unsubscribe aborts", async () => {
    const h = harness();
    const u1 = h.ts.subscribe(SES, "", () => undefined);
    const u2 = h.ts.subscribe(SES, "", () => undefined);
    await tick();
    u1();
    expect(h.conns[0]!.init!.signal!.aborted).toBe(false);
    u2();
    expect(h.conns[0]!.init!.signal!.aborted).toBe(true);
    expect(h.ts.connections).toBe(0);
    await tick(LIVE_STALL_MS * 2);
    expect(h.fetchFn).toHaveBeenCalledTimes(1);
  });

  it("restart closes every key", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    const onEnd = vi.fn();
    h.ts.subscribe(SES, "", (s) => a.push(s), onEnd);
    h.ts.subscribe(SES2, "", () => undefined, onEnd);
    await tick();
    h.ts.restart();
    expect(a.at(-1)).toEqual({ error: true, closed: true });
    expect(onEnd).toHaveBeenCalledTimes(2);
    expect(h.conns.every((c) => c.init!.signal!.aborted)).toBe(true);
    expect(h.ts.connections).toBe(0);
  });

  it("a bad session or stream throws", () => {
    const h = harness();
    for (const [s, st] of [["../x", ""], ["chn_0123456789abcdefghjkmnpqrs", ""], [SES, "nothex"], [SES, "A".repeat(32)], [SES, "a".repeat(31)]] as const) {
      expect(() => h.ts.subscribe(s, st, () => undefined)).toThrow();
    }
    expect(h.fetchFn).not.toHaveBeenCalled();
  });

  it("only the allowed events are forwarded", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    h.ts.subscribe(SES, "", (s) => a.push(s));
    await tick();
    h.conns[0]!.push(sse("message", "m") + sse("notify", "n") + sse("open", "o") + sse("more", "4"));
    await tick();
    expect(names(a)).toEqual(["open", "more"]);
  });

  it("a throwing subscriber starves no one and leaks no connection", async () => {
    const h = harness();
    const a: ThinkingSignal[] = [];
    const u1 = h.ts.subscribe(SES, "", () => {
      throw new Error("boom");
    });
    h.ts.subscribe(SES, "", (s) => a.push(s));
    await tick();
    h.conns[0]!.push(ev(1));
    await tick();
    expect(names(a)).toEqual(["open", "ev"]);
    u1();
    expect(h.ts.connections).toBe(1);
  });
});
