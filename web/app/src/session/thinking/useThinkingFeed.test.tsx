import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { tev } from "./testing";
import { adminSessionSource, userThinkingSource, type ThinkingSource } from "./source";
import { useThinkingFeed, type ThinkingFeed } from "./useThinkingFeed";

class FakeES {
  static CLOSED = 2;
  static all: FakeES[] = [];
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  readyState = 0;
  closed = false;
  constructor(public url: string) { FakeES.all.push(this); }
  addEventListener(ev: string, fn: (e: MessageEvent) => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.closed = true; this.readyState = 2; }
  emit(ev: string, data = "") { this.listeners[ev]?.forEach((fn) => fn(new MessageEvent(ev, { data }))); }
  fail() { this.readyState = 2; this.emit("error"); }
  static get last() { return FakeES.all[FakeES.all.length - 1]!; }
}

const box: { feed?: ThinkingFeed } = {};
const BACKOFF_FAST = [1, 1];
function Probe({ backoff, src = userThinkingSource("ses_1"), onTotals, onStream }: {
  backoff?: number[]; src?: ThinkingSource; onTotals?: (t: unknown) => void; onStream?: (s: string) => void;
}) {
  const f = useThinkingFeed(src, { max: 100, backoff, following: () => true, onTotals, onStream });
  useEffect(() => { box.feed = f; });
  return null;
}
const feed = { get feed() { return box.feed!.feed; }, earlier: () => box.feed!.earlier(), fill: (k: string) => box.feed!.fill(k) };
const seqs = () => feed.feed.items.map((i) => (i.t === "ev" ? `${i.ev.stream}:${i.ev.seq}` : i.t));

afterEach(() => { vi.unstubAllGlobals(); FakeES.all = []; });

