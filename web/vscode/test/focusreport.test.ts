import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FOCUS_REFRESH_MS, FocusReporter } from "../src/focusreport.js";

function mk() {
  const post = vi.fn(async (_c: string | null) => {});
  const r = new FocusReporter({ post, setInterval, clearInterval });
  return { r, post };
}

describe("FocusReporter", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("reports the visible pane's channel only while the window is focused", () => {
    const { r, post } = mk();
    r.windowFocused(true);
    r.pane("p", { visible: true, channel: "chn_a" });
    r.windowFocused(false);
    r.windowFocused(true);
    expect(post.mock.calls).toEqual([["chn_a"], [null], ["chn_a"]]);
  });

  it("refreshes every 30 s while non-null, not while null", () => {
    const { r, post } = mk();
    r.windowFocused(true);
    r.pane("p", { visible: true, channel: "chn_a" });
    vi.advanceTimersByTime(FOCUS_REFRESH_MS * 2);
    expect(post).toHaveBeenCalledTimes(3);
    r.windowFocused(false);
    post.mockClear();
    vi.advanceTimersByTime(FOCUS_REFRESH_MS * 3);
    expect(post).not.toHaveBeenCalled();
  });

  it("hidden pane -> null", () => {
    const { r, post } = mk();
    r.windowFocused(true);
    r.pane("p", { visible: true, channel: "chn_a" });
    r.pane("p", { visible: false, channel: "chn_a" });
    expect(post.mock.calls).toEqual([["chn_a"], [null]]);
  });

  it("a switch from chn_a to chn_b posts chn_b", () => {
    const { r, post } = mk();
    r.windowFocused(true);
    r.pane("p", { visible: true, channel: "chn_a" });
    r.pane("p", { visible: true, channel: "chn_b" });
    expect(post.mock.calls).toEqual([["chn_a"], ["chn_b"]]);
  });

  it("dispose posts null, and swallows post errors", () => {
    const post = vi.fn(async () => {
      throw new Error("down");
    });
    const r = new FocusReporter({ post, setInterval, clearInterval });
    r.windowFocused(true);
    r.pane("p", { visible: true, channel: "chn_a" });
    r.dispose();
    expect(post).toHaveBeenLastCalledWith(null);
  });
});
