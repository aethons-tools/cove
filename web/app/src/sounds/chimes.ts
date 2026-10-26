import { useEffect, useRef } from "react";
import type { ChannelRow, RailResponse } from "../api/types";
import { play } from "./sounds";

export type Snap = Map<string, { u: number; n: boolean }>;
export const snapshot = (rows: ChannelRow[]): Snap => new Map(rows.map((r) => [r.id, { u: r.unread, n: r.needsYou }]));

// chimeFor is the chime a rail refresh calls for: "needs" when a conversation
// starts needing you, else "messages" when one has more unread — never for
// the one you're looking at, and nothing for the first rail (the baseline).
export function chimeFor(prev: Snap | null, rows: ChannelRow[], open: string | undefined, looking: boolean): "needs" | "messages" | "" {
  if (!prev) return "";
  let kind: "needs" | "messages" | "" = "";
  for (const r of rows) {
    if (looking && r.id === open) continue;
    const p = prev.get(r.id);
    if (r.needsYou && !p?.n) kind = "needs";
    else if (r.unread > (p?.u ?? 0) && kind !== "needs") kind = "messages";
  }
  return kind;
}

// useChimes plays chimeFor's sound on each new rail. updatedAt is when the
// rail was fetched: a rail no newer than the last one seen (another
// selection's cached rail, shown on switching back) is skipped, so its
// refetch can't chime again for what already chimed.
export function useChimes(rail: RailResponse | undefined, updatedAt: number, open: string | undefined): void {
  const prev = useRef<Snap | null>(null);
  const last = useRef(0);
  useEffect(() => {
    if (!rail || updatedAt <= last.current) return;
    last.current = updatedAt;
    const rows = rail.groups.flatMap((g) => g.rows);
    const looking = document.visibilityState === "visible" && document.hasFocus();
    const kind = chimeFor(prev.current, rows, open, looking);
    prev.current = snapshot(rows);
    if (kind) play(kind);
  }, [rail, updatedAt, open]);
}
