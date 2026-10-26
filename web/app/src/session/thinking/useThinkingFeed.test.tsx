import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { tev } from "./testing";
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
function Probe({ backoff }: { backoff?: number[] }) {
  const f = useThinkingFeed("ses_1", { max: 100, backoff, following: () => true });
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
    expect(es.url).toBe("/app/user/session/ses_1/thinking/events?format=json");
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
    expect(FakeES.last.url).toBe("/app/user/session/ses_1/thinking/events?format=json");
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
});
