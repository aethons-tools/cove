import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PaneState, PaneToExt } from "../bridge/protocol";
import { setTransport } from "../api/client";
import { setLiveSource } from "../live/useLiveEvents";
import { boot } from "../test/render";
import { Bridge, install } from "./bridge";
import { fakeHost, json } from "./fakeHost";
import { Pane } from "./Pane";
import { DOUBLE_CLICK_MS } from "./RailPane";

const SES_A = "ses_0abcdefghjkmnpqrstvwxyz012";
const CHN_A = "chn_0abcdefghjkmnpqrstvwxyz012";
const CHN_B = "chn_1abcdefghjkmnpqrstvwxyz012";
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

function mount(state: PaneState = { signedIn: true, target: null, windows: [] }) {
  const h = fakeHost({ "GET /app/user/api/bootstrap": json(boot), "GET /app/user/api/rail": json(rail) });
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  render(<Pane bridge={bridge} name="rail" />);
  act(() => { h.state(state); });
  const intents = () => h.posted.filter((m): m is Extract<PaneToExt, { type: "open" }> => m.type === "open").map((m) => m.intent);
  return { h, intents };
}

describe("the rail pane", () => {
  it("renders the groups and rows like the web rail, with no New message", async () => {
    mount();
    expect(await screen.findByText("Ada")).toBeInTheDocument();
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByText("#design")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New message" })).toBeNull();
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
