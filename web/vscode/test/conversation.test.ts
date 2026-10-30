import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import * as vscode from "vscode";
import type { ExtToPane, SessionTarget } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Session } from "../src/auth";
import { ConversationPanels } from "../src/panes/conversation";
import { mock, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const CHN = "chn_0abcdefghjkmnpqrstvwxyz012";
const CHAT = "chn_1abcdefghjkmnpqrstvwxyz012";
const SIGNED_IN: Session = { url: JAM, token: "tok", user: "u", expiresAt: "" };
const known: Record<string, SessionTarget> = {
  [CHN]: { session: SES, label: "alpha", channel: CHN },
  [CHAT]: { session: "", label: "Ann, Bob", channel: CHAT },
};

function setup() {
  mock.reset();
  const auth = { session: async (u: string) => (u === JAM ? SIGNED_IN : undefined), onChange: () => () => undefined };
  const reporter = { pane: vi.fn(), drop: vi.fn() };
  const onActive = vi.fn();
  const resolve = vi.fn(async (c: string) => known[c] ?? ("gone" as const));
  const tabs = new ConversationPanels({
    extensionUri: Uri.parse("file:///ext") as unknown as vs.Uri,
    hub: { attach: vi.fn(() => () => undefined) },
    router: { handle: vi.fn() },
    thinking: { subscribe: vi.fn(() => () => {}) },
    auth,
    jamUrl: () => JAM,
    windowStudio: undefined,
    perform: vi.fn(),
    executeCommand: vscode.commands.executeCommand,
    createPanel: vscode.window.createWebviewPanel,
    resolve,
    reporter,
    onActive,
  });
  return { tabs, reporter, onActive, resolve };
}

type Panel = (typeof mock.panels)[number];
const states = (p: Panel) => (p.posted as ExtToPane[]).filter((m) => m.type === "state").map((m) => (m as { state: unknown }).state);

describe("ConversationPanels", () => {
  it("open creates one conversation-pane panel per channel and reveals it the second time", () => {
    const { tabs } = setup();
    tabs.open(CHN);
    tabs.open(CHN);
    expect(mock.panels).toHaveLength(1);
    const p = mock.panels[0]!;
    expect(p.viewType).toBe("jam.conversationTab");
    expect(p.column).toBe(vscode.ViewColumn.Active);
    expect(p.createOptions).toMatchObject({ enableScripts: true, enableCommandUris: false, retainContextWhenHidden: false });
    expect(p.webview.html).toContain(`data-pane="conversation" data-channel="${CHN}"`);
    expect(String(p.iconPath)).toBe("file:///ext/media/jam-color.svg");
    expect(p.revealed).toHaveLength(1);
    expect(tabs.has(CHN)).toBe(true);
  });

  it("open refuses a bad id", () => {
    const { tabs } = setup();
    tabs.open("chn_x");
    tabs.open("../x");
    expect(mock.panels).toHaveLength(0);
  });

  it("a History (legacy:) channel opens a tab", () => {
    const { tabs } = setup();
    tabs.open("legacy:dm:ann");
    expect(mock.panels).toHaveLength(1);
  });

  it("state posts the resolved target and the title follows its label", async () => {
    const { tabs } = setup();
    tabs.open(CHN);
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: known[CHN] }));
    expect(p.title).toBe("alpha");
  });

  it("a chat target has session \"\"", async () => {
    const { tabs } = setup();
    tabs.open(CHAT);
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: known[CHAT] }));
  });

  it("a gone conversation posts no target", async () => {
    const { tabs } = setup();
    tabs.open("chn_2abcdefghjkmnpqrstvwxyz012");
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: null }));
  });

  it("becoming active calls onActive with the target and reports the channel", async () => {
    const { tabs, onActive, reporter } = setup();
    tabs.open(CHN);
    const p = mock.panels[0]!;
    p.setActive(true);
    await vi.waitFor(() => expect(onActive).toHaveBeenCalledWith(known[CHN]));
    p.receive({ type: "viewing", channel: CHN });
    expect(reporter.pane).toHaveBeenLastCalledWith(expect.any(String), { visible: true, channel: CHN });
    p.dispose();
    expect(reporter.drop).toHaveBeenCalled();
    expect(tabs.has(CHN)).toBe(false);
  });

  it("restore of a channel already open disposes the revived tab and reveals the open one; junk state disposes", () => {
    const { tabs } = setup();
    tabs.open(CHN);
    const revived = vscode.window.createWebviewPanel("jam.conversationTab", "x", 1, {}) as unknown as Panel;
    const disposed = vi.fn();
    revived.onDidDispose(disposed);
    tabs.restore(revived as unknown as vs.WebviewPanel, { channel: CHN });
    expect(disposed).toHaveBeenCalled();
    expect(mock.panels[0]!.revealed.length).toBeGreaterThan(0);
    const junk = vscode.window.createWebviewPanel("jam.conversationTab", "x", 1, {}) as unknown as Panel;
    const gone = vi.fn();
    junk.onDidDispose(gone);
    tabs.restore(junk as unknown as vs.WebviewPanel, { channel: "nope" });
    expect(gone).toHaveBeenCalled();
  });

  it("reveal posts scrollTo to an open tab, holds it for the next ready, and says whether a tab exists", async () => {
    const { tabs } = setup();
    expect(tabs.reveal(CHN, "ask-1")).toBe(false);
    tabs.open(CHN);
    const p = mock.panels[0]!;
    expect(tabs.reveal(CHN, "ask-1")).toBe(true);
    expect(p.posted).toContainEqual({ type: "scrollTo", anchor: "ask-1" });
    p.posted.length = 0;
    p.receive({ type: "ready", pane: "conversation" });
    await vi.waitFor(() => expect(p.posted).toContainEqual({ type: "scrollTo", anchor: "ask-1" }));
  });
});
