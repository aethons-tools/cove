import { describe, expect, it, vi } from "vitest";
import { perform } from "../src/panes/actions";
import type { ActionDeps } from "../src/panes/actions";

function deps(o: Partial<ActionDeps> = {}) {
  const d = {
    openExternal: vi.fn(),
    executeCommand: vi.fn(async () => undefined),
    asExternalUri: vi.fn(async () => "http://localhost:61234/"),
    openBoard: vi.fn(),
    openSession: vi.fn(),
    goTo: vi.fn(),
    revealConversation: vi.fn(),
    error: vi.fn(),
    ...o,
  };
  return d as typeof d & ActionDeps;
}
const fwd = { forwardPort: 5173, inBrowser: false, url: "https://p.example/x" };

describe("perform", () => {
  it("forward opens the Simple Browser on the external URI", async () => {
    const d = deps();
    await perform(fwd, d);
    expect(d.asExternalUri).toHaveBeenCalledWith("http://localhost:5173/");
    expect(d.executeCommand).toHaveBeenCalledWith("simpleBrowser.show", "http://localhost:61234/");
    expect(d.openExternal).not.toHaveBeenCalled();
  });
  it("inBrowser opens it externally", async () => {
    const d = deps();
    await perform({ ...fwd, inBrowser: true }, d);
    expect(d.openExternal).toHaveBeenCalledWith("http://localhost:61234/");
    expect(d.executeCommand).not.toHaveBeenCalled();
  });
  it("asExternalUri throwing falls back to the preview URL in the browser, and shows the copy", async () => {
    const d = deps({ asExternalUri: vi.fn(async () => Promise.reject(new Error("no tunnel"))) });
    await perform(fwd, d);
    expect(d.openExternal).toHaveBeenCalledWith("https://p.example/x");
    expect(d.error).toHaveBeenCalledWith("Couldn't forward port 5173: no tunnel");
    expect(d.executeCommand).not.toHaveBeenCalled();
  });
  it("asExternalUri returning the same localhost URI counts as forwarded", async () => {
    const d = deps({ asExternalUri: vi.fn(async () => "http://localhost:5173/") });
    await perform(fwd, d);
    expect(d.executeCommand).toHaveBeenCalledWith("simpleBrowser.show", "http://localhost:5173/");
    expect(d.openExternal).not.toHaveBeenCalled();
    expect(d.error).not.toHaveBeenCalled();
  });
  it("reveal reaches revealConversation with the anchor, or null", async () => {
    const d = deps();
    await perform({ revealConversation: "ask-a1" }, d);
    await perform({ revealConversation: null }, d);
    expect(vi.mocked(d.revealConversation).mock.calls).toEqual([["ask-a1"], [null]]);
    expect(d.executeCommand).not.toHaveBeenCalled();
  });
  it("openBoard and openSession reach their deps", async () => {
    const d = deps();
    await perform({ openBoard: "brd_x" }, d);
    await perform({ openSession: "ses_x" }, d);
    await perform({ openExternal: "https://a.example/" }, d);
    expect(d.openBoard).toHaveBeenCalledWith("brd_x");
    expect(d.openSession).toHaveBeenCalledWith("ses_x");
    expect(d.openExternal).toHaveBeenCalledWith("https://a.example/");
    expect(d.executeCommand).not.toHaveBeenCalled();
  });
  it("goTo reaches its dep", async () => {
    const d = deps();
    await perform({ goTo: { channel: "c", session: "s" } }, d);
    expect(d.goTo).toHaveBeenCalledWith("c", "s");
  });
  it("dep errors are caught and shown", async () => {
    const d = deps({ openBoard: vi.fn(() => { throw new Error("boom"); }) });
    await perform({ openBoard: "brd_x" }, d);
    expect(d.error).toHaveBeenCalledWith("boom");
  });
});
