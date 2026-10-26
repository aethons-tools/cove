import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultPrefs, loadPrefs, savePrefs } from "./prefs";

afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

describe("thinking prefs", () => {
  it("defaults, and reads what the old Inbox wrote, keeping only valid parts", () => {
    expect(loadPrefs()).toEqual(defaultPrefs());
    localStorage.setItem("jam-thinking", JSON.stringify({ f: { thinking: false, errors: true, text: "yes" }, tool: "Bash", format: "raw" }));
    expect(loadPrefs()).toEqual({ f: { thinking: false, text: true, tools: true, errors: true, progress: false }, tool: "Bash", format: "raw" });
    localStorage.setItem("jam-thinking", JSON.stringify({ format: "fancy", tool: 3 }));
    expect(loadPrefs()).toEqual(defaultPrefs());
    localStorage.setItem("jam-thinking", "{nope");
    expect(loadPrefs()).toEqual(defaultPrefs());
  });

  it("saves the old Inbox's shape and survives a storage that throws", () => {
    const p = { ...defaultPrefs(), tool: "Edit" };
    savePrefs(p);
    expect(JSON.parse(localStorage.getItem("jam-thinking")!)).toEqual({ f: p.f, tool: "Edit", format: "narrative" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("full"); });
    expect(() => savePrefs(p)).not.toThrow();
  });
});
