import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ActivityRow, SessionActivity, SessionHead } from "../api/types";
import type { PaneState, PaneToExt } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setUploader } from "../composer/upload";
import { setBlobResolver } from "../blobs";
import { setLiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { setFeedOpener } from "../session/thinking/useThinkingFeed";
import { boot } from "../test/render";
import { card } from "../test/card";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { Pane, wirePane } from "./Pane";

const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const DM = "chn_0abcdefghjkmnpqrstvwxyz012";
const ROOM = "chn_1abcdefghjkmnpqrstvwxyz012";

const head: SessionHead = {
  id: SES, label: "pair", kind: "personal", ticket: "", role: "pair", project: "test", owner: "alice",
  state: { label: "pair", text: "is idle", tool: "", class: "idle" }, since: "4m", openAsks: 0,
  cost: "", turnCost: "", tokens: "", context: "", contextTip: "", uptime: "1h", mission: "", ssh: "",
  canRelease: false, canRestart: false, canEnd: false, canUpgrade: false, ops: true,
  canInterrupt: true, canCompact: false, canClear: false, restartLabel: "Restart", upgradeState: "", channel: ROOM, card: card(),
};
const row = (over: Partial<ActivityRow>): ActivityRow => ({
  from: "alice", body: "<p>hello pair</p>", raw: "hello pair", plain: false, at: "09:00", time: "2026-11-14T09:00:00Z", mine: true, seq: 1,
  system: false, sysText: "", card: "", id: "m1", conversation: { id: ROOM, kind: "room", label: "eng" }, out: false, ...over,
});
const activity = (over: Partial<SessionActivity> = {}): SessionActivity => ({
  messages: [row({})], pageFirst: "m1", pageLast: "m1", earlier: false, later: false, turns: [], limited: [],
  sendTo: `session:${SES}`, dmChannel: DM, ended: false, label: "pair", ...over,
});
const target: PaneState = { signedIn: true, target: { session: SES, label: "pair", channel: "" } };
const notFound = json({ error: "no such session", code: "not_found" }, 404);

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

// mount wires a session tab as main.tsx does, against a fake host.
function mount(routes: Record<string, Route> = {}) {
  const h = fakeHost({
    "GET /app/user/api/bootstrap": json({ ...boot, pushFocus: true }),
    [`GET /app/user/api/sessions/${SES}`]: json(head),
    [`GET /app/user/api/sessions/${SES}/activity`]: json(activity()),
    "POST /app/user/api/messages": json({ channel: DM, seq: 9 }, 201),
    ...routes,
  });
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  stops.push(wirePane(bridge, document, "session"));
  render(<Pane bridge={bridge} name="session" />);
  const viewing = () => h.posted.filter((m): m is Extract<PaneToExt, { type: "viewing" }> => m.type === "viewing").map((m) => m.channel);
  return { h, viewing };
}

describe("the session tab", () => {
  it("shows the header and the activity, without the pane or its tabs", async () => {
    const { h } = mount();
    act(() => { h.state(target); });
    expect(await screen.findByText("hello pair")).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Interrupt" })).toBeInTheDocument();
    expect(screen.queryByRole("tablist", { name: "Session pane" })).toBeNull();
    expect(screen.queryByRole("complementary", { name: "Session pane" })).toBeNull();
    expect(h.requests.map((r) => r.path)).not.toContainEqual(expect.stringContaining("/pane/"));
  });

  it("the composer posts to sendTo", async () => {
    const { h } = mount();
    act(() => { h.state(target); });
    await userEvent.type(await screen.findByRole("textbox"), "hi there");
    await userEvent.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => expect(h.requests).toContainEqual(expect.objectContaining({ method: "POST", path: "/app/user/api/messages", json: expect.objectContaining({ to: `session:${SES}` }) })));
  });

  it("reports its dm as the conversation it shows", async () => {
    const { h, viewing } = mount();
    act(() => { h.state(target); });
    await waitFor(() => expect(viewing().at(-1)).toBe(DM));
  });

  it("reports nothing before the dm exists", async () => {
    const { h, viewing } = mount({ [`GET /app/user/api/sessions/${SES}/activity`]: json(activity({ dmChannel: "" })) });
    act(() => { h.state(target); });
    expect(await screen.findByText("hello pair")).toBeInTheDocument();
    expect(viewing().filter(Boolean)).toEqual([]);
  });

  it("once the session ends: no header, no composer, the activity stays, and nothing is reported", async () => {
    const { h, viewing } = mount({
      [`GET /app/user/api/sessions/${SES}`]: notFound,
      [`GET /app/user/api/sessions/${SES}/activity`]: json(activity({ sendTo: "", ended: true })),
    });
    act(() => { h.state(target); });
    expect(await screen.findByText("hello pair")).toBeInTheDocument();
    expect(await screen.findByText("This session has ended: its activity stays.")).toBeInTheDocument();
    await waitFor(() => expect(h.requests.map((r) => r.path)).toContain(`/app/user/api/sessions/${SES}`));
    expect(screen.queryByRole("region", { name: "Session" })).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(viewing().filter(Boolean)).toEqual([]);
  });

  it("a session the extension found gone says so", async () => {
    const { h } = mount();
    act(() => { h.state({ signedIn: true, target: { session: SES, gone: true } }); });
    expect(await screen.findByText("That session has ended, or you can't see it.")).toBeInTheDocument();
    expect(h.requests).toEqual([]);
  });

  it("signed out offers Sign in", async () => {
    const { h } = mount();
    act(() => { h.state({ signedIn: false, target: null }); });
    expect(await screen.findByText("Sign in to Jam to see this session.")).toBeInTheDocument();
  });

  it("a link to a conversation opens the web app's, never navigating the tab", async () => {
    const { h } = mount();
    act(() => { h.state(target); });
    const link = await screen.findByRole("link", { name: "eng" });
    await userEvent.click(link);
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "conversation", channel: ROOM } });
    expect(await screen.findByText("hello pair")).toBeInTheDocument();
  });
});
