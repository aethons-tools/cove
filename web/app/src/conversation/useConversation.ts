import { useInfiniteQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { Conversation, MessageRow } from "../api/types";

// useConversation pages a conversation newest-first: page 0 is the newest
// (before=0), and each next page is older. A refetch (a `changed` event or
// the poll) refetches every loaded page in order, so loaded history stays.
export const useConversation = (id: string) => useInfiniteQuery<Conversation, ApiError>({
  queryKey: ["conversation", id],
  initialPageParam: 0,
  queryFn: ({ pageParam }) => apiGet<Conversation>(`conversations/${encodeURIComponent(id)}${pageParam ? `?before=${pageParam as number}` : ""}`),
  getNextPageParam: (last) => (last.earlier && last.messages.length > 0 ? last.messages[0]!.seq : undefined),
  refetchInterval: POLL_MS,
});

// messagesOf is every loaded message, oldest first, each seq once.
export function messagesOf(pages: Conversation[]): MessageRow[] {
  const seen = new Set<number>();
  const out: MessageRow[] = [];
  for (const p of [...pages].reverse()) {
    for (const m of p.messages) {
      if (!seen.has(m.seq)) { seen.add(m.seq); out.push(m); }
    }
  }
  return out;
}
