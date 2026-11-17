import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import * as vscode from "vscode";
import type { ExtToPane } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Session } from "../src/auth";
import { SessionPanels } from "../src/panes/sessiontab";
import { mock, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const GONE = "ses_1abcdefghjkmnpqrstvwxyz012";
const DM = "chn_0abcdefghjkmnpqrstvwxyz012";
const SIGNED_IN: Session = { url: JAM, token: "tok", user: "u", expiresAt: "" };

function setup() {
  mock.reset();
  const auth = { session: async (u: string) => (u === JAM ? SIGNED_IN : undefined), onChange: () => () => undefined };
  const reporter = { pane: vi.fn(), drop: vi.fn() };
  const onActive = vi.fn();
  const resolve = vi.fn(async (s: string) => (s === SES ? { label: "alpha" } : ("gone" as const)));
  const tabs = new SessionPanels({
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

describe("SessionPanels", () => {
  it("open creates one session-pane panel per session and reveals it the second time", () => {
    const { tabs } = setup();
    tabs.open(SES);
    tabs.open(SES);
    expect(mock.panels).toHaveLength(1);
    const p = mock.panels[0]!;
    expect(p.viewType).toBe("jam.sessionTab");
    expect(p.column).toBe(vscode.ViewColumn.Active);
    expect(p.createOptions).toMatchObject({ enableScripts: true, enableCommandUris: false, retainContextWhenHidden: false });
    expect(p.webview.html).toContain(`data-pane="session" data-session="${SES}"`);
    expect(p.revealed).toHaveLength(1);
    expect(tabs.has(SES)).toBe(true);
  });

  it("open refuses a bad id", () => {
    const { tabs } = setup();
    tabs.open("ses_x");
    tabs.open(DM);
    expect(mock.panels).toHaveLength(0);
  });

  it("state posts the session and the title follows its label", async () => {
    const { tabs } = setup();
    tabs.open(SES);
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "session" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: { session: SES, label: "alpha", channel: "" } }));
    expect(p.title).toBe("alpha");
  });

  it("a session the activity 404s on is gone", async () => {
    const { tabs } = setup();
    tabs.open(GONE);
    const p = mock.panels[0]!;
    p.receive({ type: "ready", pane: "session" });
    await vi.waitFor(() => expect(states(p).at(-1)).toEqual({ signedIn: true, target: { session: GONE, gone: true } }));
  });

  it("becoming active calls onActive with the session and reports the dm the pane names", async () => {
    const { tabs, onActive, reporter } = setup();
    tabs.open(SES);
    const p = mock.panels[0]!;
    p.setActive(true);
    await vi.waitFor(() => expect(onActive).toHaveBeenCalledWith(SES));
    p.receive({ type: "viewing", channel: DM });
    expect(reporter.pane).toHaveBeenLastCalledWith(expect.any(String), { visible: true, channel: DM });
    p.setActive(false);
    expect(reporter.pane).toHaveBeenLastCalledWith(expect.any(String), { visible: false, channel: DM });
    p.dispose();
    expect(reporter.drop).toHaveBeenCalled();
    expect(tabs.has(SES)).toBe(false);
  });

  it("a gone session never becomes the views' session", async () => {
    const { tabs, onActive, resolve } = setup();
    tabs.open(GONE);
    const p = mock.panels[0]!;
    p.setActive(true);
    await vi.waitFor(() => expect(resolve).toHaveBeenCalledWith(GONE));
    expect(onActive).not.toHaveBeenCalled();
  });

  it("restore of a session already open disposes the revived tab and reveals the open one; junk state disposes", () => {
    const { tabs } = setup();
    tabs.open(SES);
    const revived = vscode.window.createWebviewPanel("jam.sessionTab", "x", 1, {}) as unknown as Panel;
    const disposed = vi.fn();
    revived.onDidDispose(disposed);
    tabs.restore(revived as unknown as vs.WebviewPanel, { session: SES });
    expect(disposed).toHaveBeenCalled();
    expect(mock.panels[0]!.revealed.length).toBeGreaterThan(0);
    const junk = vscode.window.createWebviewPanel("jam.sessionTab", "x", 1, {}) as unknown as Panel;
    const gone = vi.fn();
    junk.onDidDispose(gone);
    tabs.restore(junk as unknown as vs.WebviewPanel, { session: "nope" });
    expect(gone).toHaveBeenCalled();
  });

  it("a restored tab already active calls onActive once its session resolves", async () => {
    const { tabs, onActive } = setup();
    const revived = vscode.window.createWebviewPanel("jam.sessionTab", "x", 1, {}) as unknown as Panel;
    revived.active = true;
    tabs.restore(revived as unknown as vs.WebviewPanel, { session: SES });
    expect(revived.webview.html).toContain(`data-session="${SES}"`);
    revived.receive({ type: "ready", pane: "session" });
    await vi.waitFor(() => expect(onActive).toHaveBeenCalledWith(SES));
  });
});
