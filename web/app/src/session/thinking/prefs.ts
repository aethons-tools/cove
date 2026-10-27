// The Thinking tab's preferences, under the old Inbox's key (so saved ones
// carry over): localStorage "jam-thinking" holds
// {f, tool, format}; the search is never saved.
export type Format = "narrative" | "detailed" | "raw";
export const FORMATS: Format[] = ["narrative", "detailed", "raw"];
export type Chip = "thinking" | "text" | "tools" | "errors" | "progress";
export interface ThinkingPrefs { f: Record<Chip, boolean>; tool: string; format: Format }

export const defaultPrefs = (): ThinkingPrefs => ({
  f: { thinking: true, text: true, tools: true, errors: false, progress: false }, tool: "", format: "narrative",
});

export function loadPrefs(): ThinkingPrefs {
  const d = defaultPrefs();
  try {
    const v = JSON.parse(localStorage.getItem("jam-thinking") ?? "null") as unknown;
    if (!v || typeof v !== "object") return d;
    const o = v as { f?: Record<string, unknown>; tool?: unknown; format?: unknown };
    for (const k of Object.keys(d.f) as Chip[]) {
      const b = o.f?.[k];
      if (typeof b === "boolean") d.f[k] = b;
    }
    if (typeof o.tool === "string") d.tool = o.tool;
    if (FORMATS.includes(o.format as Format)) d.format = o.format as Format;
  } catch { /* unreadable: the defaults */ }
  return d;
}

export function savePrefs(p: ThinkingPrefs): void {
  try {
    localStorage.setItem("jam-thinking", JSON.stringify({ f: p.f, tool: p.tool, format: p.format }));
  } catch { /* storage full or denied */ }
}
