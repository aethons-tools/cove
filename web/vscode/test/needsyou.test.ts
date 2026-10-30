import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SignedOutError } from "../src/api";
import { NEEDS_YOU_DEBOUNCE_MS, NeedsYou, badgeFor, needsYouRows } from "../src/needsyou";
import type { NeedsYouDeps } from "../src/needsyou";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };

const row = (id: string, o: Record<string, unknown> = {}) =>
  ({ id, label: `l-${id}`, project: "p", kind: "session", needsYou: true, phase: "", unread: 0, openAsks: 0, lastSeq: 0, selected: false, mission: "m", session: "", ssh: false, ...o }) as RailResponse["groups"][number]["rows"][number];
const rail = (...groups: [string, ReturnType<typeof row>[]][]): RailResponse =>
  ({ groups: groups.map(([bucket, rows]) => ({ bucket, title: bucket, rows })), unreadTotal: 0 }) as RailResponse;

function setup(over: Partial<NeedsYouDeps> = {}, answers: (() => Promise<RailResponse>)[] = []) {
  const badges: unknown[] = [];
  const signedOut: string[] = [];
  const fetches: number[] = [];
  const d: NeedsYouDeps = {
    session: async () => ({ url: "https://j", token: "tok" }),
    api: () => ({
      rail: () => {
        fetches.push(Date.now());
        return (answers.shift() ?? (async () => rail()))();
      },
    }),
    setBadge: (b) => void badges.push(b),
    onSignedOut: (t) => void signedOut.push(t),
    setTimeout,
    clearTimeout,
    ...over,
  };
  return { n: new NeedsYou(d), badges, signedOut, fetches };
}

describe("needsYouRows", () => {
  it("takes the needs-you group's rows once each", () => {
    const r = rail(
      ["needs-you", [row("a"), row("b"), row("a")]],
      ["active", [row("c", { needsYou: true }), row("a")]],
    );
    expect(needsYouRows(r).map((x) => x.id)).toEqual(["a", "b"]);
  });
});

describe("badgeFor", () => {
  it("0 is none; 1 and n have their copy", () => {
    expect(badgeFor(0)).toBeUndefined();
    expect(badgeFor(1)).toEqual({ value: 1, tooltip: "1 conversation needs you" });
    expect(badgeFor(3)).toEqual({ value: 3, tooltip: "3 conversations need you" });
  });
});

describe("NeedsYou", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("live events are debounced into one rail fetch", async () => {
    const { n, fetches, badges } = setup({}, [async () => rail(["needs-you", [row("a")]])]);
    n.onLive({ event: "changed", data: "" });
    await vi.advanceTimersByTimeAsync(1000);
    for (let i = 0; i < 4; i++) n.onLive({ event: "changed", data: "" });
    n.onLive({ event: "other", data: "" });
    await vi.advanceTimersByTimeAsync(NEEDS_YOU_DEBOUNCE_MS - 1001);
    expect(fetches).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(2);
    expect(fetches).toHaveLength(1);
    expect(badges).toEqual([{ value: 1, tooltip: "1 conversation needs you" }]);
  });

  it("signed out: no fetch, no badge", async () => {
    const { n, fetches, badges } = setup({ session: async () => undefined });
    await n.refresh();
    expect(fetches).toHaveLength(0);
    expect(badges).toEqual([undefined]);
  });

  it("a 401 signs out with the token and clears the badge", async () => {
    const { n, badges, signedOut } = setup({}, [async () => rail(["needs-you", [row("a")]]), async () => { throw new SignedOutError(); }]);
    await n.refresh();
    await n.refresh();
    expect(signedOut).toEqual(["tok"]);
    expect(badges.at(-1)).toBeUndefined();
  });

  it("a 500 keeps the last count", async () => {
    const { n, badges } = setup({}, [async () => rail(["needs-you", [row("a"), row("b")]]), async () => { throw new Error("500"); }]);
    await n.refresh();
    await n.refresh();
    expect(badges).toEqual([{ value: 2, tooltip: "2 conversations need you" }]);
  });

  it("a refresh during a refresh runs once more after it", async () => {
    let release!: (r: RailResponse) => void;
    const { n, fetches, badges } = setup({}, [
      () => new Promise<RailResponse>((r) => (release = r)),
      async () => rail(["needs-you", [row("a")]]),
    ]);
    const first = n.refresh();
    await vi.advanceTimersByTimeAsync(0);
    void n.refresh();
    void n.refresh();
    expect(fetches).toHaveLength(1);
    release(rail());
    await first;
    expect(fetches).toHaveLength(2);
    expect(badges.at(-1)).toEqual({ value: 1, tooltip: "1 conversation needs you" });
  });

});
