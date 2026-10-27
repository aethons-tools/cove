import type { ThinkingEvent } from "../../api/types";

// The Thinking list's items, in display order: events, the dividers a
// restart leaves, and the markers a reconnect's hole leaves. The reducer
// owns the 2000-row cap (max): live events past it drop the oldest (Load
// earlier offers them again); a loaded page past it drops the newest and
// pauses following, so the pages asked for stay (the pill reloads the tail).
export type Item =
  | { t: "ev"; ev: ThinkingEvent }
  | { t: "restart"; key: string }
  | { t: "skip"; key: string; stream: string; from: number; to: number };

export interface Feed {
  items: Item[];
  more: number; // > 0: Load earlier fetches below this seq
  stream: string; // the stream being followed
  paused: boolean; // the newest end was trimmed: live events only count
  unseen: number; // the "↓ N new" pill
  note: string; // why the feed stopped ("" = it hasn't)
}

export type FeedAction =
  | { type: "reset" }
  | { type: "stream"; stream: string }
  | { type: "live"; ev: ThinkingEvent; following: boolean }
  | { type: "more"; before: number }
  | { type: "skipped"; from: number; to: number }
  | { type: "restart" }
  // A page of older events: inserted above the first event (at null: Load
  // earlier) or below the skip marker keyed at (filling its hole from its
  // newest end), keeping seq >= from; before is the page's own boundary
  // (0 = nothing older).
  | { type: "page"; events: ThinkingEvent[]; at: string | null; from: number; before: number }
  | { type: "seen" }
  | { type: "note"; text: string };

export const emptyFeed: Feed = { items: [], more: 0, stream: "", paused: false, unseen: 0, note: "" };

export const evKey = (ev: ThinkingEvent) => `${ev.stream}:${ev.seq}`;
export const itemKey = (i: Item) => (i.t === "ev" ? evKey(i.ev) : i.key);

let markers = 0;
const marker = (kind: string) => `${kind}:${++markers}`;

const evCount = (items: Item[]) => items.reduce((n, i) => n + (i.t === "ev" ? 1 : 0), 0);

export function feedReducer(max: number) {
  return (s: Feed, a: FeedAction): Feed => {
    switch (a.type) {
      case "reset":
        return emptyFeed;
      case "stream":
        return { ...s, stream: a.stream };
      case "note":
        return { ...s, note: a.text };
      case "seen":
        return s.paused ? s : { ...s, unseen: 0 };
      case "more":
        return { ...s, more: a.before };
      case "restart":
        return { ...s, items: [...s.items, { t: "restart", key: marker("restart") }] };
      case "skipped":
        return { ...s, items: [...s.items, { t: "skip", key: marker("skip"), stream: s.stream, from: a.from, to: a.to }] };
      case "live": {
        if (s.paused) return { ...s, unseen: s.unseen + 1 };
        const k = evKey(a.ev);
        if (s.items.some((i) => itemKey(i) === k)) return s;
        let items: Item[] = [...s.items, { t: "ev", ev: a.ev }];
        let more = s.more;
        let over = evCount(items) - max;
        if (over > 0) {
          items = items.filter((i) => i.t !== "ev" || over-- <= 0);
          const first = items.find((i) => i.t === "ev");
          if (first?.t === "ev") more = first.ev.seq;
        }
        return { ...s, items, more, unseen: a.following ? s.unseen : s.unseen + 1 };
      }
      case "page": {
        const have = new Set(s.items.map(itemKey));
        const add: Item[] = a.events.filter((e) => e.seq >= a.from && !have.has(evKey(e))).map((ev) => ({ t: "ev", ev }));
        const items = [...s.items];
        let more = s.more;
        if (a.at === null) {
          const i = items.findIndex((x) => x.t === "ev");
          items.splice(i < 0 ? items.length : i, 0, ...add);
          more = a.before;
        } else {
          const i = items.findIndex((x) => x.t === "skip" && x.key === a.at);
          if (i < 0) return s; // the marker went (a reset): drop the page
          const m = items[i] as Extract<Item, { t: "skip" }>;
          items.splice(i + 1, 0, ...add);
          if (a.before > m.from) items[i] = { ...m, to: a.before - 1 };
          else items.splice(i, 1);
        }
        let paused = s.paused, unseen = s.unseen;
        const over = evCount(items) - max;
        if (over > 0) {
          let n = over;
          for (let j = items.length - 1; j >= 0 && n > 0; j--) {
            if (items[j]!.t === "ev") { items.splice(j, 1); n--; }
          }
          paused = true;
          unseen += over;
        }
        return { ...s, items, more, paused, unseen };
      }
    }
  };
}
