import { describe, expect, it } from "vitest";
import { BATCH, MAX_ELEMENT, nextRetry, pending, snapshot } from "./sync";

describe("board sync", () => {
  it("sends only versions Jam doesn't have", () => {
    const sent = new Map([["a", 2], ["b", 1]]);
    expect(pending([{ id: "a", version: 2 }, { id: "b", version: 3 }, { id: "c", version: 1 }], sent).map((e) => e.id)).toEqual(["b", "c"]);
  });

  it("batches, and never lets an oversized element block the rest", () => {
    const sent = new Map<string, number>();
    const els = Array.from({ length: BATCH + 2 }, (_, i) => ({ id: `e${i}`, version: 1 }));
    const big = { id: "big", version: 4, pad: "x".repeat(MAX_ELEMENT) };
    const s = snapshot([...els, big], sent);
    expect(s.batches.map((b) => b.length)).toEqual([BATCH, 2]);
    expect(s.tooLarge).toBe(true);
    expect(sent.get("big")).toBe(4); // settled: it is never re-tried
  });

  it("backs off by doubling to 30 s", () => {
    expect([nextRetry(1000), nextRetry(16000), nextRetry(30000)]).toEqual([2000, 30000, 30000]);
  });
});
