import { describe, expect, it } from "vitest";
import type { ChannelRow } from "../api/types";
import { chimeFor, snapshot } from "./chimes";

const row = (id: string, unread: number, needsYou = false) => ({ id, unread, needsYou }) as ChannelRow;

describe("chimeFor", () => {
  it("is silent for the first rail (the baseline)", () => {
    expect(chimeFor(null, [row("a", 3)], undefined, false)).toBe("");
  });
  it("chimes messages for more unread, needs when something starts needing you", () => {
    const prev = snapshot([row("a", 1), row("b", 0)]);
    expect(chimeFor(prev, [row("a", 2), row("b", 0)], undefined, false)).toBe("messages");
    expect(chimeFor(prev, [row("a", 2), row("b", 0, true)], undefined, false)).toBe("needs");
    expect(chimeFor(prev, [row("a", 1), row("b", 0)], undefined, false)).toBe("");
    expect(chimeFor(prev, [row("a", 0)], undefined, false)).toBe(""); // read: no chime
  });
  it("never chimes for the conversation you're looking at", () => {
    const prev = snapshot([row("a", 1)]);
    expect(chimeFor(prev, [row("a", 2)], "a", true)).toBe("");
    expect(chimeFor(prev, [row("a", 2)], "a", false)).toBe("messages"); // open but not looking
    expect(chimeFor(prev, [row("c", 1)], "a", true)).toBe("messages"); // a new conversation
  });
});
