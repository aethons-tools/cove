import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PaneState, PaneToExt } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setLiveSource } from "../live/useLiveEvents";
import { boot } from "../test/render";
import { card } from "../test/card";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { Pane } from "./Pane";
import { DOUBLE_CLICK_MS } from "./RailPane";

const SES_A = "ses_0abcdefghjkmnpqrstvwxyz012";
const SES_W = "ses_3abcdefghjkmnpqrstvwxyz012";
const SES_C = "ses_4abcdefghjkmnpqrstvwxyz012";
const CHN_A = "chn_0abcdefghjkmnpqrstvwxyz012";
const CHN_B = "chn_1abcdefghjkmnpqrstvwxyz012";
const CHN_NEW = "chn_2abcdefghjkmnpqrstvwxyz012";
const CHN_LOG = "chn_3abcdefghjkmnpqrstvwxyz012";
const CHN_C = "chn_4abcdefghjkmnpqrstvwxyz012";
const SES_NEW = "ses_2abcdefghjkmnpqrstvwxyz012";
const LEGACY = "legacy:old-thread";
const row = (id: string, label: string, over = {}) => ({
  id, label, project: "p", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const rail = {
  me: [
    row(CHN_A, "Ada", { kind: "dm", session: SES_A, ssh: true, needsYou: true, unread: 2, card: card({ session: SES_A, name: "Ada", state: "idle" }) }),
    row(CHN_B, "#design"),
  ],
  work: [{ key: "ACME-42", title: "Fix the flaky login test", log: CHN_LOG, worker: { id: SES_W, label: "acme-42", state: "waiting" }, unread: 1 }],
  agents: [
    { session: SES_A, ssh: true, card: card({ session: SES_A, name: "Ada", mission: "Keep the books" }) },
    { session: SES_C, ssh: false, card: card({ session: SES_C, name: "carl", mission: "Review PRs" }) },
  ],
  history: [row(LEGACY, "old thread", { kind: "dm" })],
  unreadTotal: 2,
};
// meRow is the Me section's row named name.
const meRow = async (name: RegExp) => within(await screen.findByRole("list", { name: "Me" })).getByRole("button", { name });
const head = (id: string, channel: string) => json({ id, label: id, channel });
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

afterEach(() => {
  setTransport();
  setLiveSource();
  vi.restoreAllMocks();
});

const recipients = { options: [{ to: "user:usr_ann", label: "Ann", kind: "person", project: "test" }] };
const roles = { roles: [{ project: "test", role: "pair", full: "" }] };

function mount(state: PaneState = { signedIn: true, target: null, windows: [] }, routes: Record<string, Route> = {}, b: Partial<typeof boot> = {}) {
  const h = fakeHost({
    "GET /app/user/api/bootstrap": json({ ...boot, newSession: true, ...b }),
    "GET /app/user/api/recipients": json(recipients),
    "GET /app/user/api/session-roles": json(roles),
    "POST /app/user/api/messages": json({ channel: CHN_NEW, seq: 1 }, 201),
    "POST /app/user/api/sessions": json({ id: SES_NEW, name: "pair-01", channel: CHN_NEW }, 201),
    [`GET /app/user/api/sessions/${SES_A}`]: head(SES_A, CHN_A),
    [`GET /app/user/api/sessions/${SES_C}`]: head(SES_C, CHN_C),
    [`GET /app/user/api/sessions/${SES_W}`]: head(SES_W, CHN_LOG),
    "PUT /app/user/api/rail/order": { kind: "http", status: 204, contentType: "" },
    "GET /app/user/api/rail": json(rail),
    ...routes,
  });
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  render(<Pane bridge={bridge} name="rail" />);
  act(() => { h.state(state); });
  const intents = () => h.posted.filter((m): m is Extract<PaneToExt, { type: "open" }> => m.type === "open").map((m) => m.intent);
  return { h, intents };
}

describe("the rail pane", () => {
  it("renders Me, Work, Agents, and History in order, under New message and New session", async () => {
    mount();
    const me = await screen.findByRole("list", { name: "Me" });
    expect(screen.getAllByRole("heading").map((h) => h.textContent)).toEqual(["Me", "Work", "Agents", "History (before the upgrade)"]);
    expect(within(me).getAllByRole("listitem").map((li) => li.textContent)).toEqual([expect.stringContaining("Ada"), expect.stringContaining("#design")]);
    expect(within(screen.getByRole("list", { name: "Work" })).getByText("ACME-42")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Agents" })).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "New message" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "New session" })).toBeInTheDocument();
  });

  it("a Me row shows its session's state or its kind, the needs-you dot, and its unread", async () => {
    mount();
    const ada = await meRow(/Ada/);
    expect(within(ada).getByText("idle")).toBeInTheDocument();
    expect(within(ada).getByLabelText("Needs you")).toBeInTheDocument();
    expect(within(ada).getByLabelText("2 unread")).toBeInTheDocument();
    const design = await meRow(/#design/);
    expect(within(design).getByText("room")).toBeInTheDocument();
    expect(within(design).queryByLabelText("Needs you")).toBeNull();
  });

  it("Unread keeps the Me rows with unread or needing you; All shows them all again", async () => {
    mount({ signedIn: true, target: null, windows: [] }, { "GET /app/user/api/rail": json({ ...rail, me: [...rail.me, row(CHN_C, "#ops", { unread: 1 })] }) });
    const me = await screen.findByRole("list", { name: "Me" });
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    expect(within(me).queryByText("#design")).toBeNull();
    expect(within(me).getByText("Ada")).toBeInTheDocument();
    expect(within(me).getByText("#ops")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: "All" }));
    expect(within(me).getByText("#design")).toBeInTheDocument();
  });

  it("nothing in any section says how to start", async () => {
    mount(undefined, { "GET /app/user/api/rail": json({ me: [], work: [], agents: [], history: [], unreadTotal: 0 }) });
    expect(await screen.findByText("No conversations yet. Start one with New message.")).toBeInTheDocument();
  });

  it("a work row opens its log; its worker opens the worker's session view", async () => {
    const { intents } = mount();
    await userEvent.click(await screen.findByRole("button", { name: /ACME-42/ }));
    await sleep(DOUBLE_CLICK_MS + 50);
    expect(intents()).toEqual([{ kind: "go", channel: CHN_LOG, session: SES_W }]);
    await userEvent.click(screen.getByRole("button", { name: "Open acme-42's session view" }));
    await vi.waitFor(() => expect(intents()).toHaveLength(2));
    expect(intents()[1]).toEqual({ kind: "go", channel: CHN_LOG, session: SES_W });
    expect(within(screen.getByRole("list", { name: "Work" })).getByLabelText("1 unread")).toBeInTheDocument();
  });

  it("a card's click opens its session's view; a double-click its studio window, when it may SSH", async () => {
    const { intents } = mount();
    const agents = await screen.findByRole("list", { name: "Agents" });
    await userEvent.click(within(agents).getByRole("button", { name: /carl/ }));
    await vi.waitFor(() => expect(intents()).toEqual([{ kind: "go", channel: CHN_C, session: SES_C }]));
    await userEvent.dblClick(within(agents).getByRole("button", { name: /Ada/ }));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()[1]).toEqual({ kind: "session", id: SES_A });
    expect(intents()).toHaveLength(2);
    await userEvent.dblClick(within(agents).getByRole("button", { name: /carl/ }));
    await vi.waitFor(() => expect(intents()).toHaveLength(3));
    expect(intents()[2]).toEqual({ kind: "go", channel: CHN_C, session: SES_C });
  });

  it("a session with no conversation for the viewer opens nothing", async () => {
    const { intents } = mount(undefined, { [`GET /app/user/api/sessions/${SES_C}`]: head(SES_C, "") });
    await userEvent.click(within(await screen.findByRole("list", { name: "Agents" })).getByRole("button", { name: /carl/ }));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()).toEqual([]);
  });

  it("History is collapsed; opened, its row goes to the legacy conversation", async () => {
    const { intents } = mount();
    const toggle = await screen.findByRole("button", { name: "History (before the upgrade)" });
    expect(screen.queryByRole("button", { name: /old thread/ })).toBeNull();
    await userEvent.click(toggle);
    await userEvent.click(await screen.findByRole("button", { name: /old thread/ }));
    await sleep(DOUBLE_CLICK_MS + 50);
    expect(intents()).toEqual([{ kind: "go", channel: LEGACY, session: "" }]);
  });

  it("dragging a Me row onto another moves it there and saves Me's order", async () => {
    // The server serves the order it saved.
    let served = rail;
    const { h } = mount(undefined, {
      "GET /app/user/api/rail": () => json(served),
      "PUT /app/user/api/rail/order": () => {
        served = { ...rail, me: [rail.me[1]!, rail.me[0]!] };
        return { kind: "http", status: 204, contentType: "" };
      },
    });
    const me = await screen.findByRole("list", { name: "Me" });
    const [ada, design] = within(me).getAllByRole("listitem");
    fireEvent.dragStart(design!);
    fireEvent.dragOver(ada!);
    fireEvent.drop(ada!);
    await vi.waitFor(() => expect(within(me).getAllByRole("listitem").map((li) => li.textContent)).toEqual([expect.stringContaining("#design"), expect.stringContaining("Ada")]));
    await vi.waitFor(() => expect(h.requests).toContainEqual(expect.objectContaining({ method: "PUT", path: "/app/user/api/rail/order", json: { section: "me", ids: [CHN_B, CHN_A] } })));
  });

  it("dragging a card saves Agents' order", async () => {
    const { h } = mount();
    const agents = await screen.findByRole("list", { name: "Agents" });
    const [ada, carl] = within(agents).getAllByRole("listitem");
    fireEvent.dragStart(ada!);
    fireEvent.dragOver(carl!);
    fireEvent.drop(carl!);
    await vi.waitFor(() => expect(h.requests).toContainEqual(expect.objectContaining({ method: "PUT", path: "/app/user/api/rail/order", json: { section: "agents", ids: [SES_C, SES_A] } })));
  });

  it("highlights the active conversation, and follows it", async () => {
    const { h } = mount({ signedIn: true, target: null, windows: [], active: CHN_B });
    const design = (await screen.findByText("#design")).closest("button")!;
    expect(design).toHaveAttribute("data-active");
    expect(await meRow(/Ada/)).not.toHaveAttribute("data-active");
    act(() => { h.state({ signedIn: true, target: null, windows: [], active: CHN_A }); });
    await vi.waitFor(async () => expect(await meRow(/Ada/)).toHaveAttribute("data-active"));
    expect(screen.getByText("#design").closest("button")).not.toHaveAttribute("data-active");
  });

  it("no newSession in the bootstrap: no New session button", async () => {
    mount(undefined, {}, { newSession: false });
    expect(await meRow(/Ada/)).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "New message" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New session" })).toBeNull();
  });

  it("New message: pick, write, send, then go to the new conversation", async () => {
    const { intents } = mount();
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.click(await screen.findByRole("button", { name: /Ann/ }));
    expect(await screen.findByText("to Ann")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message Ann" }), "hello Ann");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await vi.waitFor(() => expect(intents()).toEqual([{ kind: "go", channel: CHN_NEW, session: "" }]));
    await vi.waitFor(() => expect(screen.queryByText("to Ann")).toBeNull());
  });

  it("the message composer fills the pane and takes the focus", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.click(await screen.findByRole("button", { name: /Ann/ }));
    await screen.findByText("to Ann");
    expect(document.querySelector("[data-full-screen]")).not.toBeNull();
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Message Ann" })));
  });

  it("New session: start, then go to its conversation", async () => {
    const { intents } = mount();
    await userEvent.click(await screen.findByRole("button", { name: "New session" }));
    await userEvent.click(await screen.findByRole("radio", { name: "pair" }));
    await userEvent.click(screen.getByRole("button", { name: "Start" }));
    await vi.waitFor(() => expect(intents()).toEqual([{ kind: "go", channel: CHN_NEW, session: SES_NEW }]));
  });

  it("cancelling the picker or the message posts nothing", async () => {
    const { intents } = mount();
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await screen.findByRole("button", { name: /Ann/ });
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(screen.queryByRole("button", { name: /Ann/ })).toBeNull());
    await userEvent.click(screen.getByRole("button", { name: "New message" }));
    await userEvent.click(await screen.findByRole("button", { name: /Ann/ }));
    await screen.findByText("to Ann");
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(screen.queryByText("to Ann")).toBeNull());
    await sleep(DOUBLE_CLICK_MS + 50);
    expect(intents()).toEqual([]);
  });

  it("a failed send keeps the message open and posts nothing", async () => {
    const { intents } = mount(undefined, { "POST /app/user/api/messages": json({ error: "boom", code: "internal" }, 500) });
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.click(await screen.findByRole("button", { name: /Ann/ }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Message Ann" }), "hello");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await sleep(300);
    expect(screen.getByText("to Ann")).toBeInTheDocument();
    expect(intents()).toEqual([]);
  });

  it("a click posts go after DOUBLE_CLICK_MS", async () => {
    const { intents } = mount();
    await userEvent.click(await meRow(/Ada/));
    expect(intents()).toEqual([]);
    await sleep(DOUBLE_CLICK_MS + 50);
    expect(intents()).toEqual([{ kind: "go", channel: CHN_A, session: SES_A }]);
  });

  it("a double-click on an SSH-able session posts session, and no go", async () => {
    const { intents } = mount();
    await userEvent.dblClick(await meRow(/Ada/));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()).toEqual([{ kind: "session", id: SES_A }]);
  });

  it("a double-click on a row without a session posts go once", async () => {
    const { intents } = mount();
    await userEvent.dblClick(await meRow(/#design/));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()).toEqual([{ kind: "go", channel: CHN_B, session: "" }]);
  });

  it("▣ marks the Me rows and cards whose session has a window", async () => {
    mount({ signedIn: true, target: null, windows: [SES_A] });
    const ada = await meRow(/Ada/);
    expect(screen.getAllByLabelText("Has a window open")).toHaveLength(2);
    expect(ada).toContainElement(screen.getAllByLabelText("Has a window open")[0]!);
    expect(within(screen.getByRole("list", { name: "Agents" })).getAllByLabelText("Has a window open")).toHaveLength(1);
  });

  it("signed out shows Sign in", async () => {
    const { h } = mount({ signedIn: false, target: null });
    expect(await screen.findByText("Sign in to Jam to see your conversations.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New message" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
  });
});
