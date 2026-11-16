import { describe, expect, it } from "vitest";
import { moveTo, ordered } from "./order";

describe("moveTo", () => {
  it("puts the dragged id at the target's place, as the prototype does", () => {
    expect(moveTo(["a", "b", "c", "d"], "a", "c")).toEqual(["b", "c", "a", "d"]);
    expect(moveTo(["a", "b", "c", "d"], "d", "b")).toEqual(["a", "d", "b", "c"]);
  });
  it("leaves the order alone for itself or an unknown id", () => {
    expect(moveTo(["a", "b"], "a", "a")).toEqual(["a", "b"]);
    expect(moveTo(["a", "b"], "x", "a")).toEqual(["a", "b"]);
    expect(moveTo(["a", "b"], "a", "x")).toEqual(["a", "b"]);
  });
});

describe("ordered", () => {
  it("sorts items by the ids, keeping the rest last in their order", () => {
    const items = [{ id: "a" }, { id: "b" }, { id: "c" }];
    expect(ordered(items, (i) => i.id, ["c", "a"]).map((i) => i.id)).toEqual(["c", "a", "b"]);
  });
});
