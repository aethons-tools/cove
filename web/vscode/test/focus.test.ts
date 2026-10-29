import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../src/api";
import { FocusModel, focusSessionCommand, sessionResolver } from "../src/focus";
import type { SessionTarget } from "../src/focus";

const A = "ses_0abcdefghjkmnpqrstvwxyz012";
const B = "ses_1abcdefghjkmnpqrstvwxyz012";
const C = "ses_2abcdefghjkmnpqrstvwxyz012";

function mem(init: Record<string, unknown> = {}) {
  const m = new Map<string, unknown>(Object.entries(init));
  return {
    get: <T>(k: string) => m.get(k) as T | undefined,
    update: async (k: string, v: unknown) => void (v === undefined ? m.delete(k) : m.set(k, v)),
    m,
  };
}
const resolveOk = vi.fn(async (s: string): Promise<SessionTarget> => ({ session: s, label: `L-${s.slice(4, 6)}`, channel: `chn_${s.slice(4, 6)}` }));

function model(windowDefault: string | undefined, state = mem(), resolve: (s: string) => Promise<SessionTarget> = resolveOk) {
  return { m: new FocusModel({ windowDefault, state, resolve }), state, resolve };
}

describe("FocusModel", () => {
  it("defaults to the window's studio", async () => {
    const { m } = model(A);
    expect(m.windowSession()).toBe(A);
    const s = await m.stateOf("conversation");
    expect(s).toMatchObject({ pinned: false, elsewhere: false, target: { session: A, label: "L-0a" } });
  });

  it("focus overrides and persists; focus(undefined) clears", async () => {
    const { m, state } = model(A);
    await m.focus(B);
    expect(m.windowSession()).toBe(B);
    expect(state.m.get("jam.focus")).toBe(B);
    expect(new FocusModel({ windowDefault: A, state, resolve: resolveOk }).windowSession()).toBe(B);
    await m.focus(undefined);
    expect(m.windowSession()).toBe(A);
    expect(state.m.has("jam.focus")).toBe(false);
    await m.focus(B);
    await m.focus(A);
    expect(state.m.has("jam.focus")).toBe(false);
  });

  it("a pinned view keeps its session when the window's focus changes, and reports elsewhere", async () => {
    const { m, state } = model(A);
    await m.togglePin("conversation");
    expect(m.isPinned("conversation")).toBe(true);
    expect(state.m.get("jam.pins")).toEqual({ conversation: A });
    await m.focus(B);
    const s = await m.stateOf("conversation");
    expect(s).toMatchObject({ pinned: true, elsewhere: true, target: { session: A } });
    expect(await m.stateOf("other")).toMatchObject({ pinned: false, elsewhere: false, target: { session: B } });
  });

  it("unpin follows the window again", async () => {
    const { m, state } = model(A);
    await m.togglePin("conversation");
    await m.focus(B);
    await m.togglePin("conversation");
    expect(m.isPinned("conversation")).toBe(false);
    expect(state.m.get("jam.pins")).toEqual({});
    expect(await m.stateOf("conversation")).toMatchObject({ pinned: false, elsewhere: false, target: { session: B } });
  });

  it("no default and no override -> target null", async () => {
    const { m, resolve } = model(undefined);
    expect(await m.stateOf("conversation")).toEqual({ target: null, pinned: false, elsewhere: false });
    await m.togglePin("conversation");
    expect(m.isPinned("conversation")).toBe(false);
    expect(resolve).not.toHaveBeenCalledWith(undefined);
  });

  it("a 404 session is gone, not an error; other errors propagate", async () => {
    const head = vi.fn(async (id: string) => {
      if (id === C) throw new ApiError("nope", 404, "not_found");
      if (id === B) throw new ApiError("boom", 500, "x");
      return { id, label: "Ada", channel: "chn_1" };
    });
    const resolve = sessionResolver({ sessionHead: head } as never);
    expect(await resolve(A)).toEqual({ session: A, label: "Ada", channel: "chn_1" });
    expect(await resolve(C)).toEqual({ session: C, gone: true });
    await expect(resolve(B)).rejects.toBeInstanceOf(ApiError);
    const { m } = model(A, mem({ "jam.focus": C }), resolve);
    expect(await m.stateOf("v")).toMatchObject({ target: { session: C, gone: true }, elsewhere: false });
  });

  it("focus ignores an invalid id, and a stored invalid id is ignored on restore", async () => {
    const { m, state } = model(A, mem({ "jam.focus": "ses_x/../y" }));
    expect(m.windowSession()).toBe(A);
    await m.focus("ses_x\nHost *");
    expect(state.m.get("jam.focus")).toBe("ses_x/../y");
    expect(m.windowSession()).toBe(A);
    const p = model(A, mem({ "jam.pins": { v: "../x" } })).m;
    expect(await p.stateOf("v")).toMatchObject({ target: { session: A } });
  });

  it("evicting a rejected entry does not drop a newer one", async () => {
    let n = 0;
    const resolve = vi.fn(async (s: string): Promise<SessionTarget> => {
      if (n++ === 0) throw new Error("first fails");
      return { session: s, label: "x", channel: "c" };
    });
    const { m } = model(A, mem(), resolve);
    const first = m.stateOf("v");
    m.invalidate();
    const second = await m.stateOf("v");
    await expect(first).rejects.toThrow();
    await m.stateOf("v");
    expect(second.target).toBeTruthy();
    expect(resolve).toHaveBeenCalledTimes(2);
  });

  it("caches targets until invalidate, which re-resolves and fires onChange", async () => {
    const resolve = vi.fn(resolveOk);
    const { m } = model(A, mem(), resolve);
    await m.stateOf("v");
    await m.stateOf("w");
    expect(resolve).toHaveBeenCalledTimes(1);
    const cb = vi.fn();
    m.onChange(cb);
    m.invalidate();
    expect(cb).toHaveBeenCalledTimes(1);
    await m.stateOf("v");
    expect(resolve).toHaveBeenCalledTimes(2);
  });

  it("onChange fires on focus and pin, and unsubscribes", async () => {
    const { m } = model(A);
    const cb = vi.fn();
    const off = m.onChange(cb);
    await m.focus(B);
    await m.togglePin("v");
    expect(cb).toHaveBeenCalledTimes(2);
    off();
    await m.focus(undefined);
    expect(cb).toHaveBeenCalledTimes(2);
  });
});

