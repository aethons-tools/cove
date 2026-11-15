import { describe, expect, it } from "vitest";
import { moveTo } from "./RailSections";

describe("moveTo", () => {
  it("moves an item to the place of the one it's dropped on, either way", () => {
    expect(moveTo(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"]);
    expect(moveTo(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"]);
    expect(moveTo(["a", "b", "c"], "b", "c")).toEqual(["a", "c", "b"]);
  });

  it("onto itself, or with an id not in the list, is no move", () => {
    const ids = ["a", "b"];
    expect(moveTo(ids, "a", "a")).toBe(ids);
    expect(moveTo(ids, "x", "a")).toBe(ids);
    expect(moveTo(ids, "a", "x")).toBe(ids);
  });
});
