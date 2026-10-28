import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { tev } from "../../session/thinking/testing";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { SessionInfo } from "../api/types";

class FakeES {
  static CLOSED = 2;
  static all: FakeES[] = [];
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  readyState = 1;
  constructor(public url: string) { FakeES.all.push(this); }
  addEventListener(ev: string, fn: (e: MessageEvent) => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.readyState = 2; }
  emit(ev: string, data = "") { act(() => this.listeners[ev]?.forEach((fn) => fn(new MessageEvent(ev, { data })))); }
}
const last = () => FakeES.all[FakeES.all.length - 1]!;

const api = "/app/admin/api/agents/ses_1/session";
const info = (over: Partial<SessionInfo> = {}): SessionInfo => ({
  enabled: true, stream: "s1",
  streams: [{ streamId: "s1", started: "2026-10-28 09:00:00", lastEvent: "Oct 28 10:00:00", events: 12 }, { streamId: "s0", started: "2026-10-27 09:00:00", lastEvent: "Oct 27 10:00:00", events: 300 }],
  totals: { turns: 3, episodes: 2, toolCalls: 1234, inputTokens: 1500000, outputTokens: 2000, costUsd: 4.5 }, ...over,
});
const mount = (i: SessionInfo, path = ADMIN + "agents/ses_1/session") => {
  vi.stubGlobal("EventSource", FakeES);
  server.use(
    http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
    http.get(api, () => HttpResponse.json(i)),
  );
  return renderAdmin(adminBoot, path);
};

beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 800 });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 600 });
});
afterAll(() => {
  delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
});
afterEach(() => { vi.unstubAllGlobals(); FakeES.all = []; });

describe("an agent's session timeline", () => {
  it("shows the stream's live totals and its events through the Thinking rows", async () => {
    mount(info());
    await waitFor(() => expect(last().url).toBe(api + "/events?stream=s1"));
    last().emit("totals", JSON.stringify({ turns: 3, episodes: 2, toolCalls: 1234, inputTokens: 1500000, outputTokens: 2000, costUsd: 4.5 }));
    expect(await screen.findByText(/3 turns · 2 episodes · 1,234 tool calls · 1,500,000 in \/ 2,000 out · \$4\.50/)).toBeInTheDocument();
    last().emit("totals", JSON.stringify({ turns: 4, episodes: 2, toolCalls: 1235, inputTokens: 1500000, outputTokens: 2100, costUsd: 4.75 }));
    expect(await screen.findByText(/4 turns · .* · \$4\.75/)).toBeInTheDocument();
    last().emit("ev", JSON.stringify(tev(1, { text: "<b>hello</b>" })));
    expect(await screen.findByText("<b>hello</b>")).toBeInTheDocument();
    expect(document.querySelector(".te-text b")).toBeNull();
  });

  it("lists the streams; choosing one sets ?stream= and follows it", async () => {
    const { router } = mount(info());
    const sel = await screen.findByRole("combobox", { name: "Stream" });
    expect(screen.getByRole("option", { name: "2026-10-27 09:00:00 · 300 events" })).toBeInTheDocument();
    server.use(http.get(api, () => HttpResponse.json(info({ stream: "s0" }))));
    await userEvent.selectOptions(sel, "s0");
    await waitFor(() => expect(router.state.location.search).toBe("?stream=s0"));
    await waitFor(() => expect(last().url).toBe(api + "/events?stream=s0"));
  });

  it("shows the stream the server chose for an unknown ?stream=", async () => {
    mount(info(), ADMIN + "agents/ses_1/session?stream=nope");
    await waitFor(() => expect(last().url).toBe(api + "/events?stream=s1"));
    expect(FakeES.all.some((e) => e.url.includes("nope"))).toBe(false);
  });

  it("with no streams yet, follows the first one and fills the select when it starts", async () => {
    let calls = 0;
    mount(info({ streams: [], stream: "" }));
    server.use(http.get(api, () => (++calls, HttpResponse.json(calls === 1 ? info({ streams: [], stream: "" }) : info()))));
    await waitFor(() => expect(last().url).toBe(api + "/events"));
    last().emit("stream", "s1");
    last().emit("ev", JSON.stringify(tev(1, { text: "first words" })));
    expect(await screen.findByText("first words")).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: "2026-10-28 09:00:00 · 12 events" })).toBeInTheDocument();
  });

  it("says when session events are not configured", async () => {
    mount(info({ enabled: false, streams: [], stream: "" }));
    expect(await screen.findByText("Session events are not configured on this Jam.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to ses_1" })).toHaveAttribute("href", ADMIN + "agents/ses_1");
  });
});
