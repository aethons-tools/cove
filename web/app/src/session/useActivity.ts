import { useInfiniteQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { ActivityRow, ConversationRef, SessionActivity, TurnFold } from "../api/types";

const base = (id: string) => `sessions/${encodeURIComponent(id)}/activity`;

// useActivity pages a session's activity newest-first, as read pages: page
// 0 is the newest (anchor=end), each next page the one before its first
// message. It sits under the ["session", id] key, so the events stream's
// changed refetches it, every loaded page in order.
export const useActivity = (id: string) => useInfiniteQuery<SessionActivity, ApiError>({
  queryKey: ["session", id, "activity"],
  initialPageParam: "",
  queryFn: ({ pageParam }) => apiGet<SessionActivity>(pageParam
    ? `${base(id)}?anchor=id&dir=backward&id=${encodeURIComponent(pageParam as string)}`
    : base(id)),
  getNextPageParam: (last) => (last.earlier && last.pageFirst ? last.pageFirst : undefined),
  refetchInterval: POLL_MS,
});

// Entry is one line of the activity: a message, or a turn's folded thinking.
export type Entry = { t: "msg"; key: string; m: ActivityRow } | { t: "turn"; key: string; f: TurnFold };

const at = (e: Entry) => (e.t === "msg" ? Date.parse(e.m.time) : e.f.start);

// entriesOf is every loaded message and turn, each once, in time order: a
// turn sits after the messages sent before it started (a turn that starts
// as a message arrives comes after it).
export function entriesOf(pages: SessionActivity[]): Entry[] {
  const seen = new Set<string>();
  const out: Entry[] = [];
  const add = (e: Entry) => {
    if (!seen.has(e.key)) { seen.add(e.key); out.push(e); }
  };
  for (const p of [...pages].reverse()) {
    for (const m of p.messages) add({ t: "msg", key: m.id, m });
    for (const f of p.turns) add({ t: "turn", key: `turn:${f.stream}:${f.turn}`, f });
  }
  return out
    .map((e, i) => ({ e, i }))
    .sort((a, b) => at(a.e) - at(b.e) || (a.e.t === b.e.t ? a.i - b.i : a.e.t === "msg" ? -1 : 1))
    .map(({ e }) => e);
}

// limitedOf is the loaded pages' conversations with a tripped agent limit,
// each once.
export function limitedOf(pages: SessionActivity[]): ConversationRef[] {
  const seen = new Map<string, ConversationRef>();
  for (const p of pages) for (const c of p.limited) seen.set(c.id, c);
  return [...seen.values()];
}
