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
  id: "ses_1", tab, previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], ...over,
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
