import { useEffect, useReducer, useRef, useState, type Dispatch } from "react";
import { apiGet, ApiError } from "../../api/client";
import type { ThinkingEvent, ThinkingPage } from "../../api/types";
import { emptyFeed, feedReducer, type Feed, type FeedAction } from "./feed";

export interface ThinkingFeed {
  feed: Feed;
  pinned: string; // the stream chosen ("" = follow the newest)
  connect(stream: string): void;
  earlier(): Promise<void>;
  fill(key: string): Promise<void>;
  seen(): void;
}

const enc = encodeURIComponent;
export const BACKOFF = [2000, 5000, 15000];
export const MAX_ROWS = 2000;

// Conn is one tab's feed connection; gen counts its connections, so a
// closed feed's late messages and a stale retry are ignored.
interface Conn {
  id: string;
  es: EventSource | null;
  tries: number;
  retry: ReturnType<typeof setTimeout> | undefined;
  gen: number;
  dispatch: Dispatch<FeedAction>;
  setPinned: (s: string) => void;
  following: () => boolean;
  backoff: number[];
}

// open (re)connects c to stream ("" = the newest), clearing the list first
// when asked.
function open(c: Conn, stream: string, clear: boolean) {
  c.es?.close();
  clearTimeout(c.retry);
  const gen = ++c.gen;
  c.setPinned(stream);
  if (clear) c.dispatch({ type: "reset" });
  const es = new EventSource(`/app/user/session/${enc(c.id)}/thinking/events${stream ? `?stream=${enc(stream)}` : ""}`);
  c.es = es;
  const on = (name: string, fn: (e: MessageEvent<string>) => void) =>
    es.addEventListener(name, (e) => { if (c.gen === gen) fn(e as MessageEvent<string>); });
  on("stream", (e) => c.dispatch({ type: "stream", stream: e.data }));
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
    open(c, "", false);
  });
  on("open", () => { c.tries = 0; });
  on("error", () => {
    if (es.readyState !== EventSource.CLOSED) return; // EventSource retries by itself
    es.close();
    c.es = null;
    void apiGet(`sessions/${enc(c.id)}/pane/thinking`)
      .then(() => 0, (err: unknown) => (err instanceof ApiError ? err.status : 0))
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
// …/thinking/events (signals and rows; each row is data, rendered as text),
// older pages from api/sessions/{id}/thinking. A feed the server refuses
// (EventSource can't say why) is checked against the pane: 404 means the
// viewer lost the session; anything else (429 too many feeds, a 502)
// retries after each backoff delay, then says so — never looping.
export function useThinkingFeed(id: string, opts: { max?: number; backoff?: number[]; following: () => boolean }): ThinkingFeed {
  const [feed, dispatch] = useReducer(feedReducer(opts.max ?? MAX_ROWS), emptyFeed);
  const [pinned, setPinned] = useState("");
  const conn = useRef<Conn>({ id, es: null, tries: 0, retry: undefined, gen: 0, dispatch, setPinned,
    following: opts.following, backoff: opts.backoff ?? BACKOFF });
  const feedNow = useRef(feed);
  useEffect(() => {
    conn.current.following = opts.following;
    conn.current.backoff = opts.backoff ?? BACKOFF;
    feedNow.current = feed;
  });

  useEffect(() => {
    const c = conn.current;
    c.id = id;
    open(c, "", false);
    return () => { c.gen++; c.es?.close(); clearTimeout(c.retry); };
  }, [id]);

  const page = async (before: number, at: string | null, from: number, stream: string) => {
    if (!stream || !before) return;
    const p = await apiGet<ThinkingPage>(`sessions/${enc(id)}/thinking?stream=${enc(stream)}&before=${before}`);
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
