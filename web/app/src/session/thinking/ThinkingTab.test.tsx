import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { tev } from "./testing";
import ThinkingTab from "./ThinkingTab";

class FakeES {
  static CLOSED = 2;
  static last: FakeES;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  readyState = 1;
  constructor(public url: string) { FakeES.last = this; }
  addEventListener(ev: string, fn: (e: MessageEvent) => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.readyState = 2; }
  emit(ev: string, data = "") { act(() => this.listeners[ev]?.forEach((fn) => fn(new MessageEvent(ev, { data })))); }
  send(...evs: ReturnType<typeof tev>[]) { for (const e of evs) this.emit("ev", JSON.stringify(e)); }
}

const paneJSON = (over: Record<string, unknown> = {}) => ({ id: "ses_1", tab: "thinking", previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [],
  streams: [{ id: "s1", firstAt: "2026-10-26T09:00:00Z", events: 12 }, { id: "s0", firstAt: "2026-10-25T09:00:00Z", events: 300 }], eventsOff: false, ...over });

function mount(props: { busy?: boolean; max?: number } = {}) {
  vi.stubGlobal("EventSource", FakeES);
  server.use(
    http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json(paneJSON())),
    http.get("/app/user/api/sessions/:id/thinking/raw", ({ request }) => HttpResponse.json({ raw: `{"seq":${new URL(request.url).searchParams.get("seq")}}`, truncated: 0 })),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<MantineProvider theme={theme} env="test"><QueryClientProvider client={client}><ThinkingTab id="ses_1" busy={props.busy ?? false} max={props.max} /></QueryClientProvider></MantineProvider>);
  FakeES.last.emit("stream", "s1");
  return FakeES.last;
}
const lines = () => screen.queryAllByTestId("te").map((n) => n.querySelector(".te-text")!.textContent);

// jsdom lays nothing out: give elements a viewport, so the virtualizer
// (which sizes its window from clientWidth/clientHeight) renders rows.
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 800 });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 600 });
});
afterAll(() => {
  delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
});
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

