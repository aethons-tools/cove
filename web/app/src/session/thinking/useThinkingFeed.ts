import { useEffect, useReducer, useRef, useState, type Dispatch } from "react";
import type { ThinkingEvent } from "../../api/types";
import { emptyFeed, feedReducer, type Feed, type FeedAction } from "./feed";
import type { ThinkingSource } from "./source";

export interface ThinkingFeed {
  feed: Feed;
  pinned: string; // the stream chosen ("" = follow the newest)
  connect(stream: string): void;
  earlier(): Promise<void>;
  fill(key: string): Promise<void>;
  seen(): void;
}

// A FeedSource is the part of an EventSource the feed uses, so a shell can
// supply its own (the VS Code pane's, over its bridge).
export type FeedSource = {
  readonly readyState: number; // 0 connecting, 1 open, 2 closed (EventSource's)
  addEventListener(type: string, fn: (e: { data: string }) => void): void;
  close(): void;
};
export type FeedOpener = (url: string) => FeedSource;
export const eventSourceFeed: FeedOpener = (u) => new EventSource(u);
export const FEED_CLOSED = 2;
let opener: FeedOpener = eventSourceFeed;
// setFeedOpener swaps how feeds open; no argument restores eventSourceFeed.
export function setFeedOpener(o?: FeedOpener): void { opener = o ?? eventSourceFeed; }

export const BACKOFF = [2000, 5000, 15000];
export const MAX_ROWS = 2000;

// Conn is one tab's feed connection; gen counts its connections, so a
// closed feed's late messages and a stale retry are ignored.
interface Conn {
  src: ThinkingSource;
  es: FeedSource | null;
  tries: number;
  retry: ReturnType<typeof setTimeout> | undefined;
  gen: number;
  dispatch: Dispatch<FeedAction>;
  setPinned: (s: string) => void;
  following: () => boolean;
  backoff: number[];
  on: FeedCallbacks;
}

// FeedCallbacks hand the caller what the feed carries beside its rows: the
// stream it follows, and the stream's running totals (parsed JSON).
export interface FeedCallbacks {
  onTotals?: (t: unknown) => void;
  onStream?: (stream: string) => void;
}

// open (re)connects c to stream ("" = the newest), clearing the list first
// when asked.
function open(c: Conn, stream: string, clear: boolean) {
  c.es?.close();
  clearTimeout(c.retry);
  const gen = ++c.gen;
  c.setPinned(stream);
  if (clear) c.dispatch({ type: "reset" });
  const es = opener(c.src.events(stream));
  c.es = es;
  const on = (name: string, fn: (e: { data: string }) => void) =>
    es.addEventListener(name, (e) => { if (c.gen === gen) fn(e); });
  on("stream", (e) => { c.dispatch({ type: "stream", stream: e.data }); c.on.onStream?.(e.data); });
  on("totals", (e) => {
    let t: unknown;
    try { t = JSON.parse(e.data); } catch { return; }
    c.on.onTotals?.(t);
  });
  on("ev", (e) => {
    let ev: ThinkingEvent;
    try { ev = JSON.parse(e.data) as ThinkingEvent; } catch { return; }
    c.dispatch({ type: "live", ev, following: c.following() });
  });
  on("more", (e) => c.dispatch({ type: "more", before: Number(e.data) }));
  on("skipped", (e) => {
    const [from, to] = e.data.split("-").map(Number);
    if (from && to) c.dispatch({ type: "skipped", from, to });
  });
  on("newstream", () => {
    c.dispatch({ type: "restart" });
    open(c, c.src.pinned ?? "", false);
  });
  on("open", () => { c.tries = 0; });
  on("error", () => {
    if (es.readyState !== FEED_CLOSED) return; // EventSource retries by itself
    es.close();
    c.es = null;
    void c.src.check()
      .then((st) => {
        if (c.gen !== gen) return;
        if (st === 404) { c.dispatch({ type: "note", text: "This session is no longer available to you" }); return; }
        if (c.tries >= c.backoff.length) { c.dispatch({ type: "note", text: "Too many Thinking views open — close one, or reload" }); return; }
        const wait = c.backoff[c.tries++]!;
        c.retry = setTimeout(() => { if (c.gen === gen) open(c, stream, true); }, wait);
      });
  });
}

// useThinkingFeed is a session's Thinking feed: the JSON form of
// the source's events (signals and rows; each row is data, rendered as
// text) and older pages. A feed the server refuses (EventSource can't say
// why) is checked against the source: 404 means the
// viewer lost the session; anything else (429 too many feeds, a 502)
// retries after each backoff delay, then says so — never looping.
export function useThinkingFeed(src: ThinkingSource, opts: { max?: number; backoff?: number[]; following: () => boolean } & FeedCallbacks): ThinkingFeed {
  const [feed, dispatch] = useReducer(feedReducer(opts.max ?? MAX_ROWS), emptyFeed);
  const [pinned, setPinned] = useState("");
  const conn = useRef<Conn>({ src, es: null, tries: 0, retry: undefined, gen: 0, dispatch, setPinned,
    following: opts.following, backoff: opts.backoff ?? BACKOFF, on: opts });
  const feedNow = useRef(feed);
  useEffect(() => {
    conn.current.src = src;
    conn.current.following = opts.following;
    conn.current.backoff = opts.backoff ?? BACKOFF;
    conn.current.on = { onTotals: opts.onTotals, onStream: opts.onStream };
    feedNow.current = feed;
  });

  useEffect(() => {
    const c = conn.current;
    c.src = src;
    open(c, src.pinned ?? "", false);
    return () => { c.gen++; c.es?.close(); clearTimeout(c.retry); };
    // src.key names the session: a new source object for it is the same feed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [src.key]);

  const page = async (before: number, at: string | null, from: number, stream: string) => {
    if (!stream || !before) return;
    const p = await conn.current.src.page(stream, before);
    dispatch({ type: "page", events: p.events, at, from, before: p.before });
  };

  return {
    feed,
    pinned,
    connect: (stream) => { conn.current.tries = 0; open(conn.current, stream, true); },
    earlier: () => {
      const f = feedNow.current, first = f.items.find((i) => i.t === "ev");
      return page(f.more, null, 0, first?.t === "ev" ? first.ev.stream : f.stream);
    },
    fill: (key) => {
      const m = feedNow.current.items.find((i) => i.t === "skip" && i.key === key);
      return m?.t === "skip" ? page(m.to + 1, key, m.from, m.stream) : Promise.resolve();
    },
    seen: () => dispatch({ type: "seen" }),
  };
}
