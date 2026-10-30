import { afterEach, describe, expect, it } from "vitest";
import { card } from "../../test/card";
import { forget, record, WINDOW_MS } from "./history";

describe("state history", () => {
  afterEach(forget);

  it("starts flat at the first state, and adds a point only on a change", () => {
    expect(record(card({ state: "working" }), 1000)).toEqual([{ at: 1000, state: "working", mark: false }]);
    expect(record(card({ state: "working" }), 2000)).toHaveLength(1);
    expect(record(card({ state: "idle" }), 3000)).toEqual([
      { at: 1000, state: "working", mark: false },
      { at: 3000, state: "idle", mark: false },
    ]);
  });

  it("marks an init, even with the state unchanged, and not on a lower count", () => {
    record(card({ inits: 1 }), 1000);
    expect(record(card({ inits: 2 }), 2000).at(-1)).toEqual({ at: 2000, state: "working", mark: true });
    expect(record(card({ inits: 0 }), 3000)).toHaveLength(2);
  });

  it("keeps the window, opening on the state it starts in", () => {
    record(card({ state: "idle" }), 0);
    record(card({ state: "working" }), 1000);
    record(card({ state: "thinking" }), 2000);
    const pts = record(card({ state: "working" }), WINDOW_MS + 1500);
    expect(pts.map((p) => p.state)).toEqual(["working", "thinking", "working"]);
  });

  it("keeps sessions apart", () => {
    record(card({ session: "a", state: "idle" }), 0);
    expect(record(card({ session: "b", state: "working" }), 1)).toEqual([{ at: 1, state: "working", mark: false }]);
  });
});