describe("useThinkingFeed", () => {
  it("follows the JSON feed: stream, events, more, skipped", () => {
    vi.stubGlobal("EventSource", FakeES);
    render(<Probe />);
    const es = FakeES.last;
    expect(es.url).toBe("/app/user/session/ses_1/thinking/events");
    act(() => {
      es.emit("stream", "s1");
      es.emit("ev", JSON.stringify(tev(4)));
      es.emit("more", "4");
      es.emit("skipped", "5-7");
      es.emit("ev", JSON.stringify(tev(8)));
    });
    expect(seqs()).toEqual(["s1:4", "skip", "s1:8"]);
    expect(feed.feed.more).toBe(4);
    expect(feed.feed.stream).toBe("s1");
  });

  it("on newstream: a divider, then the new stream without clearing", () => {
    vi.stubGlobal("EventSource", FakeES);
    render(<Probe />);
    act(() => { FakeES.last.emit("stream", "s1"); FakeES.last.emit("ev", JSON.stringify(tev(1))); });
    const first = FakeES.last;
    act(() => first.emit("newstream", "s2"));
    expect(first.closed).toBe(true);
    expect(FakeES.all).toHaveLength(2);
    expect(FakeES.last.url).toBe("/app/user/session/ses_1/thinking/events");
    act(() => { FakeES.last.emit("stream", "s2"); FakeES.last.emit("ev", JSON.stringify(tev(1, { stream: "s2" }))); });
    expect(seqs()).toEqual(["s1:1", "restart", "s2:1"]);
    // A late event from the closed feed is ignored.
    act(() => first.emit("ev", JSON.stringify(tev(2))));
    expect(seqs()).toEqual(["s1:1", "restart", "s2:1"]);
  });

  it("loads earlier pages and fills holes from the API", async () => {
    vi.stubGlobal("EventSource", FakeES);
    const asked: string[] = [];
    server.use(http.get("/app/user/api/sessions/:id/thinking", ({ request }) => {
      const u = new URL(request.url);
      asked.push(`${u.searchParams.get("stream")}<${u.searchParams.get("before")}`);
      const before = Number(u.searchParams.get("before"));
      return HttpResponse.json({ events: [tev(before - 2), tev(before - 1)], before: before - 2 > 1 ? before - 2 : 0 });
    }));
    render(<Probe />);
    act(() => { FakeES.last.emit("stream", "s1"); FakeES.last.emit("ev", JSON.stringify(tev(10))); FakeES.last.emit("more", "10"); });
    await act(() => feed.earlier());
    expect(seqs()).toEqual(["s1:8", "s1:9", "s1:10"]);
    expect(feed.feed.more).toBe(8);
    act(() => { FakeES.last.emit("skipped", "11-14"); FakeES.last.emit("ev", JSON.stringify(tev(15))); });
    const key = feed.feed.items.find((i) => i.t === "skip")!;
    await act(() => feed.fill(key.t === "skip" ? key.key : ""));
    expect(asked).toEqual(["s1<10", "s1<15"]);
    expect(seqs()).toEqual(["s1:8", "s1:9", "s1:10", "skip", "s1:13", "s1:14", "s1:15"]);
  });

  it("a refused feed: 404 says the session is gone, and stops", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ error: "no such session", code: "not_found" }, { status: 404 })));
    render(<Probe backoff={BACKOFF_FAST} />);
    act(() => FakeES.last.fail());
    await waitFor(() => expect(feed.feed.note).toBe("This session is no longer available to you"));
    expect(FakeES.all).toHaveLength(1);
  });

  it("a refused feed otherwise retries on the backoff, then says so, and stops", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ id: "ses_1", tab: "thinking" })));
    render(<Probe backoff={BACKOFF_FAST} />);
    act(() => FakeES.last.fail());
    await waitFor(() => expect(FakeES.all).toHaveLength(2));
    act(() => FakeES.last.fail());
    await waitFor(() => expect(FakeES.all).toHaveLength(3));
    act(() => FakeES.last.fail());
    await waitFor(() => expect(feed.feed.note).toBe("Too many Thinking views open — close one, or reload"));
    await new Promise((r) => setTimeout(r, 20));
    expect(FakeES.all).toHaveLength(3);
  });

  it("an open resets the backoff; a transient error (still connecting) is EventSource's own retry", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ id: "ses_1", tab: "thinking" })));
    render(<Probe backoff={[1]} />);
    act(() => { FakeES.last.readyState = 0; FakeES.last.emit("error"); });
    await new Promise((r) => setTimeout(r, 10));
    expect(FakeES.all).toHaveLength(1);
    act(() => FakeES.last.fail());
    await waitFor(() => expect(FakeES.all).toHaveLength(2));
    act(() => { FakeES.last.emit("open"); FakeES.last.fail(); }); // opened: the backoff starts over
    await waitFor(() => expect(FakeES.all).toHaveLength(3));
  });

  it("closes the feed on unmount", () => {
    vi.stubGlobal("EventSource", FakeES);
    const { unmount } = render(<Probe />);
    unmount();
    expect(FakeES.last.closed).toBe(true);
  });

  it("a pinned source stays on its stream: its feed, earlier pages, and a newstream", async () => {
    vi.stubGlobal("EventSource", FakeES);
    let asked = "";
    server.use(http.get("/app/admin/api/agents/:id/session/page", ({ request }) => {
      asked = new URL(request.url).search;
      return HttpResponse.json({ events: [tev(1, { stream: "s0" })], before: 0 });
    }));
    render(<Probe src={adminSessionSource("ses_1", "s0")} />);
    expect(FakeES.last.url).toBe("/app/admin/api/agents/ses_1/session/events?stream=s0");
    act(() => { FakeES.last.emit("stream", "s0"); FakeES.last.emit("ev", JSON.stringify(tev(2, { stream: "s0" }))); FakeES.last.emit("more", "2"); });
    await act(() => feed.earlier());
    expect(asked).toBe("?stream=s0&before=2");
    expect(seqs()).toEqual(["s0:1", "s0:2"]);
    act(() => FakeES.last.emit("newstream", "s9"));
    expect(FakeES.last.url).toBe("/app/admin/api/agents/ses_1/session/events?stream=s0");
  });

  it("hands the feed's totals and stream to the caller", () => {
    vi.stubGlobal("EventSource", FakeES);
    const totals = vi.fn(), stream = vi.fn();
    render(<Probe onTotals={totals} onStream={stream} />);
    act(() => { FakeES.last.emit("stream", "s1"); FakeES.last.emit("totals", '{"turns":2}'); FakeES.last.emit("totals", "not json"); });
    expect(stream).toHaveBeenCalledWith("s1");
    expect(totals).toHaveBeenCalledTimes(1);
    expect(totals).toHaveBeenCalledWith({ turns: 2 });
  });
});
