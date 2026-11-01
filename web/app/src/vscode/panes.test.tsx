import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Bootstrap, Conversation, MessageRow, SessionHead, SessionPane } from "../api/types";
import type { BridgeResponse, PaneState, SessionPaneName } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setUploader } from "../composer/upload";
import { setBlobResolver } from "../blobs";
import { setLiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { setFeedOpener } from "../session/thinking/useThinkingFeed";
import { tev } from "../session/thinking/testing";
import { boot } from "../test/render";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { Pane, wirePane } from "./Pane";
import { card } from "../test/card";

// Valid session ids (SES_RE): the bridge's feed opener refuses anything else.
const SES_A = "ses_0abcdefghjkmnpqrstvwxyz012";
const SES_B = "ses_1abcdefghjkmnpqrstvwxyz012";
const BRD = "brd_0abcdefghjkmnpqrstvwxyz01";
const noContent: BridgeResponse = { kind: "http", status: 204, contentType: "" };
const at = (session: string, channel = "chn_a") => ({ session, label: "spider", channel });
const signedIn = (target: PaneState["target"]): PaneState => ({ signedIn: true, target });

const head = (id: string, over: Partial<SessionHead> = {}): SessionHead => ({
  id, label: "spider", kind: "standing", ticket: "", role: "r", project: "p", owner: "",
  state: { label: "x", text: "is running", tool: "", class: "idle" }, since: "", openAsks: 0, cost: "", turnCost: "", tokens: "",
  context: "", contextTip: "", uptime: "", mission: "", ssh: "", canRelease: false, canRestart: false, canEnd: false, canUpgrade: false,
  ops: true, canInterrupt: true, canCompact: false, canClear: false, restartLabel: "Restart", upgradeState: "", channel: "chn_a", card: card({ session: id, name: "card-spider" }), ...over,
});
const pane = (id: string, tab: string, over: Partial<SessionPane> = {}): SessionPane => ({
  id, tab, previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], streams: [], eventsOff: false,
  ...over,
});
const sessionRoutes = (id: string, tab: string, p: Partial<SessionPane> = {}, h: Partial<SessionHead> = {}): Record<string, Route> => ({
  [`GET /app/user/api/sessions/${id}`]: json(head(id, h)),
  [`GET /app/user/api/sessions/${id}/pane/${tab}`]: json(pane(id, tab, p)),
});

const msg = (seq: number, over: Partial<MessageRow> = {}): MessageRow => ({
  from: "spider", body: `<p>m${seq}</p>`, raw: `m${seq}`, plain: false, at: "09:00", time: new Date().toISOString(),
  mine: false, seq, system: false, sysText: "", card: "", ...over,
});
const conv = (channel: string, over: Partial<Conversation> = {}): Conversation => ({
  channelId: channel, label: `conv ${channel}`, kind: "session", project: "test", phase: "live", needsYou: false, sendTo: channel,
  messages: [msg(1, { body: `<p>hello from ${channel}</p>` })], lastSeq: 1, earlier: false,
  sessionIds: [SES_A], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: SES_A, newBoard: false, ...over,
});

// jsdom lays nothing out: give elements a viewport, so the Thinking list's
// virtualizer renders rows.
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 800 });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 600 });
});
afterAll(() => {
  delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
});

let stops: (() => void)[] = [];
afterEach(() => {
  stops.forEach((s) => s());
  stops = [];
  setTransport();
  setUploader();
  setBlobResolver();
  setLiveSource();
  setFocusSink();
  setFeedOpener();
  localStorage.clear();
  vi.restoreAllMocks();
});

// mount wires a pane as main.tsx does, against a fake host.
function mount(name: SessionPaneName, routes: Record<string, Route> = {}, b: Partial<Bootstrap> = {}) {
  const h = fakeHost({ "GET /app/user/api/bootstrap": json({ ...boot, ...b }), ...routes });
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  stops.push(wirePane(bridge, document, name));
  const r = render(<Pane bridge={bridge} name={name} />);
  return { h, ...r };
}

