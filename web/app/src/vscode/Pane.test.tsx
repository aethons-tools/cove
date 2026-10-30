import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Bootstrap, Conversation, MessageRow } from "../api/types";
import type { BridgeResponse, PaneState } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setUploader } from "../composer/upload";
import { setBlobResolver } from "../blobs";
import { setLiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { boot } from "../test/render";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { interceptLinks } from "./links";
import { Pane } from "./Pane";

const BRD = "brd_0abcdefghjkmnpqrstvwxyz01";
const NEW_BRD = "brd_1abcdefghjkmnpqrstvwxyz01";
const noContent: BridgeResponse = { kind: "http", status: 204, contentType: "" };
const at = (s: string) => ({ session: "ses_1", label: "spider", channel: s });
const signedIn = (target: PaneState["target"]): PaneState => ({ signedIn: true, target });

const msg = (seq: number, over: Partial<MessageRow> = {}): MessageRow => ({
  from: "spider", body: `<p>m${seq}</p>`, raw: `m${seq}`, plain: false, at: "09:00", time: new Date().toISOString(),
  mine: false, seq, system: false, sysText: "", card: "", ...over,
});
const conv = (channel: string, over: Partial<Conversation> = {}): Conversation => ({
  channelId: channel, label: `conv ${channel}`, kind: "session", project: "test", phase: "live", needsYou: false, sendTo: channel,
  messages: [msg(1, { body: `<p>hello from ${channel}</p>` })], lastSeq: 1, earlier: false,
  sessionIds: ["ses_1"], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: "ses_1", newBoard: false, ...over,
});
const routesFor = (channel: string, c: Conversation = conv(channel)): Record<string, Route> => ({
  [`GET /app/user/api/conversations/${channel}`]: json(c),
  [`GET /app/user/api/conversations/${channel}/presence`]: json({ sessions: [] }),
  [`POST /app/user/api/conversations/${channel}/read`]: noContent,
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
  vi.restoreAllMocks();
});

// mount wires a pane as main.tsx does, against a fake host.
function mount(routes: Record<string, Route> = {}, b: Partial<Bootstrap> = {}) {
  const h = fakeHost({ "GET /app/user/api/bootstrap": json({ ...boot, ...b }), ...routesFor("chn_a"), ...routes });
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  let channel = "";
  stops.push(bridge.onState((s) => { channel = s.target && "channel" in s.target ? s.target.channel : ""; }));
  stops.push(interceptLinks(document, () => channel, (intent) => bridge.post({ type: "open", intent })));
  const r = render(<Pane bridge={bridge} name="conversation" />);
  return { h, ...r };
}

describe("the Conversation pane", () => {
  it("renders the focused session's conversation through the bridge", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toBeInTheDocument();
    expect(screen.queryByText("Copy SSH")).toBeNull();
    const reqs = h.requests.map((r) => `${r.method} ${r.path}`);
    expect(reqs.slice(0, 2)).toEqual(["GET /app/user/api/bootstrap", "GET /app/user/api/conversations/chn_a"]);
    for (const r of h.requests) {
      expect(r.path.startsWith("/app/user/api/")).toBe(true);
      expect(r.path).not.toMatch(/^\/app\/user\/api\/sessions\//);
    }
  });

  it("waits for the first state", async () => {
    const { h } = mount();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByText(/Jam|session/)).toBeNull();
    expect(h.requests).toEqual([]);
  });

  it("no target says this isn't a studio window, with no action", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn(null)); });
    expect(await screen.findByText("This window isn't a Jam studio window.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
    expect(h.requests).toEqual([]);
  });

  it("signed out shows Sign in, which posts signIn", async () => {
    const { h } = mount();
    act(() => { h.state({ signedIn: false, target: null }); });
    expect(await screen.findByText("Sign in to Jam to see this conversation.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
    expect(h.requests).toEqual([]);
  });

  it("gone shows the ended copy", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn({ session: "ses_1", gone: true })); });
    expect(await screen.findByText("That session has ended, or you can't see it.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("a lookup error shows it with Try again, which posts ready", async () => {
    const { h } = mount();
    act(() => { h.state({ ...signedIn(null), error: "Jam is unreachable" }); });
    expect(await screen.findByText("Couldn't load the focused session: Jam is unreachable")).toBeInTheDocument();
    expect(screen.queryByText(/This window isn't a Jam studio window/)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(h.posted).toContainEqual({ type: "ready", pane: "conversation" });
    expect(h.requests).toEqual([]);
  });

  it("a new target switches the conversation", async () => {
    const { h } = mount(routesFor("chn_b"));
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    act(() => { h.state(signedIn(at("chn_b"))); });
    expect(await screen.findByText("hello from chn_b")).toBeInTheDocument();
    expect(screen.queryByText("hello from chn_a")).toBeNull();
    expect(h.requests.some((r) => r.path === "/app/user/api/conversations/chn_b")).toBe(true);
  });

  it("Open on a board card posts a board intent and the conversation stays mounted", async () => {
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:1");
    const c = conv("chn_a", { messages: [msg(1), msg(2, { card: "board", body: "", board: { id: BRD, title: "Plan" } })], lastSeq: 2 });
    const { h } = mount({
      ...routesFor("chn_a", c),
      [`GET /app/user/boards/${BRD}/thumb.png`]: { kind: "http", status: 200, contentType: "image/png", bytes: new ArrayBuffer(1) },
    });
    act(() => { h.state(signedIn(at("chn_a"))); });
    const box = await screen.findByRole("textbox");
    await userEvent.type(box, "half a thought");
    await userEvent.click(screen.getByRole("link", { name: "Open" }));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "board", id: BRD } });
    expect(screen.getByRole("textbox")).toBe(box);
    expect(box).toHaveValue("half a thought");
  });

  it("New whiteboard navigation posts a board intent", async () => {
    const { h } = mount({
      ...routesFor("chn_a", conv("chn_a", { newBoard: true })),
      "POST /app/user/api/boards": json({ board: NEW_BRD }),
    }, { boards: true });
    act(() => { h.state(signedIn(at("chn_a"))); });
    await userEvent.click(await screen.findByRole("button", { name: "New whiteboard" }));
    await userEvent.type(await screen.findByLabelText("Title"), "Plan");
    await userEvent.click(screen.getByRole("button", { name: "Start" }));
    await waitFor(() => expect(h.posted).toContainEqual({ type: "open", intent: { kind: "board", id: NEW_BRD } }));
    expect(h.requests).toContainEqual({ method: "POST", path: "/app/user/api/boards", json: { channel: "chn_a", title: "Plan" } });
    expect(screen.getByText("hello from chn_a")).toBeInTheDocument();
  });

  it("a live changed event refetches the conversation", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    const count = () => h.requests.filter((r) => r.path === "/app/user/api/conversations/chn_a").length;
    const before = count();
    act(() => { h.live("changed"); });
    await waitFor(() => expect(count()).toBe(before + 1));
  });

  it("an image attachment loads as a blob URL", async () => {
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:1");
    const c = conv("chn_a", { messages: [msg(1, { files: [{ id: "blb_1", name: "shot.png", size: "1 KB", image: true }] })] });
    const { h } = mount({
      ...routesFor("chn_a", c),
      "GET /app/user/blobs/blb_1": { kind: "http", status: 200, contentType: "image/png", bytes: new ArrayBuffer(1) },
    });
    act(() => { h.state(signedIn(at("chn_a"))); });
    await waitFor(() => expect(screen.getByRole("img", { name: "shot.png" })).toHaveAttribute("src", "blob:1"));
    expect(h.requests).toContainEqual({ method: "GET", path: "/app/user/blobs/blb_1", accept: "bytes" });
  });

  it("a 401 shows Sign in again, which posts signIn", async () => {
    const { h } = mount({ "GET /app/user/api/bootstrap": { kind: "http", status: 401, contentType: "text/html", text: "<p>login</p>" } });
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText(/Your sign-in has expired/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
  });

  it("viewing is posted only when pushFocus is on", async () => {
    const off = mount();
    act(() => { off.h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 20));
    expect(off.h.posted.filter((m) => m.type === "viewing")).toEqual([]);
    off.unmount();

    const on = mount({}, { pushFocus: true });
    act(() => { on.h.state(signedIn(at("chn_a"))); });
    await waitFor(() => expect(on.h.posted).toContainEqual({ type: "viewing", channel: "chn_a" }));
  });

  it("signing in again starts over with a fresh bootstrap", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    act(() => { h.state({ ...signedIn(at("chn_a")), signedIn: false }); });
    expect(await screen.findByText("Sign in to Jam to see this conversation.")).toBeInTheDocument();
    act(() => { h.state(signedIn(at("chn_a"))); });
    expect(await screen.findByText("hello from chn_a")).toBeInTheDocument();
    expect(h.requests.filter((r) => r.path === "/app/user/api/bootstrap")).toHaveLength(2);
  });
});
