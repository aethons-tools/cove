import { describe, expect, it, vi } from "vitest";
import type { ExtToPane } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { PaneHub } from "../src/panes/hub.js";
import type { LiveEvent, LiveStream } from "../src/live.js";

function fakeLive() {
  let cb: ((e: LiveEvent) => void) | undefined;
  const unsub = vi.fn(() => void (cb = undefined));
  const live = { subscribe: vi.fn((f: (e: LiveEvent) => void) => ((cb = f), unsub)) } as unknown as LiveStream;
  return { live, emit: (e: LiveEvent) => cb?.(e), unsub };
}
const port = (id: string) => {
  const got: ExtToPane[] = [];
  return { id, post: (m: ExtToPane) => void got.push(m), got };
};

describe("PaneHub", () => {
  it("open, changed, presence and board reach every pane; notify and message don't", () => {
    const f = fakeLive();
    const seen: string[] = [];
    const hub = new PaneHub(f.live, (e) => seen.push(e.event));
    const a = port("a");
    const b = port("b");
    hub.attach(a);
    hub.attach(b);
    expect(f.live.subscribe).toHaveBeenCalledTimes(1);
    for (const event of ["open", "changed", "presence", "board", "notify", "message"]) f.emit({ event, data: "d" });
    const want = ["open", "changed", "presence", "board"].map((event) => ({ type: "live", event, data: "d" }));
    expect(a.got).toEqual(want);
    expect(b.got).toEqual(want);
    expect(seen).toEqual(["open", "changed", "presence", "board", "notify", "message"]);
  });

  it("detach of the last pane unsubscribes", () => {
    const f = fakeLive();
    const hub = new PaneHub(f.live);
    const d1 = hub.attach(port("a"));
    const d2 = hub.attach(port("b"));
    d1();
    expect(f.unsub).not.toHaveBeenCalled();
    d2();
    expect(f.unsub).toHaveBeenCalledTimes(1);
  });
});
