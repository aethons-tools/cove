import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { useFocusReport } from "./useFocusReport";
import { useAppBadge } from "./useAppBadge";

let posts: (string | null)[];
let focused = true;
beforeEach(() => {
  posts = [];
  focused = true;
  vi.useFakeTimers();
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  vi.stubGlobal("fetch", vi.fn(async (_u: string, init: RequestInit) => {
    posts.push((JSON.parse(init.body as string) as { channel: string | null }).channel);
    return new Response(null, { status: 204 });
  }));
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function Probe({ ch, on }: { ch: string | null; on: boolean }) { useFocusReport(ch, on); return null; }

describe("focus reporting", () => {
  it("reports the open conversation while focused, null when not, and refreshes every 30 s", () => {
    const { rerender } = render(<Probe ch="chn_a" on />);
    expect(posts).toEqual(["chn_a"]);
    act(() => { vi.advanceTimersByTime(30_000); });
    expect(posts).toEqual(["chn_a", "chn_a"]);
    focused = false;
    act(() => { window.dispatchEvent(new Event("blur")); });
    expect(posts).toEqual(["chn_a", "chn_a", null]);
    act(() => { vi.advanceTimersByTime(30_000); });
    expect(posts).toEqual(["chn_a", "chn_a", null]); // nothing to refresh
    focused = true;
    act(() => { window.dispatchEvent(new Event("focus")); });
    rerender(<Probe ch="chn_b" on />);
    expect(posts).toEqual(["chn_a", "chn_a", null, "chn_a", null, "chn_b"]); // a switch clears, then reports
    act(() => { window.dispatchEvent(new Event("pagehide")); });
    expect(posts.at(-1)).toBeNull();
  });

  it("reports null when the conversation goes (no channel)", () => {
    const { rerender } = render(<Probe ch="chn_a" on />);
    rerender(<Probe ch={null} on />);
    expect(posts).toEqual(["chn_a", null]);
  });

  it("reports null when the conversation view goes away (Home, a board, /new)", () => {
    const { unmount } = render(<Probe ch="chn_a" on />);
    unmount();
    expect(posts).toEqual(["chn_a", null]);
  });

  it("does nothing when focus reporting is off", () => {
    render(<Probe ch="chn_a" on={false} />);
    act(() => { vi.advanceTimersByTime(60_000); });
    expect(posts).toEqual([]);
  });
});

describe("the app badge", () => {
  function Badge({ n }: { n?: number }) { useAppBadge(n); return null; }
  it("shows the unread total, clears at 0, only on change", () => {
    const set = vi.fn(async () => {}), clear = vi.fn(async () => {});
    Object.assign(navigator, { setAppBadge: set, clearAppBadge: clear });
    const { rerender } = render(<Badge n={3} />);
    rerender(<Badge n={3} />);
    rerender(<Badge n={0} />);
    rerender(<Badge />);
    expect(set.mock.calls).toEqual([[3]]);
    expect(clear).toHaveBeenCalledTimes(1);
  });
});
