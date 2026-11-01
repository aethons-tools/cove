import { describe, expect, it } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import { focusManager } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { theme } from "../theme";
import { BootContext } from "../BootContext";
import { ConversationView } from "./ConversationView";
import type { Conversation, MessageRow, RailResponse } from "../api/types";

// today is a time on the current day: the view labels it "Today".
const today = (h: number, m: number): Date => {
  const d = new Date();
  d.setHours(h, m, 0, 0);
  return d;
};
const rail: RailResponse = { unreadTotal: 0, groups: [] };
const msg = (seq: number, over: Partial<MessageRow> = {}): MessageRow => ({
  from: "spider", body: `<p>m${seq}</p>`, raw: `m${seq}`, plain: false, at: "09:00", time: today(9, seq % 60).toISOString(),
  mine: false, seq, system: false, sysText: "", card: "", ...over,
});
const conv = (over: Partial<Conversation> = {}): Conversation => ({
  channelId: "chn_a", label: "intercom-spider", kind: "session", project: "test", phase: "live", needsYou: false, sendTo: "chn_a",
  messages: [msg(1), msg(2, { mine: true, raw: "**hi**", body: "<p><strong>hi</strong></p>" })], lastSeq: 2, earlier: false,
  sessionIds: ["ses_1"], sessions: [{ label: "intercom-spider", text: "is running", tool: "Bash", class: "busy" }],
  canJoin: false, canLeave: true, callIn: [{ to: "user:usr_2", label: "bob", kind: "person", project: "test" }], openAsks: [], session: "", newBoard: false, ...over,
});