describe("the Thinking tab", () => {
  it("shows events as text (never markup), pairs tool results, and labels costs", async () => {
    const es = mount();
    es.send(tev(1, { text: "<img src=x onerror=alert(1)>" }),
      tev(2, { kind: "tool_call", tool: "Bash", toolUseId: "tu1", text: "go test", at: 1000 }),
      tev(3, { kind: "tool_result", tool: "Bash", toolUseId: "tu1", detail: "ok pkg", at: 4100 }),
      tev(4, { kind: "result", costUsd: 0.12, durationMs: 38000, claudeSession: "c1" }));
    expect(await screen.findByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
    expect(lines()).toEqual(["<img src=x onerror=alert(1)>", "go test", "episode 1 · $0.12 session total · 38s"]);
    const call = screen.getAllByTestId("te")[1]!;
    expect(within(call).getByText("✓ 3.1s")).toBeInTheDocument();
    expect(within(call).queryByText("ok pkg")).toBeNull();
    await userEvent.click(call.querySelector(".te-line")!);
    expect(within(screen.getAllByTestId("te")[1]!).getByText("ok pkg")).toBeInTheDocument();
  });

  it("clips long output to 40 lines with show all", async () => {
    const es = mount();
    es.send(tev(1, { kind: "tool_call", toolUseId: "t", text: "cat" }),
      tev(2, { kind: "tool_result", toolUseId: "t", detail: Array.from({ length: 45 }, (_, i) => `row${i}`).join("\n") }));
    const call = (await screen.findAllByTestId("te"))[0]!;
    await userEvent.click(call.querySelector(".te-line")!);
    const open = screen.getAllByTestId("te")[0]!;
    expect(within(open).queryByText(/row44/)).toBeNull();
    await userEvent.click(within(open).getByRole("button", { name: "show all 45 lines" }));
    expect(within(screen.getAllByTestId("te")[0]!).getByText(/row44/)).toBeInTheDocument();
  });

  it("filters with the chips, tool and search, and remembers chips, tool and format (not the search)", async () => {
    const es = mount();
    es.send(tev(1, { kind: "thinking", text: "pondering" }), tev(2, { kind: "tool_call", tool: "Edit", toolUseId: "e", text: "edit x" }), tev(3, { text: "done" }));
    await screen.findByText("pondering");
    await userEvent.click(screen.getByRole("button", { name: "Thinking" }));
    expect(lines()).toEqual(["edit x", "done"]);
    expect(screen.getByRole("button", { name: "Thinking" })).toHaveAttribute("aria-pressed", "false");
    await userEvent.selectOptions(screen.getByLabelText("Tool"), "Edit");
    expect(lines()).toEqual(["edit x"]);
    await userEvent.selectOptions(screen.getByLabelText("Tool"), "");
    await userEvent.type(screen.getByLabelText("Search events"), "don");
    expect(lines()).toEqual(["done"]);
    await userEvent.selectOptions(screen.getByLabelText("Format"), "detailed");
    expect(JSON.parse(localStorage.getItem("jam-thinking")!)).toEqual({
      f: { thinking: false, text: true, tools: true, errors: false, progress: false }, tool: "", format: "detailed" });
  });

  it("raw fetches each shown line's JSON", async () => {
    localStorage.setItem("jam-thinking", JSON.stringify({ format: "raw" }));
    const es = mount();
    es.send(tev(7));
    expect(await screen.findByText('{"seq":7}')).toBeInTheDocument();
  });

  it("loads earlier, and past the cap pauses; the pill reloads the live tail", async () => {
    server.use(http.get("/app/user/api/sessions/:id/thinking", ({ request }) => {
      const before = Number(new URL(request.url).searchParams.get("before"));
      return HttpResponse.json({ events: [tev(before - 2), tev(before - 1)], before: before - 2 });
    }));
    const es = mount({ max: 4 });
    es.send(tev(10), tev(11), tev(12));
    es.emit("more", "10");
    await userEvent.click(await screen.findByRole("button", { name: "Load earlier" }));
    await waitFor(() => expect(lines()).toEqual(["said 8", "said 9", "said 10", "said 11"])); // 12 trimmed: paused
    expect(screen.getByRole("button", { name: "Show new events" })).toHaveTextContent("1");
    es.send(tev(13));
    expect(lines()).toEqual(["said 8", "said 9", "said 10", "said 11"]);
    expect(screen.getByRole("button", { name: "Show new events" })).toHaveTextContent("2");
    const before = FakeES.last;
    await userEvent.click(screen.getByRole("button", { name: "Show new events" }));
    expect(FakeES.last).not.toBe(before); // reconnected to the live tail
    expect(lines()).toEqual([]);
  });

  it("marks a hole and a restart; picks an older stream; says working while busy", async () => {
    const es = mount({ busy: true });
    es.send(tev(1));
    es.emit("skipped", "2-5");
    expect(await screen.findByText(/events 2-5 skipped/)).toBeInTheDocument();
    expect(screen.getByText("working…")).toBeInTheDocument();
    es.emit("newstream", "s2");
    expect(screen.getByText("— session restarted —")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("option", { name: /300 events/ })).toBeInTheDocument());
    await userEvent.selectOptions(screen.getByLabelText("Stream"), "s0");
    expect(FakeES.last.url).toBe("/app/user/session/ses_1/thinking/events?stream=s0");
    expect(screen.getByLabelText("Stream")).toHaveValue("s0");
  });

  it("says when events aren't recorded", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json(paneJSON({ streams: [], eventsOff: true }))));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<MantineProvider theme={theme} env="test"><QueryClientProvider client={client}><ThinkingTab id="ses_1" busy={false} /></QueryClientProvider></MantineProvider>);
    expect(await screen.findByText("Session events aren't recorded on this jam.")).toBeInTheDocument();
  });
});
