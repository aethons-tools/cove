// The session pane's layout, stored as the old Inbox stores it (layout.html's
// mePaneLoad/mePaneSave), so both UIs share it: localStorage
// "jam-pane:<session>" and "jam-pane:last" (the layout a session never
// opened starts from) hold {tab, place, w, h}.

export const PANE_TABS = ["studio", "thinking", "previews", "artifacts", "status"] as const;
export type PaneTab = (typeof PANE_TABS)[number];
export const TAB_LABEL: Record<PaneTab, string> = {
  studio: "Studio", thinking: "Thinking", previews: "Previews", artifacts: "Artifacts", status: "Status",
};
export const isTab = (v: unknown): v is PaneTab => typeof v === "string" && (PANE_TABS as readonly string[]).includes(v);

export type Place = "right" | "below";
export interface PaneLayout { tab: PaneTab | null; place: Place; w?: number; h?: number }

// PaneState is a session's pane as the view uses it (usePaneLayout).
export interface PaneState {
  tab: PaneTab | null; place: Place; narrow: boolean; size: number;
  toggle(t: PaneTab): void; close(): void; flip(): void; resize(px: number, save?: boolean): void; commit(): void;
}

export const MIN_PANE = 200;
export const MIN_MAIN = 240;
export const KEY_STEP = 24;
export const DEFAULT_SIZE: Record<Place, number> = { right: 380, below: 280 };
export const NARROW = "(max-width: 900px)";

const size = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? Math.round(v) : undefined);

// parse keeps what's valid of a stored layout; anything else is the default.
function parse(raw: string | null): PaneLayout | null {
  if (!raw) return null;
  let v: unknown;
  try { v = JSON.parse(raw); } catch { return null; }
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  const l: PaneLayout = { tab: isTab(o.tab) ? o.tab : null, place: o.place === "below" ? "below" : "right" };
  const w = size(o.w), h = size(o.h);
  if (w) l.w = w;
  if (h) l.h = h;
  return l;
}

export function loadLayout(id: string): PaneLayout {
  try {
    return parse(localStorage.getItem("jam-pane:" + id)) ?? parse(localStorage.getItem("jam-pane:last")) ?? { tab: null, place: "right" };
  } catch {
    return { tab: null, place: "right" };
  }
}

export function saveLayout(id: string, l: PaneLayout): void {
  const v = JSON.stringify(l);
  try {
    localStorage.setItem("jam-pane:" + id, v);
    localStorage.setItem("jam-pane:last", v);
  } catch { /* storage full or denied: the layout lasts this page only */ }
}

// clampSize keeps the pane at least MIN_PANE, leaving the messages MIN_MAIN.
export const clampSize = (px: number, total: number) => Math.round(Math.max(MIN_PANE, Math.min(total - MIN_MAIN, px)));