const row = (id: string) => ({ id, label: id, project: "p", kind: "studio", needsYou: false, mission: "m" });

function cmd(over: { conv?: unknown; hasDefault?: boolean; pickIndex?: number } = {}) {
  const rail = { groups: [{ bucket: "active", title: "S", rows: [row("chn_a")] }], unreadTotal: 0 };
  const api = { rail: vi.fn(async () => rail), conversation: vi.fn(async () => over.conv ?? { session: B }) };
  const ui = {
    pick: vi.fn(async (items: { label: string }[], _p: string) => items[over.pickIndex ?? 0]),
    error: vi.fn(async () => undefined),
  };
  const model = { focus: vi.fn(async () => {}) };
  const run = () => focusSessionCommand({ api: api as never, ui: ui as never, model: model as never, hasDefault: over.hasDefault ?? true });
  return { api, ui, model, run };
}

describe("focusSessionCommand", () => {
  it("This window's studio resets", async () => {
    const c = cmd();
    await c.run();
    expect(c.ui.pick.mock.calls[0]![1]).toBe("Focus a session (unpinned Jam panes follow it)");
    expect((c.ui.pick.mock.calls[0]![0] as { label: string }[])[0]!.label).toBe("$(home) This window's studio");
    expect(c.model.focus).toHaveBeenCalledWith(undefined);
  });

  it("a pick of a row with a session focuses it", async () => {
    const c = cmd({ hasDefault: false });
    await c.run();
    expect((c.ui.pick.mock.calls[0]![0] as { label: string }[]).map((i) => i.label)).toEqual(["chn_a"]);
    expect(c.api.conversation).toHaveBeenCalledWith("chn_a");
    expect(c.model.focus).toHaveBeenCalledWith(B);
  });

  it("a row whose session is malformed shows the message", async () => {
    const c = cmd({ conv: { session: "ses_x/../y" }, pickIndex: 1 });
    await c.run();
    expect(c.ui.error).toHaveBeenCalledWith("That conversation has no session to focus");
    expect(c.model.focus).not.toHaveBeenCalled();
  });

  it("a row without one shows the message", async () => {
    const c = cmd({ conv: { session: "" }, pickIndex: 1 });
    await c.run();
    expect(c.ui.error).toHaveBeenCalledWith("That conversation has no session to focus");
    expect(c.model.focus).not.toHaveBeenCalled();
  });
});
