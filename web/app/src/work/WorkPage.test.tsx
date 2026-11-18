import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, MessageRow, RailResponse, WorkUnit } from "../api/types";
import { when } from "./WorkPanel";

const unit = (over: Partial<WorkUnit> = {}): WorkUnit => ({
  key: "ACME-42", title: "Fix the flaky login test", url: "https://linear.app/acme/issue/ACME-42", log: "chn_acme42", state: "in-review",
  workers: [
    { session: "ses_old", label: "acme-42", started: "2026-11-15T10:00:00Z", stopped: "2026-11-15T18:30:00Z", current: false, view: "" },
    { session: "ses_new", label: "acme-42-b", started: "2026-11-16T09:12:00Z", current: true, view: "/app/user/s/ses_new" },
  ],
  pullRequests: [
    { url: "https://github.com/acme/web/pull/741", number: 741, title: "First try", state: "closed", reportedAt: "2026-11-15T12:00:00Z" },
    { url: "https://github.com/acme/web/pull/745", number: 745, title: "Fix the login test", state: "open", reportedAt: "2026-11-16T12:00:00Z" },
  ],
  ...over,
});
const rail: RailResponse = {
  unreadTotal: 0, me: [], agents: [], history: [],
  work: [{ key: "ACME-42", title: "Fix the flaky login test", log: "chn_acme42", worker: { id: "ses_new", label: "acme-42-b", state: "working" }, state: "in-review" }],
};
const msg = (seq: number, raw: string): MessageRow => ({
  from: "acme-42-b", body: `<p>${raw}</p>`, raw, plain: false, at: "09:00", time: "2026-11-16T09:00:00Z",
  mine: false, seq, system: false, sysText: "", card: "",
});
const log: Conversation = {
  channelId: "chn_acme42", label: "ACME-42", kind: "work", project: "acme", phase: "live", needsYou: false, sendTo: "chn_acme42",
  messages: [msg(1, "Starting with the route.")], lastSeq: 1, earlier: false, sessionIds: [], sessions: [],
  canJoin: true, canLeave: false, callIn: [], openAsks: [], session: "", newBoard: false,
};

// serve answers the unit (null = not found), its log and the rail; it
// returns the messages posted.
function serve(u: WorkUnit | null = unit()) {
  const sent: unknown[] = [];
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
    http.get("/app/user/api/work/*", () => (u ? HttpResponse.json(u) : HttpResponse.json({ error: "no such unit of work", code: "not_found" }, { status: 404 }))),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(log)),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.post("/app/user/api/messages", async ({ request }) => {
      sent.push(await request.json());
      return HttpResponse.json({ channel: "chn_acme42", seq: 2 }, { status: 201 });
    }),
  );
  return sent;
}

const mockNarrow = () =>
  vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
    matches: query === "(max-width: 900px)", media: query, onchange: null,
    addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
  }) as unknown as MediaQueryList);

afterEach(() => { vi.restoreAllMocks(); });

