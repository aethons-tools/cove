import { afterEach, describe, expect, it, vi } from "vitest";
import { clampSize, isTab, loadLayout, saveLayout } from "./layout";

afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

describe("the pane layout", () => {
  it("reads what the old Inbox wrote, per session, else the last used", () => {
    localStorage.setItem("jam-pane:ses_a", JSON.stringify({ tab: "status", place: "below", w: 500, h: 300 }));
    localStorage.setItem("jam-pane:last", JSON.stringify({ tab: "artifacts", place: "right" }));
    expect(loadLayout("ses_a")).toEqual({ tab: "status", place: "below", w: 500, h: 300 });
    expect(loadLayout("ses_new")).toEqual({ tab: "artifacts", place: "right" });
  });

  it("falls back to closed, on the right, for anything unreadable", () => {
    const closed = { tab: null, place: "right" };
    expect(loadLayout("ses_x")).toEqual(closed);
    for (const v of ["null", "42", "[1]", "{not json", JSON.stringify({ tab: "bogus", place: "sideways", w: -5, h: "big" })]) {
      localStorage.setItem("jam-pane:ses_x", v);
      expect(loadLayout("ses_x")).toEqual(closed);
    }
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    expect(loadLayout("ses_x")).toEqual(closed);
  });

  it("saves to the session and to last, and survives a storage that throws", () => {
    saveLayout("ses_a", { tab: "previews", place: "right", w: 420 });
    expect(JSON.parse(localStorage.getItem("jam-pane:ses_a")!)).toEqual({ tab: "previews", place: "right", w: 420 });
    expect(localStorage.getItem("jam-pane:last")).toBe(localStorage.getItem("jam-pane:ses_a"));
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("full"); });
    expect(() => saveLayout("ses_a", { tab: null, place: "right" })).not.toThrow();
  });

  it("clamps the pane between 200px and leaving the messages 240px", () => {
    expect(clampSize(100, 1000)).toBe(200);
    expect(clampSize(900, 1000)).toBe(760);
    expect(clampSize(400, 1000)).toBe(400);
    expect(clampSize(400, 300)).toBe(200); // too small for both: the pane keeps its minimum
  });

  it("knows the tabs", () => {
    expect(isTab("studio")).toBe(false); // the Studio tab was removed
    expect(isTab("bogus")).toBe(false);
    expect(isTab(null)).toBe(false);
  });
});
