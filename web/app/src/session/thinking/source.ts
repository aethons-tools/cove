import { apiAt, apiGet, ApiError } from "../../api/client";
import type { ThinkingPage, ThinkingRaw } from "../../api/types";

// ThinkingSource is where a Thinking view reads a session's events: the
// live feed's URL, older pages, one event's raw JSON, and a check (an HTTP
// status; 404 = the session is gone) for a feed the server refused.
// key names the session (a new key reconnects); pinned follows that one
// stream only.
export interface ThinkingSource {
  key: string;
  events(stream: string): string;
  page(stream: string, before: number): Promise<ThinkingPage>;
  raw(stream: string, seq: number): Promise<ThinkingRaw>;
  check(): Promise<number>;
  pinned?: string;
}

const enc = encodeURIComponent;
const status = (p: Promise<unknown>) => p.then(() => 200, (e: unknown) => (e instanceof ApiError ? e.status : 0));

// userThinkingSource is a user-area session's Thinking (the viewer's own).
export function userThinkingSource(id: string): ThinkingSource {
  return {
    key: `user:${id}`,
    events: (s) => `/app/user/session/${enc(id)}/thinking/events${s ? `?stream=${enc(s)}` : ""}`,
    page: (s, before) => apiGet<ThinkingPage>(`sessions/${enc(id)}/thinking?stream=${enc(s)}&before=${before}`),
    raw: (s, seq) => apiGet<ThinkingRaw>(`sessions/${enc(id)}/thinking/raw?stream=${enc(s)}&seq=${seq}`),
    check: () => status(apiGet(`sessions/${enc(id)}/pane/thinking`)),
  };
}

// adminSessionSource is an agent's session timeline as an operator reads it,
// pinned to one stream.
export function adminSessionSource(id: string, stream: string): ThinkingSource {
  const base = `/app/admin/api/agents/${enc(id)}/session`;
  const api = apiAt(base);
  return {
    key: `admin:${id}:${stream}`,
    events: (s) => `${base}/events${s ? `?stream=${enc(s)}` : ""}`,
    page: (s, before) => api.get<ThinkingPage>(`/page?stream=${enc(s)}&before=${before}`),
    raw: (s, seq) => api.get<ThinkingRaw>(`/raw?stream=${enc(s)}&seq=${seq}`),
    check: () => status(api.get("")),
    pinned: stream,
  };
}
