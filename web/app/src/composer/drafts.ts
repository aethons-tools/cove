// The composer's per-recipient state in this tab's sessionStorage, under the
// old Inbox's keys (a half-typed reply carries across the two UIs), falling
// back to memory where storage is unavailable. Never sent anywhere.
export type Draft = { text: string; sel: number };

const memory = new Map<string, string>();
function get(key: string): string | null {
  try { return sessionStorage.getItem(key); } catch { return memory.get(key) ?? null; }
}
function set(key: string, value: string | null) {
  try {
    if (value === null) sessionStorage.removeItem(key); else sessionStorage.setItem(key, value);
  } catch {
    if (value === null) memory.delete(key); else memory.set(key, value);
  }
}

export const loadReply = (to: string) => get("me-reply:" + to) ?? "";
export const saveReply = (to: string, text: string) => set("me-reply:" + to, text ? text : null);

export function loadDrafts(to: string): Draft[] {
  try {
    const d: unknown = JSON.parse(get("me-drafts:" + to) ?? "[]");
    return Array.isArray(d) ? (d as Draft[]).filter((x) => typeof x?.text === "string") : [];
  } catch { return []; }
}
export const saveDrafts = (to: string, d: Draft[]) => set("me-drafts:" + to, JSON.stringify(d));
