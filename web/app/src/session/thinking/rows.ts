import type { ThinkingEvent } from "../../api/types";
import { itemKey, type Item } from "./feed";
import { fmtDuration, usd } from "./format";
import type { ThinkingPrefs } from "./prefs";

export const ICONS: Record<string, string> = {
  thinking: "💭", text: "💬", prompt: "→", tool_call: "▶", tool_result: "◀", result: "─", gap: "⚠", progress: "·", other: "·",
};

// Row is one line of the list as shown.
export interface Row {
  key: string;
  kind: string;
  ev?: ThinkingEvent;
  icon: string;
  text: string;
  status: string; // a tool call's "…" / "✓ 3.1s" / "✗ 500ms"
  err: boolean;
  output: string; // a tool call's folded result
  outputTruncated: number;
  empty: boolean; // a thinking block with only its signature
  marker?: "restart" | "skip";
  skip?: { from: number; to: number };
}

const TOOLS = new Set(["tool_call", "tool_result"]);

// deriveRows is items as shown (layout.html's meThPair, meThCosts, meThRuns
// and the filter classes, recomputed from scratch): a tool result folds into
// its call (wherever either sits); results become episode dividers; a run of
// empty thinking collapses into its first; then the chips, Errors only, the
// tool and the search hide rows (gaps and markers always show).
export function deriveRows(items: Item[], p: ThinkingPrefs, q: string): Row[] {
  const rows: Row[] = items.map((i): Row => {
    if (i.t !== "ev") {
      return { key: i.key, kind: i.t, icon: "", text: "", status: "", err: false, output: "", outputTruncated: 0, empty: false,
        marker: i.t, skip: i.t === "skip" ? { from: i.from, to: i.to } : undefined };
    }
    const e = i.ev;
    const empty = e.kind === "thinking" && !e.text.trim();
    return { key: itemKey(i), kind: e.kind, ev: e, icon: ICONS[e.kind] ?? "·", text: empty ? "thinking" : e.text,
      status: e.kind === "tool_call" ? "…" : "", err: e.isError, output: "", outputTruncated: 0, empty };
  });
  // Pair: each call takes the first result with its tool-use id.
  const calls = new Map<string, Row>();
  for (const r of rows) if (r.kind === "tool_call" && r.ev?.toolUseId && !calls.has(r.ev.toolUseId)) calls.set(r.ev.toolUseId, r);
  const folded = new Set<string>();
  const paired = new Set<Row>();
  for (const r of rows) {
    if (r.kind !== "tool_result" || !r.ev?.toolUseId) continue;
    const c = calls.get(r.ev.toolUseId);
    if (!c || paired.has(c)) continue;
    paired.add(c);
    folded.add(r.key);
    c.status = `${r.ev.isError ? "✗" : "✓"} ${fmtDuration(r.ev.at - c.ev!.at)}`;
    c.err = c.err || r.ev.isError;
    c.output = r.ev.detail;
    c.outputTruncated = r.ev.detailTruncated;
  }
  const shown = rows.filter((r) => !folded.has(r.key));
  // Costs: each result against the previous result of its claude session.
  const prev = new Map<string, number>();
  for (const r of shown) {
    if (r.kind !== "result" || !r.ev) continue;
    const e = r.ev, k = e.claudeSession || `${e.stream}:${e.turn}`, before = prev.get(k);
    prev.set(k, e.costUsd);
    const cost = before === undefined || e.costUsd < before ? `${usd(e.costUsd)} session total` : usd(e.costUsd - before);
    r.text = `episode ${e.turn} · ${cost} · ${fmtDuration(e.durationMs)}`;
  }
  // Runs of empty thinking (same stream); hidden progress doesn't break one.
  const absorbed = new Set<Row>();
  let head: Row | null = null, n = 0;
  for (const r of shown) {
    if (!p.f.progress && r.kind === "progress") continue;
    if (!r.empty) { head = null; continue; }
    if (head && head.ev!.stream === r.ev!.stream) {
      n++;
      absorbed.add(r);
      head.text = `thinking ×${n}`;
      continue;
    }
    head = r;
    n = 1;
  }
  const needle = q.trim().toLowerCase();
  return shown.filter((r) => {
    if (r.marker) return true;
    if (absorbed.has(r)) return false;
    const k = r.kind;
    if (k === "thinking" && !p.f.thinking) return false;
    if ((k === "text" || k === "prompt") && !p.f.text) return false;
    if (TOOLS.has(k) && !p.f.tools) return false;
    if (k === "progress" && !p.f.progress) return false;
    if (p.f.errors && TOOLS.has(k) && !r.err) return false;
    if (k === "gap") return true;
    if (p.tool && r.ev!.tool !== p.tool) return false;
    if (needle && !`${r.ev!.tool} ${r.text} ${r.ev!.detail} ${r.output}`.toLowerCase().includes(needle)) return false;
    return true;
  });
}

// toolsIn is the tool names in items, in first-seen order (the tool filter).
export function toolsIn(items: Item[]): string[] {
  const out: string[] = [];
  for (const i of items) if (i.t === "ev" && i.ev.tool && !out.includes(i.ev.tool)) out.push(i.ev.tool);
  return out;
}
