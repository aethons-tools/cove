import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, SessionHead, SessionPane } from "../api/types";

const head = (over: Partial<SessionHead> = {}): SessionHead => ({
  id: "ses_1", label: "intercom-spider", kind: "standing", ticket: "", role: "jam-ic", project: "test", owner: "",
  state: { label: "intercom-spider", text: "is running", tool: "Bash", class: "busy" }, since: "4m", openAsks: 2,
  cost: "$1.20", turnCost: "$0.10", tokens: "1,200 in / 300 out tokens", context: "ctx 84k / 200k · 42%",
  contextTip: "context: 84,300 of 200,000 tokens (claude-x)", uptime: "2h 5m", mission: "Jam SPA slice 5", ssh: "at-jam ssh ses_1",
  studio: false, canRelease: false, canRestart: true, canUpgrade: true, ops: true,
  canInterrupt: true, canCompact: false, canClear: true, restartLabel: "Restart", upgradeState: "", ...over,
});
const pane = (tab: string, over: Partial<SessionPane> = {}): SessionPane => ({
  id: "ses_1", tab, previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], streams: [], eventsOff: false, ...over,
});
const conv: Conversation = {
  channelId: "chn_s", label: "intercom-spider", kind: "session", project: "test", phase: "live", needsYou: false, sendTo: "chn_s",
  messages: [], lastSeq: 0, earlier: false, sessionIds: ["ses_1"], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [],
  session: "ses_1",
};

