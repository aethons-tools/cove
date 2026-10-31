import { useEffect, useMemo, useState } from "react";
import type { CardState, SessionCard } from "../../api/types";

// A session's recent states for its card's sparkline, recorded in the page
// from the cards it fetches (rail and header). Not a source of truth: a fresh
// page starts flat at the current state.

export interface Point {
  at: number; // ms since the epoch
  state: CardState;
  mark: boolean; // a system/init arrived here (the card's inits went up)
}

export const WINDOW_MS = 10 * 60_000;
const MAX_POINTS = 120;

interface History {
  points: Point[];
  inits: number;
}

const histories = new Map<string, History>();

// record adds card to its session's history (a point when its state changed
// or an init arrived) and returns the points within the window, the first of
// them the state the window opens in.
export function record(card: SessionCard, now = Date.now()): Point[] {
  let h = histories.get(card.session);
  if (!h) {
    h = { points: [{ at: now, state: card.state, mark: false }], inits: card.inits };
    histories.set(card.session, h);
    return [...h.points];
  }
  const mark = card.inits > h.inits;
  h.inits = card.inits; // lower: Jam restarted and counts afresh
  if (mark || h.points[h.points.length - 1]!.state !== card.state) h.points.push({ at: now, state: card.state, mark });
  // Keep the last point before the window: it is the state the window opens in.
  const from = now - WINDOW_MS;
  let first = 0;
  while (first + 1 < h.points.length && h.points[first + 1]!.at <= from) first++;
  h.points = h.points.slice(Math.max(first, h.points.length - MAX_POINTS));
  return [...h.points];
}

// forget drops every history (tests).
export function forget(): void { histories.clear(); }

// useStateHistory records card each time a new one arrives.
export function useStateHistory(card: SessionCard): Point[] {
  return useMemo(() => record(card), [card]);
}

// useNow is the time, ticking every tick ms, so a sparkline scrolls while its
// session's state holds.
export function useNow(tick = 5000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), tick);
    return () => clearInterval(t);
  }, [tick]);
  return now;
}
