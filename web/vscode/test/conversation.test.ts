import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import * as vscode from "vscode";
import type { ExtToPane, PaneState } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "../src/api";
import type { Session } from "../src/auth";
import { Router } from "../src/bridge/router";
import { FocusModel } from "../src/focus";
import type { SessionTarget } from "../src/focus";
import { LiveStream } from "../src/live";
import { ConversationViewProvider } from "../src/panes/conversation";
import { PaneHub } from "../src/panes/hub";
import { fakeWebviewView, mock, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const A = "ses_0abcdefghjkmnpqrstvwxyz012";
const B = "ses_1abcdefghjkmnpqrstvwxyz012";
const CHN_A = "chn_0abcdefghjkmnpqrstvwxyz012";
const CHN_B = "chn_1abcdefghjkmnpqrstvwxyz012";
const BRD = "brd_0abcdefghjkmnpqrstvwxyz012";
const SIGNED_IN: Session = { url: JAM, token: "tok", user: "u", expiresAt: "" };
const known: Record<string, SessionTarget> = {
  [A]: { session: A, label: "alpha", channel: CHN_A },
  [B]: { session: B, label: "beta", channel: CHN_B },
};

type View = ReturnType<typeof fakeWebviewView>;

function setup(o: { windowDefault?: string; resolve?: (s: string) => Promise<SessionTarget>; fetch?: (url: string, init: RequestInit) => Promise<Response> } = {}) {
  mock.reset();
  let sess: Session | undefined = SIGNED_IN;
  const authSubs = new Set<(s: Session | undefined) => void>();
  const auth = {
    session: async (u: string) => (sess && sess.url === u ? sess : undefined),
    onChange: (cb: (s: Session | undefined) => void) => {
      authSubs.add(cb);
      return () => void authSubs.delete(cb);
    },
  };
  const setSession = (s: Session | undefined) => {
    sess = s;
    for (const cb of [...authSubs]) cb(s);
  };
  const session = async () => {
    const s = await auth.session(JAM);
    return s && { url: s.url, token: s.token };
  };
  const signals: AbortSignal[] = [];
  const liveFetch = vi.fn(
    (_u: unknown, init?: RequestInit) =>
      new Promise<Response>((_r, rej) => {
        signals.push(init!.signal!);
        init!.signal!.addEventListener("abort", () => rej(new Error("aborted")));
      }),
  );
  const live = new LiveStream({ session, fetch: liveFetch as unknown as typeof globalThis.fetch, onSignedOut: vi.fn(), setTimeout, clearTimeout, random: () => 0.5 });
  const hub = new PaneHub(live);
  const fetch = vi.fn<(url: string, init: RequestInit) => Promise<Response>>(o.fetch ?? (async () => Response.json({ ok: true })));
  const router = new Router({ session, fetch: fetch as unknown as typeof globalThis.fetch, onSignedOut: vi.fn() });
  const store = new Map<string, unknown>();
  const resolve = vi.fn(o.resolve ?? (async (s: string) => known[s] ?? { session: s, gone: true as const }));
  const model = new FocusModel({
    windowDefault: o.windowDefault,
    state: { get: <T>(k: string) => store.get(k) as T | undefined, update: async (k, v) => void store.set(k, v) },
    resolve,
  });
  const reporter = { pane: vi.fn(), drop: vi.fn() };
  const perform = vi.fn(async (_a: unknown) => undefined);
  const provider = new ConversationViewProvider({
    extensionUri: Uri.parse("file:///ext") as unknown as vs.Uri,
    hub,
    router,
    model,
    reporter,
    auth,
    jamUrl: () => JAM,
    perform,
    windowStudio: () => A,
    executeCommand: vscode.commands.executeCommand,
  });
  const open = (): View => {
    const v = fakeWebviewView();
    provider.resolveWebviewView(v as unknown as vs.WebviewView, {} as vs.WebviewViewResolveContext, {} as vs.CancellationToken);
    return v;
  };
  return { provider, open, setSession, live, liveFetch, signals, fetch, model, resolve, reporter, perform };
}

const states = (v: View) => (v.posted as ExtToPane[]).filter((m) => m.type === "state").map((m) => (m as { state: PaneState }).state);
const lastState = (v: View) => states(v).at(-1);
const settle = () => new Promise((r) => setTimeout(r, 10));

describe("ConversationViewProvider", () => {
  it("resolve sets enableScripts, enableCommandUris false, localResourceRoots dist/webview, no retainContextWhenHidden", () => {
    const v = setup().open();
    const o = v.webview.options as vs.WebviewOptions & { retainContextWhenHidden?: boolean };
    expect(o.enableScripts).toBe(true);
    expect(o.enableCommandUris).toBe(false);
    expect(o.localResourceRoots!.map(String)).toEqual(["file:///ext/dist/webview"]);
    expect(Object.keys(o).sort()).toEqual(["enableCommandUris", "enableScripts", "localResourceRoots"]);
    expect(o.retainContextWhenHidden).toBeUndefined();
    expect(v.webview.html).toContain(`src="https://x.vscode-cdn.net/file/ext/dist/webview/pane.js"`);
    expect(v.webview.html).toContain(`href="https://x.vscode-cdn.net/file/ext/dist/webview/pane.css"`);
    expect(v.webview.html).toContain(`data-pane="conversation"`);
    const nonce = /nonce="([^"]+)"/.exec(v.webview.html)![1]!;
    expect(v.webview.html).toContain(`'nonce-${nonce}'`);
    expect(v.webview.html).not.toContain("tok");
  });

  it("each render has a new nonce", () => {
    const s = setup();
    const n = (v: View) => /nonce="([^"]+)"/.exec(v.webview.html)![1];
    expect(n(s.open())).not.toBe(n(s.open()));
  });

  it("ready gets state with the window's session", async () => {
    const v = setup({ windowDefault: A }).open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: known[A], pinned: false, elsewhere: false }));
    expect(mock.context.get("jam.conversation.hasSession")).toBe(true);
    expect(mock.context.get("jam.conversation.pinned")).toBe(false);
    expect(v.description).toBe("");
    // no message to the pane carries the token or the Jam URL
    expect(JSON.stringify(v.posted)).not.toMatch(/tok|jam\.example/);
  });

  it("no window session: no target, and the pin action is hidden", async () => {
    const v = setup().open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: null, pinned: false, elsewhere: false }));
    expect(mock.context.get("jam.conversation.hasSession")).toBe(false);
  });

  it("a gone session posts the gone target", async () => {
    const C = "ses_2abcdefghjkmnpqrstvwxyz012";
    const v = setup({ windowDefault: C }).open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)?.target).toEqual({ session: C, gone: true }));
  });

  it("a failing lookup shows an error state once, with no retry loop", async () => {
    const s = setup({ windowDefault: A, resolve: async () => Promise.reject(new Error("Jam is unreachable")) });
    const v = s.open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: null, pinned: false, elsewhere: false, error: "Jam couldn't be reached." }));
    await settle();
    expect(states(v)).toHaveLength(1);
    expect(s.resolve).toHaveBeenCalledTimes(1);
    // the pane's Try again is a new ready, which looks again
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(s.resolve).toHaveBeenCalledTimes(2));
  });

  it("a lookup that finds the sign-in gone shows signed out", async () => {
    const v = setup({ windowDefault: A, resolve: async () => Promise.reject(new SignedOutError()) }).open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)).toMatchObject({ signedIn: false, target: null }));
  });

  it("a request is answered through the router", async () => {
    const s = setup({ windowDefault: A });
    const v = s.open();
    v.receive({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/bootstrap" } });
    await vi.waitFor(() => expect(v.posted).toContainEqual({ type: "response", id: 1, res: { kind: "http", status: 200, contentType: "application/json", json: { ok: true } } }));
    const [url, init] = s.fetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${JAM}/app/user/api/bootstrap`);
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer tok");
    v.receive({ type: "request", id: 2, req: { method: "POST", path: "/me/studios/ses_x/ssh/cert" } });
    await vi.waitFor(() => expect(v.posted).toContainEqual(expect.objectContaining({ type: "response", id: 2, res: expect.objectContaining({ kind: "refused" }) })));
    expect(s.fetch).toHaveBeenCalledTimes(1);
  });

  it("open board performs openBoard", async () => {
    const s = setup();
    s.open().receive({ type: "open", intent: { kind: "board", id: BRD } });
    await vi.waitFor(() => expect(s.perform).toHaveBeenCalledWith({ openBoard: BRD }));
  });

  it("open javascript: is dropped", async () => {
    const s = setup();
    const v = s.open();
    for (const url of ["javascript:alert(1)", "command:workbench.action.terminal.new", "vscode://x", "file:///etc/passwd"]) {
      v.receive({ type: "open", intent: { kind: "external", url } });
    }
    v.receive({ type: "open", intent: { kind: "nope" } });
    await settle();
    expect(s.perform).not.toHaveBeenCalled();
    expect(vi.mocked(vscode.commands.executeCommand).mock.calls.filter((c) => c[0] !== "setContext")).toEqual([]);
  });

  it("open focusSession performs the command; signIn runs jam.signIn", async () => {
    const s = setup();
    const v = s.open();
    v.receive({ type: "open", intent: { kind: "focusSession" } });
    v.receive({ type: "signIn" });
    await settle();
    expect(s.perform).toHaveBeenCalledWith({ command: "jam.focusSession" });
    const cmds = vi.mocked(vscode.commands.executeCommand).mock.calls.filter((c) => c[0] !== "setContext");
    expect(cmds).toEqual([["jam.signIn"]]);
  });

  it("a preview for the window's studio forwards; another's opens its URL", async () => {
    const s = setup();
    const v = s.open();
    v.receive({ type: "open", intent: { kind: "preview", session: A, port: 5173, url: "https://p.example/x", inBrowser: false } });
    v.receive({ type: "open", intent: { kind: "preview", session: B, port: 5173, url: "https://p.example/x", inBrowser: false } });
    await settle();
    expect(s.perform.mock.calls).toEqual([[{ forwardPort: 5173, inBrowser: false, url: "https://p.example/x" }], [{ openExternal: "https://p.example/x" }]]);
  });

  it("scrollTo posts to a resolved view, or waits for the next ready", async () => {
    const s = setup();
    s.provider.scrollTo("ask-a");
    const v = s.open();
    expect(v.posted).not.toContainEqual({ type: "scrollTo", anchor: "ask-a" });
    v.receive({ type: "ready", pane: "conversation" });
    await settle();
    expect(v.posted).toContainEqual({ type: "scrollTo", anchor: "ask-a" });
    s.provider.scrollTo("ask-b");
    expect(v.posted).toContainEqual({ type: "scrollTo", anchor: "ask-b" });
  });

  it("viewing and visibility reach the focus reporter; dispose drops the pane", () => {
    const s = setup();
    const v = s.open();
    v.receive({ type: "viewing", channel: CHN_A });
    const id = s.reporter.pane.mock.calls[0]![0];
    expect(s.reporter.pane).toHaveBeenLastCalledWith(id, { visible: true, channel: CHN_A });
    v.setVisible(false);
    expect(s.reporter.pane).toHaveBeenLastCalledWith(id, { visible: false, channel: CHN_A });
    v.receive({ type: "viewing", channel: "not a channel" });
    expect(s.reporter.pane).toHaveBeenLastCalledWith(id, { visible: false, channel: null });
    v.dispose();
    expect(s.reporter.drop).toHaveBeenCalledWith(id);
  });

  it("pinned and elsewhere → description is the label and context pinned is true", async () => {
    const s = setup({ windowDefault: A });
    const v = s.open();
    await s.model.togglePin("jam.conversation");
    await s.model.focus(B);
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: known[A], pinned: true, elsewhere: true }));
    expect(v.description).toBe("alpha");
    expect(mock.context.get("jam.conversation.pinned")).toBe(true);
    await s.model.togglePin("jam.conversation");
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: known[B], pinned: false, elsewhere: false }));
    expect(v.description).toBe("");
    expect(mock.context.get("jam.conversation.pinned")).toBe(false);
  });

  it("signed-out change posts signedIn false to every pane (Review Focus 2)", async () => {
    const s = setup({ windowDefault: A });
    const a = s.open();
    const b = s.open();
    s.setSession(undefined);
    for (const v of [a, b]) await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: false, target: null, pinned: false, elsewhere: false }));
    s.setSession(SIGNED_IN);
    for (const v of [a, b]) await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: known[A], pinned: false, elsewhere: false }));
  });

  it("a late answer doesn't overwrite a newer state", async () => {
    let release: (t: SessionTarget) => void = () => {};
    const s = setup({
      windowDefault: A,
      resolve: (id) => (id === A ? new Promise<SessionTarget>((r) => (release = r)) : Promise.resolve(known[id]!)),
    });
    const v = s.open();
    v.receive({ type: "ready", pane: "conversation" });
    await settle();
    await s.model.focus(B);
    await vi.waitFor(() => expect(lastState(v)?.target).toEqual(known[B]));
    release(known[A]!);
    await settle();
    expect(lastState(v)?.target).toEqual(known[B]);
  });

  it("dispose detaches from the hub (the stream closes with no panes)", async () => {
    const s = setup({ windowDefault: A });
    const v = s.open();
    await vi.waitFor(() => expect(s.liveFetch).toHaveBeenCalledTimes(1));
    expect(s.live.state).toBe("connecting");
    v.dispose();
    expect(s.live.state).toBe("idle");
    expect(s.signals[0]!.aborted).toBe(true);
    expect(v.listeners()).toBe(0);
    // a disposed pane no longer hears state changes
    const before = v.posted.length;
    s.setSession(undefined);
    await settle();
    expect(v.posted.length).toBe(before);
  });

  it("a non-JSON server body never reaches the error copy; a JSON message is capped", async () => {
    const html = `<html><body>${"x".repeat(500)}</body></html>`;
    let next: Error = new ApiError(html, 502, "");
    const s = setup({ windowDefault: A, resolve: async () => Promise.reject(next) });
    const v = s.open();
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)?.error).toBe("Jam answered 502."));
    next = new ApiError("y".repeat(500), 500, "internal");
    v.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(lastState(v)?.error).toBe(`${"y".repeat(200)}… (HTTP 500)`));
  });

  // A hidden webview loses its JS; shown again, its new Bridge numbers
  // requests from 1 while the old context's requests may still be in flight.
  for (const how of ["ready", "hide"] as const) {
    it(`a reloaded webview never gets the old context's answers (${how})`, async () => {
      const signals: AbortSignal[] = [];
      const releases: ((r: Response) => void)[] = [];
      const s = setup({
        windowDefault: A,
        fetch: (_u, init) =>
          new Promise<Response>((res) => {
            signals.push(init.signal!);
            releases.push(res);
          }),
      });
      const v = s.open();
      v.receive({ type: "ready", pane: "conversation" });
      v.receive({ type: "request", id: 1, req: { method: "POST", path: "/app/user/uploads", bytes: new ArrayBuffer(4), progress: true } });
      await vi.waitFor(() => expect(releases).toHaveLength(1));
      if (how === "hide") {
        v.setVisible(false);
        expect(signals[0]!.aborted).toBe(true);
        v.setVisible(true);
      }
      v.receive({ type: "ready", pane: "conversation" });
      expect(signals[0]!.aborted).toBe(true);
      v.receive({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/bootstrap" } });
      await vi.waitFor(() => expect(releases).toHaveLength(2));
      releases[0]!(Response.json({ ref: "old" }, { status: 201 }));
      await settle();
      releases[1]!(Response.json({ new: true }));
      await vi.waitFor(() => expect(v.posted.filter((m) => (m as ExtToPane).type === "response")).toHaveLength(1));
      await settle();
      const answers = (v.posted as ExtToPane[]).filter((m) => m.type === "response" || m.type === "progress");
      expect(answers).toEqual([{ type: "response", id: 1, res: { kind: "http", status: 200, contentType: "application/json", json: { new: true } } }]);
    });
  }
});
