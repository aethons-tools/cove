import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import * as vscode from "vscode";
import type { ExtToPane } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Session } from "../src/auth";
import { BoardPanels, tabTitle, TITLE_MAX } from "../src/panes/board";
import { paneHtml } from "../src/panes/port";
import { fakeWebviewPanel, mock, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const BRD = "brd_0abcdefghjkmnpqrstvwxyz012";
const BRD2 = "brd_1abcdefghjkmnpqrstvwxyz012";
const SIGNED_IN: Session = { url: JAM, token: "tok", user: "u", expiresAt: "" };
const CDN = "https://x.vscode-cdn.net";

function setup() {
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
  const detach = vi.fn();
  const hub = { attach: vi.fn(() => detach) };
  const boards = new BoardPanels({
    extensionUri: Uri.parse("file:///ext") as unknown as vs.Uri,
    hub,
    router: { handle: vi.fn() },
    thinking: { subscribe: vi.fn(() => () => {}) },
    auth,
    jamUrl: () => JAM,
    windowStudio: undefined,
    perform: vi.fn(),
    executeCommand: vscode.commands.executeCommand,
    createPanel: vscode.window.createWebviewPanel,
  });
  const setSession = (s: Session | undefined) => {
    sess = s;
    for (const cb of [...authSubs]) cb(s);
  };
  return { boards, hub, detach, setSession };
}

type Panel = (typeof mock.panels)[number];
const states = (p: Panel) => (p.posted as ExtToPane[]).filter((m) => m.type === "state").map((m) => (m as { state: unknown }).state);

describe("BoardPanels", () => {
  it("open creates a panel with retainContextWhenHidden and the exact CSP", () => {
    const { boards } = setup();
    boards.open(BRD);
    expect(mock.panels).toHaveLength(1);
    const p = mock.panels[0]!;
    expect(p.viewType).toBe("jam.board");
    expect(p.title).toBe("Whiteboard");
    expect(p.column).toBe(vscode.ViewColumn.Active);
    expect(p.createOptions).toEqual({ enableScripts: true, enableCommandUris: false, enableFindWidget: false, retainContextWhenHidden: true, localResourceRoots: [expect.anything()] });
    expect(((p.createOptions as vs.WebviewPanelOptions & vs.WebviewOptions).localResourceRoots ?? []).map(String)).toEqual(["file:///ext/dist/webview"]);
    expect(String(p.iconPath)).toBe("file:///ext/media/jam.svg");
    const nonce = /nonce="([^"]+)"/.exec(p.webview.html)![1]!;
    const m = /Content-Security-Policy" content="([^"]*)"/.exec(p.webview.html)!;
    expect(m[1]!.replace(/&#39;/g, "'")).toBe(
      `default-src 'none'; script-src 'nonce-${nonce}' ${CDN}; style-src ${CDN} 'unsafe-inline'; font-src ${CDN}; img-src ${CDN} blob: data:; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'`,
    );
    expect(p.webview.html).toContain(`data-pane="board" data-board="${BRD}"`);
    expect(p.webview.html).not.toContain("tok");
  });

  it("the assets URL ends in a slash", () => {
    const p = fakeWebviewPanel("jam.board", "x", 1, {});
    const html = paneHtml(p.webview as unknown as vs.Webview, Uri.parse("file:///ext") as unknown as vs.Uri, "board", { board: BRD });
    expect(html).toContain(`data-assets="${CDN}/file/ext/dist/webview/excalidraw/"`);
  });

  it("open of an open board reveals it, no second panel", () => {
    const { boards } = setup();
    boards.open(BRD);
    boards.open(BRD);
    expect(mock.panels).toHaveLength(1);
    expect(mock.panels[0]!.reveal).toHaveBeenCalledTimes(1);
    boards.open(BRD2);
    expect(mock.panels).toHaveLength(2);
  });

  it("a bad id opens nothing", () => {
    const { boards } = setup();
    for (const id of ["", "../x", "chn_0abcdefghjkmnpqrstvwxyz012", `${BRD}x`, 5 as unknown as string]) boards.open(id);
    expect(mock.panels).toHaveLength(0);
  });

  it.each([
    ["a\u0007b", "ab"],
    ["x".repeat(80), "x".repeat(TITLE_MAX)],
    ["", "Whiteboard"],
    ["  \u0001 ", "Whiteboard"],
    ["  Plan  ", "Plan"],
    ["x\u202Egnp.json", "xgnp.json"],
    ["a\u2066b\u2069c\u202Ad", "abcd"],
    ["s\u200Bp\u200Cl\u200Di\u200Et\u200F\u2060\uFEFF", "split"],
    ["\u200B\uFEFF", "Whiteboard"],
  ])("title messages set tabTitle(title): %j", (title, want) => {
    expect(tabTitle(title)).toBe(want);
    const { boards } = setup();
    boards.open(BRD);
    const p = mock.panels[0]!;
    p.receive({ type: "title", title });
    expect(p.title).toBe(want);
  });

  it("restore rebuilds a panel from {board}, and restore with bad state disposes it", () => {
    const { boards } = setup();
    const p = fakeWebviewPanel("jam.board", "Old", 1, {});
    boards.restore(p as unknown as vs.WebviewPanel, { board: BRD });
    expect(p.webview.html).toContain(`data-board="${BRD}"`);
    expect(p.webview.options).toMatchObject({ enableScripts: true, enableCommandUris: false });
    boards.open(BRD);
    expect(mock.panels).toHaveLength(0);
    expect(p.reveal).toHaveBeenCalled();

    for (const s of [undefined, null, {}, { board: "../x" }, { board: 3 }]) {
      const q = fakeWebviewPanel("jam.board", "Old", 1, {});
      const d = vi.fn();
      q.onDidDispose(d);
      (q as { dispose: () => void }).dispose = () => d();
      boards.restore(q as unknown as vs.WebviewPanel, s);
      expect(d).toHaveBeenCalled();
      expect(q.webview.html).toBe("");
    }
  });

  it("a duplicate restore disposes the restored panel and reveals the open one", () => {
    const { boards } = setup();
    boards.open(BRD);
    const open = mock.panels[0]!;
    const q = fakeWebviewPanel("jam.board", "Old", 1, {});
    const d = vi.fn();
    (q as { dispose: () => void }).dispose = d;
    boards.restore(q as unknown as vs.WebviewPanel, { board: BRD });
    expect(d).toHaveBeenCalledTimes(1);
    expect(q.webview.html).toBe("");
    expect(open.reveal).toHaveBeenCalledTimes(1);
    boards.open(BRD);
    expect(mock.panels).toHaveLength(1);
    expect(open.reveal).toHaveBeenCalledTimes(2);
  });

  it("sign-out posts signedIn false to open boards", async () => {
    const { boards, setSession } = setup();
    boards.open(BRD);
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "board" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: null, pinned: false, elsewhere: false }));
    setSession(undefined);
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: false, target: null, pinned: false, elsewhere: false }));
    setSession(SIGNED_IN);
    await vi.waitFor(() => expect(states(p).at(-1)).toMatchObject({ signedIn: true }));
  });

  it("hiding a board doesn't reset it", () => {
    const { boards } = setup();
    boards.open(BRD);
    const p = mock.panels[0]!;
    p.setVisible(false);
    p.setVisible(true);
    expect(p.webview.postMessage).not.toHaveBeenCalledWith(expect.objectContaining({ type: "reset" }));
  });

  it("dispose then open makes a new panel, and the port is disposed", () => {
    const { boards, detach } = setup();
    boards.open(BRD);
    mock.panels[0]!.dispose();
    expect(detach).toHaveBeenCalledTimes(1);
    boards.open(BRD);
    expect(mock.panels).toHaveLength(2);
  });
});
