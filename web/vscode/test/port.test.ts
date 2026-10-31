import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import type { ExtToPane, PaneState } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { IntentAction } from "../src/bridge/intents";
import { paneHtml, THINKING_SUBS_PER_PANE, wirePane } from "../src/panes/port";
import type { ThinkingSignal } from "../src/thinking";
import { fakeWebviewView, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const A = "ses_0abcdefghjkmnpqrstvwxyz012";
const B = "ses_1abcdefghjkmnpqrstvwxyz012";
const STR = "0123456789abcdef0123456789abcdef";
const STATE: PaneState = { signedIn: true, target: null };

type Sub = { session: string; stream: string; cb: (s: ThinkingSignal) => void; onEnd?: () => void; unsub: ReturnType<typeof vi.fn> };

function setup() {
  const v = fakeWebviewView();
  const subs: Sub[] = [];
  const thinking = {
    subscribe: vi.fn((session: string, stream: string, cb: (s: ThinkingSignal) => void, onEnd?: () => void) => {
      const unsub = vi.fn();
      subs.push({ session, stream, cb, onEnd, unsub });
      return unsub;
    }),
  };
  const detach = vi.fn();
  const hub = { attach: vi.fn(() => detach) };
  const router = { handle: vi.fn() };
  const perform = vi.fn((_a: IntentAction) => undefined);
  const executeCommand = vi.fn(async (..._a: unknown[]) => undefined);
  const port = wirePane(
    v.webview as unknown as vs.Webview,
    "thinking-1",
    { hub, router, thinking, jamUrl: () => JAM, windowStudio: A, perform, executeCommand },
    { state: async () => STATE },
  );
  const posted = () => v.posted as ExtToPane[];
  return { v, port, subs, thinking, hub, detach, router, perform, executeCommand, posted };
}

const settle = () => new Promise((r) => setTimeout(r, 10));
const errors = (p: ExtToPane[]) => p.filter((m) => m.type === "thinkingError");

describe("wirePane", () => {
  it("allows two Thinking subscriptions per pane", () => {
    expect(THINKING_SUBS_PER_PANE).toBe(2);
  });

  it("thinkingOpen subscribes and forwards events under the pane's sub id", () => {
    const s = setup();
    s.v.receive({ type: "thinkingOpen", sub: 7, session: A, stream: STR });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(1);
    expect(s.subs[0]).toMatchObject({ session: A, stream: STR });
    s.subs[0]!.cb({ event: "open", data: "" });
    s.subs[0]!.cb({ event: "ev", data: '{"seq":1}' });
    s.subs[0]!.cb({ error: true, closed: false });
    expect(s.posted()).toEqual([
      { type: "thinking", sub: 7, event: "open", data: "" },
      { type: "thinking", sub: 7, event: "ev", data: '{"seq":1}' },
      { type: "thinkingError", sub: 7, closed: false },
    ]);
  });

  it("a third open sub is refused with a closed error (Review Focus 3)", () => {
    const s = setup();
    for (let sub = 1; sub <= 50; sub++) s.v.receive({ type: "thinkingOpen", sub, session: A, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(THINKING_SUBS_PER_PANE);
    expect(errors(s.posted())).toHaveLength(48);
    expect(errors(s.posted())[0]).toEqual({ type: "thinkingError", sub: 3, closed: true });
  });

  it("a bad session, a non-hex stream or a reused sub is refused without subscribing", () => {
    const s = setup();
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(1);
    const bad = [
      { sub: 2, session: "../x", stream: "" },
      { sub: 3, session: "chn_0abcdefghjkmnpqrstvwxyz012", stream: "" },
      { sub: 4, session: A, stream: "Z".repeat(32) },
      { sub: 5, session: A, stream: "../events" },
      { sub: 6, session: A, stream: 7 },
      { sub: 1, session: B, stream: "" },
      { sub: 1.5, session: B, stream: "" },
      { sub: -1, session: B, stream: "" },
    ];
    for (const m of bad) s.v.receive({ type: "thinkingOpen", ...m });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(1);
    expect(errors(s.posted()).map((m) => (m as { sub: number }).sub)).toEqual([2, 3, 4, 5, 6, 1, 1.5, -1]);
    for (const m of errors(s.posted())) expect(m).toMatchObject({ closed: true });
    // a sub id that isn't a number at all is dropped
    s.v.receive({ type: "thinkingOpen", sub: "x", session: B, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(1);
    expect(errors(s.posted())).toHaveLength(8);
  });

  it("a subscribe that throws is refused with a closed error", () => {
    const s = setup();
    s.thinking.subscribe.mockImplementationOnce(() => {
      throw new Error("Not a session id.");
    });
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    expect(s.posted()).toEqual([{ type: "thinkingError", sub: 1, closed: true }]);
    s.v.receive({ type: "thinkingOpen", sub: 2, session: A, stream: "" });
    s.v.receive({ type: "thinkingOpen", sub: 3, session: A, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(3);
  });

  it("dispose unsubscribes every feed and detaches", () => {
    const s = setup();
    expect(s.hub.attach).toHaveBeenCalledWith(expect.objectContaining({ id: "thinking-1" }));
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    s.v.receive({ type: "thinkingOpen", sub: 2, session: B, stream: STR });
    s.port.dispose();
    expect(s.subs.map((x) => x.unsub.mock.calls.length)).toEqual([1, 1]);
    expect(s.detach).toHaveBeenCalledTimes(1);
    expect(s.v.listeners()).toBe(0);
    // nothing reaches a disposed pane, and it opens nothing
    s.subs[0]!.cb({ event: "ev", data: "{}" });
    s.v.receive({ type: "thinkingOpen", sub: 3, session: A, stream: "" });
    expect(s.posted()).toEqual([]);
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(2);
  });

  it("ready closes the old context's feeds, so a reloaded view's sub 1 is accepted", async () => {
    const s = setup();
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    s.v.receive({ type: "thinkingOpen", sub: 2, session: A, stream: STR });
    s.v.receive({ type: "ready", pane: "thinking" });
    expect(s.subs.map((x) => x.unsub.mock.calls.length)).toEqual([1, 1]);
    // a late signal from a closed feed doesn't reach the new context
    s.subs[0]!.cb({ event: "ev", data: "{}" });
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(3);
    await settle();
    expect(s.posted()).toEqual([{ type: "state", state: STATE }]);
  });

  it("a key's end frees the slot; thinkingClose twice is harmless", () => {
    const s = setup();
    s.v.receive({ type: "thinkingOpen", sub: 1, session: A, stream: "" });
    s.v.receive({ type: "thinkingOpen", sub: 2, session: A, stream: STR });
    s.subs[0]!.cb({ error: true, closed: true });
    s.subs[0]!.onEnd!();
    s.v.receive({ type: "thinkingOpen", sub: 3, session: A, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(3);
    s.v.receive({ type: "thinkingClose", sub: 2 });
    s.v.receive({ type: "thinkingClose", sub: 2 });
    s.v.receive({ type: "thinkingClose", sub: 99 });
    s.v.receive({ type: "thinkingClose", sub: 1 }); // ended: nothing to close
    expect(s.subs.map((x) => x.unsub.mock.calls.length)).toEqual([0, 1, 0]);
    s.v.receive({ type: "thinkingOpen", sub: 4, session: B, stream: "" });
    expect(s.thinking.subscribe).toHaveBeenCalledTimes(4);
    expect(errors(s.posted())).toEqual([{ type: "thinkingError", sub: 1, closed: true }]);
  });

  it("open preview for the window's studio performs forwardPort", () => {
    const s = setup();
    s.v.receive({ type: "open", intent: { kind: "preview", session: A, port: 5173, url: "https://p.example/", inBrowser: true } });
    s.v.receive({ type: "open", intent: { kind: "preview", session: B, port: 5173, url: "https://p.example/", inBrowser: true } });
    expect(s.perform.mock.calls).toEqual([[{ forwardPort: 5173, inBrowser: true, url: "https://p.example/" }], [{ openExternal: "https://p.example/" }]]);
  });

  it("open javascript: performs nothing", () => {
    const s = setup();
    s.v.receive({ type: "open", intent: { kind: "external", url: "javascript:alert(1)" } });
    for (const port of [0, 70000, "22"]) s.v.receive({ type: "open", intent: { kind: "preview", session: A, port, url: "https://p.example/", inBrowser: false } });
    s.v.receive({ type: "open", intent: { kind: "reveal", anchor: 'x" onmouseover="' } });
    s.v.receive({ type: "open", intent: { kind: "session", id: "ses_bad" } });
    s.v.receive({ type: "open", intent: null });
    expect(s.perform).not.toHaveBeenCalled();
    expect(s.executeCommand).not.toHaveBeenCalled();
  });

  it("viewing and title reach their hooks", () => {
    const v = fakeWebviewView();
    const onViewing = vi.fn();
    const onTitle = vi.fn();
    wirePane(
      v.webview as unknown as vs.Webview,
      "x-1",
      { hub: { attach: () => () => undefined }, router: { handle: vi.fn() }, thinking: { subscribe: vi.fn() }, jamUrl: () => JAM, windowStudio: undefined, perform: vi.fn(), executeCommand: vi.fn() },
      { state: async () => STATE, onViewing, onTitle },
    );
    v.receive({ type: "viewing", channel: null });
    v.receive({ type: "title", title: "Board" });
    expect(onViewing).toHaveBeenCalledWith(null);
    expect(onTitle).toHaveBeenCalledWith("Board");
  });

  it("refresh posts the state; one overtaken by a newer refresh is dropped", async () => {
    const v = fakeWebviewView();
    const answers: ((s: PaneState) => void)[] = [];
    const port = wirePane(
      v.webview as unknown as vs.Webview,
      "x-2",
      { hub: { attach: () => () => undefined }, router: { handle: vi.fn() }, thinking: { subscribe: vi.fn() }, jamUrl: () => JAM, windowStudio: undefined, perform: vi.fn(), executeCommand: vi.fn() },
      { state: () => new Promise<PaneState>((r) => answers.push(r)) },
    );
    const first = port.refresh();
    const second = port.refresh();
    answers[1]!({ ...STATE, error: "newer" });
    answers[0]!(STATE);
    expect(await first).toBeUndefined();
    expect(await second).toEqual({ ...STATE, error: "newer" });
    expect(v.posted).toEqual([{ type: "state", state: { ...STATE, error: "newer" } }]);
  });
});

describe("paneHtml", () => {
  it("sets the view options and renders the named pane under the CSP", () => {
    const v = fakeWebviewView();
    const html = paneHtml(v.webview as unknown as vs.Webview, Uri.parse("file:///ext") as unknown as vs.Uri, "status");
    expect(v.webview.options).toEqual({ enableScripts: true, enableCommandUris: false, localResourceRoots: [expect.anything()] });
    expect(v.webview.options.localResourceRoots!.map(String)).toEqual(["file:///ext/dist/webview"]);
    expect(html).toContain(`data-pane="status"`);
    expect(html).toContain(`src="https://x.vscode-cdn.net/file/ext/dist/webview/pane.js"`);
    expect(html).toContain(
      "default-src 'none'; script-src 'nonce-",
    );
    expect(html).toContain(
      "style-src https://x.vscode-cdn.net 'unsafe-inline'; font-src https://x.vscode-cdn.net; img-src https://x.vscode-cdn.net blob: data:; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'",
    );
  });
});
