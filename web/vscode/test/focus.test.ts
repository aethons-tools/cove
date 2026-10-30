import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../src/api";
import { FocusModel, sessionResolver } from "../src/focus";
import type { SessionTarget } from "../src/focus";

const A = "ses_0abcdefghjkmnpqrstvwxyz012";
const B = "ses_1abcdefghjkmnpqrstvwxyz012";
const C = "ses_2abcdefghjkmnpqrstvwxyz012";

const resolveOk = vi.fn(async (s: string): Promise<SessionTarget> => ({ session: s, label: `L-${s.slice(4, 6)}`, channel: `chn_${s.slice(4, 6)}` }));

describe("sessionResolver", () => {
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
  });
});

describe("FocusModel", () => {
  it("target() is the window's session, resolved once and cached", async () => {
    const resolve = vi.fn(resolveOk);
    const m = new FocusModel({ windowSession: A, resolve });
    expect(m.windowSession()).toBe(A);
    expect(await m.target()).toEqual({ session: A, label: "L-0a", channel: "chn_0a" });
    await m.target();
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it("no window session: target() is null and resolves nothing", async () => {
    const resolve = vi.fn(resolveOk);
    const m = new FocusModel({ windowSession: undefined, resolve });
    expect(m.windowSession()).toBeUndefined();
    expect(await m.target()).toBeNull();
    expect(resolve).not.toHaveBeenCalled();
  });

  it("a gone session is its target", async () => {
    const m = new FocusModel({ windowSession: C, resolve: async (s) => ({ session: s, gone: true }) });
    expect(await m.target()).toEqual({ session: C, gone: true });
  });

  it("a failed lookup isn't cached, and evicting it doesn't drop a newer one", async () => {
    let n = 0;
    const resolve = vi.fn(async (s: string): Promise<SessionTarget> => {
      if (n++ === 0) throw new Error("first fails");
      return { session: s, label: "x", channel: "c" };
    });
    const m = new FocusModel({ windowSession: A, resolve });
    const first = m.target();
    m.invalidate();
    const second = await m.target();
    await expect(first).rejects.toThrow();
    await m.target();
    expect(second).toBeTruthy();
    expect(resolve).toHaveBeenCalledTimes(2);
  });

  it("invalidate re-resolves and fires onChange; unsubscribe stops it", async () => {
    const resolve = vi.fn(resolveOk);
    const m = new FocusModel({ windowSession: A, resolve });
    await m.target();
    const cb = vi.fn();
    const off = m.onChange(cb);
    m.invalidate();
    expect(cb).toHaveBeenCalledTimes(1);
    await m.target();
    expect(resolve).toHaveBeenCalledTimes(2);
    off();
    m.invalidate();
    expect(cb).toHaveBeenCalledTimes(1);
  });
});
