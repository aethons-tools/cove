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

describe("useChimes", () => {
  it("never re-chimes for a stale cached rail shown on switching back", async () => {
    const { renderHook } = await import("@testing-library/react");
    const sounds = await import("./sounds");
    const { vi } = await import("vitest");
    const play = vi.spyOn(sounds, "play").mockReturnValue(true);
    const { useChimes } = await import("./chimes");
    const rail = (u: number) => ({ unreadTotal: u, groups: [{ bucket: "channels", title: "Channels", rows: [row("c", u)] }] });
    const { rerender } = renderHook(({ r, at }) => useChimes(r, at, undefined), { initialProps: { r: rail(0), at: 2 } });
    rerender({ r: rail(1), at: 3 }); // a new message: chime
    expect(play).toHaveBeenCalledTimes(1);
    rerender({ r: rail(0), at: 1 }); // a stale cache (older): ignored
    rerender({ r: rail(1), at: 4 }); // its refetch: nothing new
    expect(play).toHaveBeenCalledTimes(1);
    play.mockRestore();
  });
});