// serve answers the conversation, its session head (null = 404) and pane
// tabs; it returns the action POSTs seen.
function serve(h: SessionHead | null | (() => SessionHead | null), panes: Record<string, SessionPane> = {}) {
  const posts: string[] = [];
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(conv)),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.get("/app/user/api/sessions/:id", () => {
      const v = typeof h === "function" ? h() : h;
      return v ? HttpResponse.json(v) : HttpResponse.json({ error: "no such session", code: "not_found" }, { status: 404 });
    }),
    http.get("/app/user/api/sessions/:id/pane/:tab", ({ params }) =>
      HttpResponse.json(panes[params.tab as string] ?? pane(params.tab as string))),
    http.post("/app/user/api/sessions/:id/:action", ({ params }) => {
      posts.push(params.action as string);
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return posts;
}

afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

describe("the session header", () => {
  it("shows who, what it's doing, cost, context and uptime", async () => {
    serve(head());
    renderApp(boot, "/app/next/c/chn_s");
    const hd = await screen.findByRole("region", { name: "Session" });
    for (const text of ["standing", "jam-ic · test", "Jam SPA slice 5", "is running", "Bash", "· 4m", "?2", "· this turn $0.10", "up 2h 5m"]) {
      expect(within(hd).getByText(text, { exact: false })).toBeInTheDocument();
    }
    expect(within(hd).getByTitle("1,200 in / 300 out tokens")).toHaveTextContent("$1.20");
    expect(within(hd).getByText("ctx 84k / 200k · 42%")).toHaveAttribute("title", "context: 84,300 of 200,000 tokens (claude-x)");
    // Studio only with the Studio tab; the rest always.
    expect(within(hd).getAllByRole("tab").map((b) => b.textContent)).toEqual(["Thinking", "Previews", "Artifacts", "Status"]);
  });

  it("runs Interrupt at once and confirms the ⋯ menu's ending actions", async () => {
    const posts = serve(head());
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("button", { name: "Interrupt" }));
    await waitFor(() => expect(posts).toEqual(["interrupt"]));
    await user.click(screen.getByRole("button", { name: "More actions" }));
    expect(await screen.findByRole("menuitem", { name: "Compact" })).toBeDisabled();
    await user.click(screen.getByRole("menuitem", { name: "Restart" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Restart intercom-spider?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(posts).toEqual(["interrupt"]);
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Clear" }));
    const d2 = await screen.findByRole("dialog");
    expect(within(d2).getByText(/forgets the conversation/)).toBeInTheDocument();
    await user.click(within(d2).getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(posts).toEqual(["interrupt", "clear"]));
  });

  it("shows only what the viewer may do", async () => {
    serve(head({ ops: false, canRestart: false, canUpgrade: false, ssh: "" }));
    renderApp(boot, "/app/next/c/chn_s");
    await screen.findByRole("region", { name: "Session" });
    for (const name of ["Interrupt", "More actions", "Copy SSH"]) expect(screen.queryByRole("button", { name })).toBeNull();
  });

  it("shows the server's refusal, and changes nothing", async () => {
    serve(head());
    server.use(http.post("/app/user/api/sessions/:id/:action", () =>
      HttpResponse.json({ error: "interrupt is not available while the session is waiting", code: "conflict" }, { status: 409 })));
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("button", { name: "Interrupt" }));
    expect(await screen.findByText("interrupt is not available while the session is waiting")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Session" })).toBeInTheDocument();
  });

  it("keeps the ⋯ menu open across a live refresh", async () => {
    let n = 0;
    serve(() => head({ since: `${++n}m` }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s", client);
    await user.click(await screen.findByRole("button", { name: "More actions" }));
    expect(await screen.findByRole("menuitem", { name: "Upgrade" })).toBeInTheDocument();
    const before = n;
    await client.invalidateQueries({ queryKey: ["session"] });
    await waitFor(() => expect(screen.getByText(`· ${before + 1}m`, { exact: false })).toBeInTheDocument());
    expect(screen.getByRole("menuitem", { name: "Upgrade" })).toBeInTheDocument();
  });
});

const mockNarrow = (narrow: boolean) =>
  vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
    matches: narrow && query === "(max-width: 900px)", media: query, onchange: null,
    addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
  }) as unknown as MediaQueryList);
const saved = (key: string) => JSON.parse(localStorage.getItem("jam-pane:" + key)!) as Record<string, unknown>;

describe("the session pane", () => {
  it("opens from a tab, toggles closed, moves below, and is remembered per session", async () => {
    serve(head(), { status: pane("status", { status: [{ name: "Turns", value: "12 (3 episodes)" }] }) });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Status" }));
    const aside = await screen.findByRole("complementary", { name: "Session pane" });
    expect(await within(aside).findByText("12 (3 episodes)")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Status" })).toHaveAttribute("aria-selected", "true");
    expect(saved("ses_1")).toEqual({ tab: "status", place: "right" });
    await user.click(within(aside).getByRole("button", { name: "Move the pane right or below" }));
    expect(aside.closest("[data-place]")).toHaveAttribute("data-place", "below");
    expect(screen.getByRole("separator")).toHaveAttribute("aria-orientation", "horizontal");
    await user.click(screen.getByRole("tab", { name: "Status" }));
    expect(screen.queryByRole("complementary", { name: "Session pane" })).toBeNull();
    expect(saved("last")).toEqual({ tab: null, place: "below" });
  });

  it("resizes from the keyboard, 24px a step, and keeps the size", async () => {
    localStorage.setItem("jam-pane:ses_1", JSON.stringify({ tab: "status", place: "right", w: 400 }));
    serve(head());
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    const div = await screen.findByRole("separator", { name: "Resize the session pane" });
    expect(div).toHaveAttribute("aria-orientation", "vertical");
    // jsdom lays nothing out: clamp against a stubbed rect.
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 1200, height: 800 } as DOMRect);
    div.focus();
    await user.keyboard("{ArrowLeft}");
    expect(saved("ses_1").w).toBe(424);
    await user.keyboard("{ArrowRight}{ArrowRight}");
    expect(saved("ses_1").w).toBe(376);
    expect(screen.getByRole("complementary", { name: "Session pane" }).style.flexBasis).toBe("376px");
  });

  it("saves a drag's final size on release", async () => {
    localStorage.setItem("jam-pane:ses_1", JSON.stringify({ tab: "status", place: "right", w: 400 }));
    serve(head());
    renderApp(boot, "/app/next/c/chn_s");
    const div = await screen.findByRole("separator", { name: "Resize the session pane" });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 1200, height: 800, right: 1200, bottom: 800 } as DOMRect);
    div.setPointerCapture = () => {};
    const { fireEvent } = await import("@testing-library/react");
    fireEvent.pointerDown(div, { button: 0, pointerId: 1 });
    fireEvent.pointerMove(div, { clientX: 700, pointerId: 1 });
    fireEvent.pointerMove(div, { clientX: 650, pointerId: 1 });
    fireEvent.pointerUp(div, { pointerId: 1 });
    expect(saved("ses_1").w).toBe(550);
  });

  it("starts a session never opened from the last layout; ?tab= opens a tab and is kept", async () => {
    localStorage.setItem("jam-pane:last", JSON.stringify({ tab: "previews", place: "below" }));
    serve(head());
    renderApp(boot, "/app/next/c/chn_s?tab=artifacts");
    const aside = await screen.findByRole("complementary", { name: "Session pane" });
    expect(await within(aside).findByText("Nothing shared here yet.")).toBeInTheDocument();
    expect(saved("ses_1")).toEqual({ tab: "artifacts", place: "below" });
  });

  it("keeps a remembered Studio closed without the Studio tab, and its layout unchanged", async () => {
    localStorage.setItem("jam-pane:ses_1", JSON.stringify({ tab: "studio", place: "right", w: 700 }));
    serve(head({ studio: false }));
    renderApp(boot, "/app/next/c/chn_s?tab=studio");
    await screen.findByRole("region", { name: "Session" });
    expect(screen.queryByRole("complementary", { name: "Session pane" })).toBeNull();
    expect(saved("ses_1")).toEqual({ tab: "studio", place: "right", w: 700 });
  });

  it("puts the pane below in a narrow window, without the ⇄ button or changing the saved place", async () => {
    mockNarrow(true);
    localStorage.setItem("jam-pane:ses_1", JSON.stringify({ tab: "status", place: "right" }));
    serve(head());
    renderApp(boot, "/app/next/c/chn_s");
    const aside = await screen.findByRole("complementary", { name: "Session pane" });
    expect(aside.closest("[data-place]")).toHaveAttribute("data-place", "below");
    expect(within(aside).queryByRole("button", { name: "Move the pane right or below" })).toBeNull();
    expect(saved("ses_1").place).toBe("right");
  });

  it("lists previews, artifacts and status, and links Studio to the current Inbox", async () => {
    serve(head({ studio: true }), {
      previews: pane("previews", { previews: [{ name: "web", url: "https://web--ses_1.preview.test/", port: 5173, frame: true }] }),
      artifacts: pane("artifacts", {
        boards: [{ id: "brd_1", title: "Plan", channel: "chn_s" }],
        files: [{ id: "blb_1", name: "shot.png", size: "12 KB", image: true }, { id: "blb_2", name: "log.txt", size: "3 KB", image: false }],
        asks: [{ id: "ask_1", question: "Merge it?", state: "open" }],
        links: [{ url: "https://github.com/aethons-tools/cove/pull/472", label: "cove PR #472" }],
      }),
    });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Previews" }));
    const web = await screen.findByRole("link", { name: /web.*port 5173/ });
    expect(web).toHaveAttribute("href", "https://web--ses_1.preview.test/");
    expect(web).toHaveAttribute("target", "_blank");
    expect(web).toHaveAttribute("rel", "noopener");
    await user.click(screen.getByRole("tab", { name: "Artifacts" }));
    expect(await screen.findByRole("link", { name: /Plan/ })).toHaveAttribute("href", "/app/user/?c=chn_s&board=brd_1");
    expect(screen.getByRole("img", { name: "shot.png" })).toHaveAttribute("src", "/app/user/blobs/blb_1");
    expect(screen.getByRole("link", { name: /log\.txt/ })).toHaveAttribute("download");
    expect(screen.getByRole("link", { name: "cove PR #472" })).toHaveAttribute("title", "https://github.com/aethons-tools/cove/pull/472");
    const target = document.createElement("div");
    target.id = "ask-ask_1";
    target.scrollIntoView = vi.fn();
    document.body.append(target);
    await user.click(screen.getByRole("button", { name: /Merge it\?/ }));
    expect(target.scrollIntoView).toHaveBeenCalled();
    target.remove();
    // Studio links to the current Inbox until slice 6b.
    await user.click(screen.getByRole("tab", { name: "Studio" }));
    expect(await screen.findByRole("link", { name: "Open it in the current Inbox" })).toHaveAttribute("href", "/app/user/?c=chn_s&tab=studio");
  });

  it("opens the Thinking tab in the app (its own chunk), following the session's feed", async () => {
    const urls: string[] = [];
    vi.stubGlobal("EventSource", class { static CLOSED = 2; readyState = 1; constructor(u: string) { urls.push(u); } addEventListener() {} close() {} });
    serve(head(), { thinking: pane("thinking", { streams: [] }) });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Thinking" }));
    expect(await screen.findByRole("log", { name: "Session events" })).toBeInTheDocument();
    expect(urls).toContain("/app/user/session/ses_1/thinking/events?format=json");
    vi.unstubAllGlobals();
  });

  it("says when previews are off", async () => {
    serve(head(), { previews: pane("previews", { previewsOff: true }) });
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await user.click(await screen.findByRole("tab", { name: "Previews" }));
    expect(await screen.findByText("Previews aren't available to you here.")).toBeInTheDocument();
  });

  it("drops the header and pane once the session is gone, keeping the conversation", async () => {
    let live = true;
    localStorage.setItem("jam-pane:ses_1", JSON.stringify({ tab: "status", place: "right" }));
    const posts = serve(() => (live ? head({ canRelease: true }) : null));
    server.use(http.post("/app/user/api/sessions/:id/:action", ({ params }) => {
      posts.push(params.action as string);
      live = false;
      return new HttpResponse(null, { status: 204 });
    }));
    const user = userEvent.setup();
    renderApp(boot, "/app/next/c/chn_s");
    await screen.findByRole("complementary", { name: "Session pane" });
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Release" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Release" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Session" })).toBeNull());
    expect(screen.queryByRole("complementary", { name: "Session pane" })).toBeNull();
    expect(screen.getByRole("heading", { name: "intercom-spider" })).toBeInTheDocument(); // the conversation stays
    expect(screen.queryByText("no such session")).toBeNull(); // a gone session is not an error
    expect(posts).toEqual(["release"]);
  });

  it("keeps the conversation (composer, place) mounted when its session ends", async () => {
    let live = true;
    serve(() => (live ? head() : null));
    server.use(http.get("/app/user/api/conversations/:c", () => HttpResponse.json({ ...conv, session: live ? "ses_1" : "" })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderApp(boot, "/app/next/c/chn_s", client);
    await screen.findByRole("region", { name: "Session" });
    const box = screen.getByRole("textbox");
    live = false;
    await client.invalidateQueries();
    await waitFor(() => expect(screen.queryByRole("region", { name: "Session" })).toBeNull());
    await waitFor(() => expect(client.getQueryState(["conversation", "chn_s"])?.fetchStatus).toBe("idle"));
    expect(screen.getByRole("textbox")).toBe(box);
  });

  it("leaves the saved layouts alone in a conversation without a session", async () => {
    serve(head());
    server.use(http.get("/app/user/api/conversations/:c", () => HttpResponse.json({ ...conv, session: "" })));
    renderApp(boot, "/app/next/c/chn_s?tab=status");
    await screen.findByRole("heading", { name: "intercom-spider" });
    expect(screen.queryByRole("region", { name: "Session" })).toBeNull();
    expect(localStorage.length).toBe(0);
  });
});
