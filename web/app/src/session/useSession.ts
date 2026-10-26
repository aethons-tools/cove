import { useQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { SessionHead, SessionPane } from "../api/types";
import type { PaneTab } from "./layout";

const base = (id: string) => `sessions/${encodeURIComponent(id)}`;

// useSessionHead is a session's header, live (the events stream's changed
// and presence) with the 30 s poll behind it.
export const useSessionHead = (id: string) => useQuery<SessionHead, ApiError>({
  queryKey: ["session", id], queryFn: () => apiGet<SessionHead>(base(id)), refetchInterval: POLL_MS,
});

// usePaneTab is one tab of the session's pane (Previews, Artifacts, Status).
export const usePaneTab = (id: string, tab: PaneTab) => useQuery<SessionPane, ApiError>({
  queryKey: ["session", id, "pane", tab], queryFn: () => apiGet<SessionPane>(`${base(id)}/pane/${tab}`), refetchInterval: POLL_MS,
});