describe("a unit of work's page", () => {
  it("heads the page with the ticket, linked to the tracker, and its state", async () => {
    serve();
    renderApp(boot, "/app/user/w/ACME-42");
    const head = await screen.findByRole("banner", { name: "Unit of work" });
    expect(within(head).getByRole("link", { name: "ACME-42" })).toHaveAttribute("href", "https://linear.app/acme/issue/ACME-42");
    expect(within(head).getByRole("link", { name: "Fix the flaky login test" })).toHaveAttribute("href", "https://linear.app/acme/issue/ACME-42");
    expect(within(head).getByRole("link", { name: /Linear/ })).toHaveAttribute("href", "https://linear.app/acme/issue/ACME-42");
    expect(within(head).getByText("in review")).toBeInTheDocument();
    // The log's membership controls stay on the page.
    expect(within(head).getByRole("button", { name: "Join" })).toBeInTheDocument();
  });

  it("shows the work log, with a composer that posts to it", async () => {
    const sent = serve();
    const user = userEvent.setup();
    renderApp(boot, "/app/user/w/ACME-42");
    expect(await screen.findByText("Starting with the route.")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Message ACME-42" }), "ship it");
    await user.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(sent).toEqual([{ to: "chn_acme42", body: "ship it" }]));
  });

  it("lists the agents, the current one first with its status, linked when they may be opened", async () => {
    serve();
    renderApp(boot, "/app/user/w/ACME-42");
    const agents = await screen.findByRole("list", { name: "Agents" });
    const items = within(agents).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(within(items[0]!).getByRole("link", { name: "acme-42-b" })).toHaveAttribute("href", "/app/user/s/ses_new");
    expect(items[0]).toHaveTextContent(`Worker now, since ${when("2026-11-16T09:12:00Z")}`);
    expect(await within(items[0]!).findByText("working")).toBeInTheDocument();
    expect(within(items[1]!).queryByRole("link")).toBeNull();
    expect(items[1]).toHaveTextContent("acme-42");
    expect(items[1]).toHaveTextContent(`${when("2026-11-15T10:00:00Z")} to ${when("2026-11-15T18:30:00Z")}`);
  });

  it("lists the pull requests with their number, title and state, each linked", async () => {
    serve();
    renderApp(boot, "/app/user/w/ACME-42");
    const prs = await screen.findByRole("list", { name: "Pull requests" });
    const items = within(prs).getAllByRole("listitem");
    expect(within(items[0]!).getByRole("link", { name: "#741" })).toHaveAttribute("href", "https://github.com/acme/web/pull/741");
    expect(items[0]).toHaveTextContent("First try");
    expect(within(items[0]!).getByText("closed")).toBeInTheDocument();
    expect(within(items[1]!).getByRole("link", { name: "#745" })).toHaveAttribute("href", "https://github.com/acme/web/pull/745");
    expect(within(items[1]!).getByText("open")).toBeInTheDocument();
  });

  it("folds the panel away with Hide, and back with Show", async () => {
    serve();
    const user = userEvent.setup();
    renderApp(boot, "/app/user/w/ACME-42");
    await screen.findByRole("list", { name: "Agents" });
    await user.click(screen.getByRole("button", { name: "Hide" }));
    expect(screen.queryByRole("list", { name: "Agents" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show the panel" }));
    expect(screen.getByRole("list", { name: "Agents" })).toBeInTheDocument();
  });

  it("on a narrow screen, makes the log and the panel two tabs", async () => {
    mockNarrow();
    serve();
    const user = userEvent.setup();
    renderApp(boot, "/app/user/w/ACME-42");
    expect(await screen.findByText("Starting with the route.")).toBeVisible();
    expect(screen.getByRole("tab", { name: "Log" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("list", { name: "Agents" })).toBeNull();
    await user.click(screen.getByRole("tab", { name: "Agents & pull requests" }));
    expect(await screen.findByRole("list", { name: "Agents" })).toBeInTheDocument();
    expect(screen.queryByText("Starting with the route.")).toBeNull();
  });

  it("reads a GitHub key with its slash and hash", async () => {
    serve(unit({ key: "acme/web#12", url: "https://github.com/acme/web/issues/12" }));
    let asked = "";
    server.use(http.get("/app/user/api/work/*", ({ request }) => {
      asked = new URL(request.url).pathname;
      return HttpResponse.json(unit({ key: "acme/web#12", url: "https://github.com/acme/web/issues/12" }));
    }));
    renderApp(boot, `/app/user/w/${encodeURIComponent("acme/web#12")}`);
    const head = await screen.findByRole("banner", { name: "Unit of work" });
    expect(within(head).getByRole("link", { name: "acme/web#12" })).toBeInTheDocument();
    expect(within(head).getByRole("link", { name: /GitHub/ })).toBeInTheDocument();
    expect(asked).toBe("/app/user/api/work/acme%2Fweb%2312");
  });

  it("says not found to one who may not read the log", async () => {
    serve(null);
    renderApp(boot, "/app/user/w/ACME-99");
    expect(await screen.findByText("Nothing here.")).toBeInTheDocument();
  });
});
