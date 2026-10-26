import { describe, expect, it } from "vitest";
import type { Item } from "./feed";
import { defaultPrefs, type ThinkingPrefs } from "./prefs";
import { deriveRows, toolsIn } from "./rows";
import { tev } from "./testing";

const evs = (...e: ReturnType<typeof tev>[]): Item[] => e.map((ev) => ({ t: "ev", ev }));
const prefs = (over: { f?: Partial<ThinkingPrefs["f"]>; tool?: string } = {}): ThinkingPrefs => {
  const d = defaultPrefs();
  return { ...d, tool: over.tool ?? d.tool, f: { ...d.f, ...over.f } };
};

describe("the Thinking rows", () => {
  const call = tev(2, { kind: "tool_call", tool: "Bash", toolUseId: "tu1", text: "go test", at: 1000 });
  const res = tev(3, { kind: "tool_result", tool: "Bash", toolUseId: "tu1", text: "ok", detail: Array.from({ length: 45 }, (_, i) => `l${i}`).join("\n"), at: 4100 });

  it("folds a tool result into its call: status, time, output", () => {
    const rows = deriveRows(evs(tev(1), call, res), prefs(), "");
    expect(rows.map((r) => r.kind)).toEqual(["text", "tool_call"]);
    expect(rows[1]).toMatchObject({ icon: "▶", status: "✓ 3.1s", err: false, text: "go test" });
    expect(rows[1]!.output.split("\n")).toHaveLength(45);
    const fail = deriveRows(evs(call, tev(3, { kind: "tool_result", toolUseId: "tu1", isError: true, at: 1500 })), prefs(), "");
    expect(fail[0]).toMatchObject({ status: "✗ 500ms", err: true });
    expect(deriveRows(evs(call), prefs(), "")[0]!.status).toBe("…");
  });

  it("pairs a result that arrived before its call (an older page)", () => {
    const rows = deriveRows(evs(res, call), prefs(), "");
    expect(rows.map((r) => r.kind)).toEqual(["tool_call"]);
    expect(rows[0]!.status).toMatch(/^✓/);
  });

  it("labels results as episode dividers: the cost since the session's previous result, else its total", () => {
    const r1 = tev(4, { kind: "result", turn: 1, costUsd: 0.12, durationMs: 38000, claudeSession: "c1" });
    const r2 = tev(9, { kind: "result", turn: 2, costUsd: 0.5, durationMs: 1200, claudeSession: "c1" });
    const r3 = tev(12, { kind: "result", turn: 3, costUsd: 0.05, durationMs: 900, claudeSession: "c1" }); // a cleared session: lower
    const rows = deriveRows(evs(r1, r2, r3), prefs(), "");
    expect(rows.map((r) => r.text)).toEqual([
      "episode 1 · $0.12 session total · 38s", "episode 2 · $0.38 · 1.2s", "episode 3 · $0.05 session total · 900ms"]);
  });

  it("collapses a run of empty thinking into its first; hidden progress doesn't break it", () => {
    const think = (seq: number) => tev(seq, { kind: "thinking", text: " " });
    const items = evs(think(1), tev(2, { kind: "progress", text: "…" }), think(3), think(4), tev(5), think(6));
    const rows = deriveRows(items, prefs(), "");
    expect(rows.map((r) => [r.kind, r.text])).toEqual([["thinking", "thinking ×3"], ["text", "said 5"], ["thinking", "thinking"]]);
    expect(rows[0]!.empty).toBe(true);
    // With Progress shown, the progress line splits the run.
    expect(deriveRows(items, prefs({ f: { progress: true } }), "").map((r) => r.text)).toEqual(["thinking", "…", "thinking ×2", "said 5", "thinking"]);
  });

  it("filters by chip, errors, tool and search; gaps and markers always show", () => {
    const items: Item[] = [...evs(tev(1, { kind: "thinking", text: "hmm" }), tev(2, { kind: "prompt", text: "do it" }), call, res,
      tev(5, { kind: "tool_call", tool: "Edit", toolUseId: "tu2", text: "edit x" }), tev(6, { kind: "tool_result", toolUseId: "tu2", isError: true }),
      tev(7, { kind: "gap", text: "3 events lost" })), { t: "skip", key: "skip:1", stream: "s1", from: 8, to: 9 }];
    const shown = (p: ThinkingPrefs, q = "") => deriveRows(items, p, q).map((r) => r.marker ?? r.text);
    expect(shown(prefs({ f: { thinking: false } }))).toEqual(["do it", "go test", "edit x", "3 events lost", "skip"]);
    expect(shown(prefs({ f: { text: false } }))).toEqual(["hmm", "go test", "edit x", "3 events lost", "skip"]);
    expect(shown(prefs({ f: { tools: false } }))).toEqual(["hmm", "do it", "3 events lost", "skip"]);
    expect(shown(prefs({ f: { errors: true } }))).toEqual(["hmm", "do it", "edit x", "3 events lost", "skip"]);
    expect(shown(prefs({ tool: "Edit" }))).toEqual(["edit x", "3 events lost", "skip"]);
    expect(shown(prefs(), "l44")).toEqual(["go test", "3 events lost", "skip"]); // the folded output is searched
    expect(toolsIn(items)).toEqual(["Bash", "Edit"]);
  });
});