describe("the session panes", () => {
  it("thinking renders the session's feed over the bridge", async () => {
    const { h } = mount("thinking", sessionRoutes(SES_A, "thinking"));
    let sub = 0;
    h.onThinkingOpen((m) => { sub = m.sub; });
    act(() => { h.state(signedIn(at(SES_A))); });
    await waitFor(() => expect(h.posted).toContainEqual({ type: "thinkingOpen", sub: expect.any(Number), session: SES_A, stream: "" }));
    act(() => {
      h.thinking(sub, "open");
      h.thinking(sub, "stream", "s1");
      h.thinking(sub, "ev", JSON.stringify(tev(3)));
    });
    expect(await screen.findByText("said 3")).toBeInTheDocument();
    const paths = h.requests.map((r) => `${r.method} ${r.path}`);
    expect(paths).toContain(`GET /app/user/api/sessions/${SES_A}/pane/thinking`);
    for (const r of h.requests) expect(r.path).not.toContain("/thinking/events");
  });

  it("previews Open posts a preview intent for the pane's session", async () => {
    const { h } = mount("previews", sessionRoutes(SES_A, "previews", { previews: [{ name: "web", url: "https://p.test/", port: 8080, frame: true }] }));
    act(() => { h.state(signedIn(at(SES_A))); });
    await userEvent.click(await screen.findByRole("button", { name: "Open" }));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "preview", session: SES_A, port: 8080, url: "https://p.test/", inBrowser: false } });
    await userEvent.click(screen.getByRole("button", { name: "Browser" }));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "preview", session: SES_A, port: 8080, url: "https://p.test/", inBrowser: true } });
    expect(screen.getByRole("button", { name: "Open" })).toHaveAttribute("title", "Open in VS Code's Simple Browser");
    expect(screen.queryByText("View here")).toBeNull();
  });

  it("status shows the card, the header without tabs and the status rows; Interrupt posts sessions/ses_a/interrupt", async () => {
    const { h } = mount("status", {
      ...sessionRoutes(SES_A, "status", { status: [{ name: "Model", value: "opus" }] }, { mission: "ship slice 4" }),
      [`POST /app/user/api/sessions/${SES_A}/interrupt`]: noContent,
    });
    act(() => { h.state(signedIn(at(SES_A))); });
    expect(await screen.findByText("opus")).toBeInTheDocument();
    expect(screen.getByText("Model")).toBeInTheDocument();
    expect(await screen.findByText("ship slice 4")).toBeInTheDocument();
    expect(screen.getByText("spider")).toBeInTheDocument();
    // The status card heads the pane.
    expect(screen.getByText("card-spider")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "recent states, now working" })).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Interrupt" }));
    await waitFor(() => expect(h.requests).toContainEqual({ method: "POST", path: `/app/user/api/sessions/${SES_A}/interrupt`, json: {} }));
  });

  it("artifacts ask click posts reveal ask-<id>; a board thumb click posts a board intent", async () => {
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:1");
    const { h } = mount("artifacts", {
      ...sessionRoutes(SES_A, "artifacts", {
        asks: [{ id: "ask_1", question: "Ship it?", state: "open" }],
        boards: [{ id: BRD, title: "Plan", channel: "chn_a" }],
      }),
      [`GET /app/user/boards/${BRD}/thumb.png`]: { kind: "http", status: 200, contentType: "image/png", bytes: new ArrayBuffer(1) },
    });
    act(() => { h.state(signedIn(at(SES_A))); });
    await userEvent.click(await screen.findByText("Ship it?"));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "reveal", anchor: "ask-ask_1" } });
    await userEvent.click(screen.getByRole("link", { name: /Plan/ }));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "board", id: BRD } });
  });

  const nouns: [SessionPaneName, string][] = [
    ["conversation", "Sign in to Jam to see this conversation."],
    ["thinking", "Sign in to Jam to see this session's thinking."],
    ["previews", "Sign in to Jam to see this session's previews."],
    ["status", "Sign in to Jam to see this session's status."],
    ["artifacts", "Sign in to Jam to see this session's artifacts."],
  ];
  it.each(nouns)("every session pane shows its own signed-out and no-session copy (%s)", async (name, out) => {
    const { h } = mount(name);
    act(() => { h.state({ signedIn: false, target: null }); });
    expect(await screen.findByText(out)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
    act(() => { h.state(signedIn(null)); });
    expect(await screen.findByText(name === "conversation" ? "That conversation is gone, or you can't see it." : `Open a session's conversation to see its ${name}.`)).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
    act(() => { h.state({ ...signedIn(null), error: "Jam is unreachable" }); });
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(h.posted).toContainEqual({ type: "ready", pane: name });
    expect(h.requests).toEqual([]);
  });

  it.each(["thinking", "previews", "status", "artifacts"] as const)("a session head 404 shows the gone copy (%s)", async (name) => {
    const routes = sessionRoutes(SES_A, name);
    delete routes[`GET /app/user/api/sessions/${SES_A}`];
    const { h } = mount(name, routes);
    act(() => { h.state(signedIn(at(SES_A))); });
    expect(await screen.findByText("That session has ended, or you can't see it.")).toBeInTheDocument();
    expect(screen.queryByText(/no route/)).toBeNull();
    expect(screen.queryByText(/no longer available/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Focus a session…" })).toBeNull();
    // No retry loop: once gone, nothing asks again.
    const asked = h.requests.length;
    await new Promise((r) => setTimeout(r, 300));
    expect(h.requests).toHaveLength(asked);
    expect(h.requests.filter((r) => r.path === `/app/user/api/sessions/${SES_A}`).length).toBeLessThanOrEqual(2);
  });

  it("a gone pane stops polling its session head", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const routes = sessionRoutes(SES_A, "status");
      delete routes[`GET /app/user/api/sessions/${SES_A}`];
      const { h } = mount("status", routes);
      act(() => { h.state(signedIn(at(SES_A))); });
      expect(await screen.findByText("That session has ended, or you can't see it.")).toBeInTheDocument();
      const heads = () => h.requests.filter((r) => r.path === `/app/user/api/sessions/${SES_A}`).length;
      const asked = heads();
      await act(() => vi.advanceTimersByTimeAsync(95_000));
      expect(heads()).toBe(asked);
      expect(screen.getByText("That session has ended, or you can't see it.")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("scrollTo retries until the ask renders", async () => {
    const scrolled = vi.fn();
    Element.prototype.scrollIntoView = scrolled;
    const c = conv("chn_a", { messages: [msg(1, { body: `<p id="ask-a1">the ask</p>` })] });
    const { h } = mount("conversation", {
      "GET /app/user/api/conversations/chn_a": () => new Promise((r) => setTimeout(() => r(json(c)), 500)),
      "GET /app/user/api/conversations/chn_a/presence": json({ sessions: [] }),
      "POST /app/user/api/conversations/chn_a/read": noContent,
    });
    act(() => { h.state(signedIn(at(SES_A))); });
    act(() => { h.scrollTo("ask-a1"); });
    expect(scrolled).not.toHaveBeenCalled();
    expect(await screen.findByText("the ask")).toBeInTheDocument();
    await waitFor(() => expect(scrolled).toHaveBeenCalledWith({ behavior: "smooth", block: "center" }));
    expect(scrolled.mock.contexts[0]).toBe(document.getElementById("ask-a1"));
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });

  it("a conversation link to an ask in another pane reveals it", async () => {
    const { h } = mount("artifacts", sessionRoutes(SES_A, "artifacts", { links: [{ label: "the ask", url: "/app/user/c/chn_a#ask-a1" }] }));
    act(() => { h.state(signedIn(at(SES_A))); });
    await userEvent.click(await screen.findByText("the ask"));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "reveal", anchor: "ask-a1" } });
  });

  it("a new target switches the pane's session", async () => {
    const { h } = mount("status", {
      ...sessionRoutes(SES_A, "status", {}, { label: "spider" }),
      ...sessionRoutes(SES_B, "status", {}, { label: "moth" }),
    });
    act(() => { h.state(signedIn(at(SES_A))); });
    expect(await screen.findByText("spider")).toBeInTheDocument();
    act(() => { h.state(signedIn({ session: SES_B, label: "moth", channel: "chn_b" })); });
    expect(await screen.findByText("moth")).toBeInTheDocument();
    expect(screen.queryByText("spider")).toBeNull();
  });
});
