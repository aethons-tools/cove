import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PaneState, PaneToExt } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setLiveSource } from "../live/useLiveEvents";
import { boot } from "../test/render";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { Pane } from "./Pane";
import { DOUBLE_CLICK_MS } from "./RailPane";

const SES_A = "ses_0abcdefghjkmnpqrstvwxyz012";
const CHN_A = "chn_0abcdefghjkmnpqrstvwxyz012";
const CHN_B = "chn_1abcdefghjkmnpqrstvwxyz012";
const CHN_NEW = "chn_2abcdefghjkmnpqrstvwxyz012";
const SES_NEW = "ses_2abcdefghjkmnpqrstvwxyz012";
const row = (id: string, label: string, over = {}) => ({
  id, label, project: "p", kind: "studio", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, ...over,
});
const rail = {
  groups: [
    { bucket: "needs-you", title: "Needs you", rows: [row(CHN_A, "Ada", { session: SES_A, ssh: true })] },
    { bucket: "channels", title: "Channels", rows: [row(CHN_B, "#design")] },
  ],
  unreadTotal: 0,
};
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
    "GET /app/user/api/rail": json(rail),
    "GET /app/user/api/recipients": json(recipients),
    "GET /app/user/api/session-roles": json(roles),
    "POST /app/user/api/messages": json({ channel: CHN_NEW, seq: 1 }, 201),
    "POST /app/user/api/sessions": json({ id: SES_NEW, name: "pair-01", channel: CHN_NEW }, 201),
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
  it("renders the groups and rows like the web rail, under New message and New session", async () => {
    mount();
    expect(await screen.findByText("Ada")).toBeInTheDocument();
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByText("#design")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New message" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "New session" })).toBeInTheDocument();
  });

  it("no newSession in the bootstrap: no New session button", async () => {
    mount(undefined, {}, { newSession: false });
    expect(await screen.findByText("Ada")).toBeInTheDocument();
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
    await userEvent.click(await screen.findByRole("button", { name: /Ada/ }));
    expect(intents()).toEqual([]);
    await sleep(DOUBLE_CLICK_MS + 50);
    expect(intents()).toEqual([{ kind: "go", channel: CHN_A, session: SES_A }]);
  });

  it("a double-click on an SSH-able session posts session, and no go", async () => {
    const { intents } = mount();
    await userEvent.dblClick(await screen.findByRole("button", { name: /Ada/ }));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()).toEqual([{ kind: "session", id: SES_A }]);
  });

  it("a double-click on a row without a session posts go once", async () => {
    const { intents } = mount();
    await userEvent.dblClick(await screen.findByRole("button", { name: /#design/ }));
    await sleep(DOUBLE_CLICK_MS * 2);
    expect(intents()).toEqual([{ kind: "go", channel: CHN_B, session: "" }]);
  });

  it("▣ marks rows whose session has a window", async () => {
    mount({ signedIn: true, target: null, windows: [SES_A] });
    const ada = await screen.findByRole("button", { name: /Ada/ });
    expect(screen.getAllByLabelText("Has a window open")).toHaveLength(1);
    expect(ada).toContainElement(screen.getByLabelText("Has a window open"));
  });

  it("signed out shows Sign in", async () => {
    const { h } = mount({ signedIn: false, target: null });
    expect(await screen.findByText("Sign in to Jam to see your conversations.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
  });
});