function serve(c: Conversation | ((before: number) => Conversation), reads: number[] = []) {
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
    http.get("/app/user/api/conversations/:c", ({ request }) => {
      const before = Number(new URL(request.url).searchParams.get("before") ?? 0);
      return HttpResponse.json(typeof c === "function" ? c(before) : c);
    }),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: (typeof c === "function" ? c(0) : c).sessions })),
    http.post("/app/user/api/conversations/:c/read", async ({ request }) => {
      reads.push(((await request.json()) as { seq: number }).seq);
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return reads;
}

describe("the conversation view", () => {
  it("shows the header, messages (server HTML), presence, and marks read", async () => {
    const reads = serve(conv());
    renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByRole("heading", { name: "intercom-spider" })).toBeInTheDocument();
    expect(screen.getByText("test · agent")).toBeInTheDocument();
    expect(screen.getByText("m1")).toBeInTheDocument();
    expect(screen.getByText("hi").tagName).toBe("STRONG"); // rendered by squawkrender, inserted as HTML
    expect(screen.getByText("you")).toBeInTheDocument();
    expect(screen.getByText("Today")).toBeInTheDocument();
    expect(await screen.findByText(/is running/)).toBeInTheDocument();
    expect(screen.getByText("Bash")).toBeInTheDocument();
    await waitFor(() => expect(reads).toEqual([2]));
  });

  it("borders a session's bubbles and the composer in the session's color", async () => {
    serve(conv({ session: "ses_1", color: "#aa3f4e", messages: [msg(1, { color: "#aa3f4e" }), msg(2, { mine: true, raw: "mine", body: "<p>mine</p>" })] }));
    const { container } = renderApp(boot, "/app/user/c/chn_a");
    await screen.findByText("m1");
    const bubble = (seq: number) => container.querySelector(`[data-seq="${seq}"]`) as HTMLElement;
    expect(bubble(1).style.borderLeft).toBe("3px solid rgb(170, 63, 78)");
    expect(bubble(2).getAttribute("style") ?? "").not.toContain("border-left");
    expect((screen.getByRole("textbox").closest("[data-composer]") as HTMLElement).style.borderLeft).toBe("3px solid rgb(170, 63, 78)");
  });

  it("no session color: no borders", async () => {
    serve(conv());
    const { container } = renderApp(boot, "/app/user/c/chn_a");
    await screen.findByText("m1");
    expect(container.querySelector('[data-seq="1"]')!.getAttribute("style") ?? "").not.toContain("border-left");
    expect((screen.getByRole("textbox").closest("[data-composer]") as HTMLElement).style.borderLeft).toMatch(/^(0|0px)?$/);
  });

  it("offers Leave and Call in to a member, Join to a non-member", async () => {
    serve(conv());
    renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByRole("button", { name: "Leave" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Call in" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Join" })).toBeNull();
  });

  it("joins, then refetches", async () => {
    let joined = false;
    serve(conv({ canJoin: true, canLeave: false, callIn: [] }));
    server.use(
      http.post("/app/user/api/conversations/chn_a/join", () => { joined = true; return new HttpResponse(null, { status: 204 }); }),
      http.get("/app/user/api/conversations/:c", () => HttpResponse.json(conv({ canJoin: !joined, canLeave: joined }))),
    );
    renderApp(boot, "/app/user/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Join" }));
    expect(await screen.findByRole("button", { name: "Leave" })).toBeInTheDocument();
  });

  it("shows a refused membership change as a notification", async () => {
    serve(conv());
    server.use(http.post("/app/user/api/conversations/chn_a/leave", () => HttpResponse.json({ error: "this channel's members are fixed", code: "conflict" }, { status: 409 })));
    renderApp(boot, "/app/user/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Leave" }));
    expect(await screen.findByText("this channel's members are fixed")).toBeInTheDocument();
  });

  it("History is read-only: no controls, no mark-read", async () => {
    const reads = serve(conv({ channelId: "legacy:x", sendTo: "", canLeave: false, callIn: [], sessions: [] }));
    renderApp(boot, "/app/user/c/legacy%3Ax");
    expect(await screen.findByText("m1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Leave|Join|Call in/ })).toBeNull();
    await new Promise((r) => setTimeout(r, 50));
    expect(reads).toEqual([]);
  });

  it("loads earlier pages without duplicates, and keeps them across a refresh", async () => {
    const all = Array.from({ length: 250 }, (_, i) => msg(i + 1));
    const page = (before: number) => {
      const upto = before > 0 ? all.filter((m) => m.seq < before) : all;
      const slice = upto.slice(-200);
      return conv({ messages: slice, earlier: upto.length > 200, lastSeq: 250 });
    };
    serve(page);
    const { container } = renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByText("m250")).toBeInTheDocument();
    expect(screen.queryByText("m50")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Load earlier" }));
    expect(await screen.findByText("m50")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load earlier" })).toBeNull();
    expect(container.querySelectorAll("[data-seq]").length).toBe(250);
    // A refresh (focus here; a `changed` event alike) refetches every loaded
    // page, so the older messages stay.
    let refetched = 0;
    server.use(http.get("/app/user/api/conversations/:c", ({ request }) => {
      refetched++;
      const before = Number(new URL(request.url).searchParams.get("before") ?? 0);
      return HttpResponse.json(page(before));
    }));
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await waitFor(() => expect(refetched).toBeGreaterThanOrEqual(2));
    expect(container.querySelectorAll("[data-seq]").length).toBe(250);
    expect(screen.getByText("m50")).toBeInTheDocument();
  }, 20_000); // 250 rows render twice: slow under a parallel full run

  it("shows attachments, system lines, re: links and a placeholder for cards", async () => {
    serve(conv({ messages: [
      msg(1, { files: [{ id: "blb_1", name: "shot.png", size: "12 KB", image: true }, { id: "blb_2", name: "log.txt", size: "1 KB", image: false }] }),
      msg(2, { system: true, sysText: "bob answered: Yes" }),
      msg(3, { reAsk: { id: "ask_1", question: "Ship it?" } }),
      msg(4, { card: "ask", body: "<p>Ship it?</p>" }),
    ] }));
    renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByRole("img", { name: "shot.png" })).toHaveAttribute("src", "/app/user/blobs/blb_1");
    expect(screen.getByRole("link", { name: /log\.txt/ })).toHaveAttribute("href", "/app/user/blobs/blb_2");
    expect(screen.getByText("bob answered: Yes")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "re: Ship it?" })).toHaveAttribute("href", expect.stringMatching(/^\/app\/user\/c\/chn_a#ask-/));
    expect(screen.getByText("This ask can't be shown here.")).toBeInTheDocument();
  });

  it("gives code blocks a copy button", async () => {
    serve(conv({ messages: [msg(1, { body: "<pre><code>go test ./...\n</code></pre>" })] }));
    renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByRole("button", { name: "Copy code" })).toBeInTheDocument();
  });

  it("says when the conversation can't be opened", async () => {
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
      http.get("/app/user/api/conversations/:c", () => HttpResponse.json({ error: "no such conversation", code: "not_found" }, { status: 404 })),
    );
    renderApp(boot, "/app/user/c/chn_gone");
    expect(await screen.findByText("no such conversation")).toBeInTheDocument();
  });

  it("keeps the conversation on screen when a refresh fails", async () => {
    serve(conv());
    renderApp(boot, "/app/user/c/chn_a");
    expect(await screen.findByText("m1")).toBeInTheDocument();
    server.use(http.get("/app/user/api/conversations/:c", () => new HttpResponse("bad gateway", { status: 502, headers: { "Content-Type": "text/plain" } })));
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    expect(await screen.findByText(/Jam answered 502/)).toBeInTheDocument();
    expect(screen.getByText("m1")).toBeInTheDocument();
  });

  it("copies a message's text without the code button's icon", async () => {
    serve(conv({ messages: [msg(1, { body: "<p>run</p><pre><code>go test\n</code></pre>" })] }));
    renderApp(boot, "/app/user/c/chn_a");
    const btn = await screen.findByRole("button", { name: "Copy code" });
    expect(btn.textContent).toBe("");
    expect(btn.closest(".jam-body")!.textContent).toBe("rungo test\n");
  });

  it("keeps the rail on screen while switching conversations", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    const railRows: RailResponse = { unreadTotal: 0, groups: [{ bucket: "channels", title: "Channels", rows: [
      { id: "chn_a", label: "alpha", project: "test", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 2, selected: false, mission: "", session: "", ssh: false },
      { id: "chn_b", label: "beta", project: "test", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 2, selected: false, mission: "", session: "", ssh: false },
    ] }] };
    serve(conv());
    server.use(http.get("/app/user/api/rail", async ({ request }) => {
      if (new URL(request.url).searchParams.get("c") === "chn_b") await gate;
      return HttpResponse.json(railRows);
    }));
    renderApp(boot, "/app/user/c/chn_a");
    await userEvent.click(await screen.findByRole("link", { name: /beta/ }));
    expect(screen.getByRole("link", { name: /alpha/ })).toBeInTheDocument(); // still there while rail?c=chn_b loads
    release();
  });

  it("withSession false renders no session header", async () => {
    serve(conv({ session: "ses_1" }));
    server.use(http.get("/app/user/api/sessions/:id", () => HttpResponse.json({
      id: "ses_1", label: "intercom-spider", kind: "standing", ticket: "", role: "jam-ic", project: "test", owner: "",
      state: { label: "intercom-spider", text: "is running", tool: "Bash", class: "busy" }, since: "4m", openAsks: 0,
      cost: "", turnCost: "", tokens: "", context: "", contextTip: "", uptime: "", mission: "", ssh: "jam ssh ses_1",
      canRelease: false, canRestart: true, canEnd: true, canUpgrade: true, ops: true,
      canInterrupt: true, canCompact: false, canClear: true, restartLabel: "Restart", upgradeState: "", channel: "chn_a",
    })));
    render(
      <MantineProvider theme={theme} env="test">
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <BootContext.Provider value={boot}>
            <MemoryRouter><ConversationView id="chn_a" withSession={false} /></MemoryRouter>
          </BootContext.Provider>
        </QueryClientProvider>
      </MantineProvider>,
    );
    expect(await screen.findByText("m1")).toBeInTheDocument();
    expect(screen.queryByText("Copy SSH")).toBeNull();
    expect(screen.getByRole("textbox")).toBeInTheDocument();
  });
});
