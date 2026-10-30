import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SignedOutError } from "../src/api";
import { NEEDS_YOU_DEBOUNCE_MS, NeedsYou, badgeFor, needsYouRows } from "../src/needsyou";
import type { NeedsYouDeps } from "../src/needsyou";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };

const row = (id: string, o: Record<string, unknown> = {}) =>
  ({ id, label: `l-${id}`, project: "p", kind: "session", needsYou: true, phase: "", unread: 0, openAsks: 0, lastSeq: 0, selected: false, mission: "m", ...o }) as RailResponse["groups"][number]["rows"][number];
const rail = (...groups: [string, ReturnType<typeof row>[]][]): RailResponse =>
  ({ groups: groups.map(([bucket, rows]) => ({ bucket, title: bucket, rows })), unreadTotal: 0 }) as RailResponse;

function setup(over: Partial<NeedsYouDeps> = {}, answers: (() => Promise<RailResponse>)[] = []) {
  const badges: unknown[] = [];
  const ctx: boolean[] = [];
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
    setContext: (_k, v) => void ctx.push(v),
    onSignedOut: (t) => void signedOut.push(t),
    setTimeout,
    clearTimeout,
    ...over,
  };
  return { n: new NeedsYou(d), badges, ctx, signedOut, fetches };
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
    expect(n.getChildren().map((r) => r.id)).toEqual(["a"]);
  });

  it("signed out: no fetch, no badge, signedIn false", async () => {
    const { n, fetches, badges, ctx } = setup({ session: async () => undefined });
    await n.refresh();
    expect(fetches).toHaveLength(0);
    expect(badges).toEqual([undefined]);
    expect(ctx).toEqual([false]);
    expect(n.getChildren()).toEqual([]);
  });

  it("a 401 signs out with the token and clears the badge", async () => {
    const { n, badges, ctx, signedOut } = setup({}, [async () => rail(["needs-you", [row("a")]]), async () => { throw new SignedOutError(); }]);
    await n.refresh();
    await n.refresh();
    expect(signedOut).toEqual(["tok"]);
    expect(badges.at(-1)).toBeUndefined();
    expect(ctx.at(-1)).toBe(false);
    expect(n.getChildren()).toEqual([]);
  });

  it("a 500 keeps the last count", async () => {
    const { n, badges } = setup({}, [async () => rail(["needs-you", [row("a"), row("b")]]), async () => { throw new Error("500"); }]);
    await n.refresh();
    await n.refresh();
    expect(badges).toEqual([{ value: 2, tooltip: "2 conversations need you" }]);
    expect(n.getChildren()).toHaveLength(2);
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

  it("the tree item opens through jam.needsYou.open with the row", () => {
    const { n } = setup();
    const r = row("a");
    const item = n.getTreeItem(r);
    expect(item.label).toBe("l-a");
    expect(item.description).toBe("p");
    expect(item.tooltip).toBe("m");
    expect(item.iconPath).toMatchObject({ id: "bell" });
    expect(item.command).toEqual({ command: "jam.needsYou.open", title: "Open", arguments: [r] });
  });

  it("tree item text is plain, with no bidi controls and no link syntax", () => {
    const { n } = setup();
    const item = n.getTreeItem(row("a", { label: "[x](command:foo)\u202e", mission: "[[y](z)](https://e.x)\u2066", project: "p\u202e" }));
    for (const v of [item.label, item.tooltip, item.description]) {
      expect(typeof v).toBe("string");
      expect(v as string).not.toMatch(/\]\(|[\u202a-\u202e\u2066-\u2069]/);
    }
    expect((item.label as string).replace(/\u200b/g, "")).toBe("[x](command:foo)");
  });

  it("signedIn is set from the session before the rail fetch, and stays set when it fails", async () => {
    let fail!: (e: Error) => void;
    const { n, ctx, fetches } = setup({}, [() => new Promise<RailResponse>((_r, j) => (fail = j))]);
    const p = n.refresh();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetches).toHaveLength(1);
    expect(ctx).toEqual([true]);
    fail(new Error("unreachable"));
    await p;
    expect(ctx).toEqual([true]);
  });
});
