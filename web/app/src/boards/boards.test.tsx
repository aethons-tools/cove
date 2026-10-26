import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, MessageRow } from "../api/types";

const card: MessageRow = { from: "alice", body: "", raw: "", plain: false, at: "09:00", time: new Date(2026, 9, 26, 9, 0).toISOString(), mine: false, seq: 4,
  system: false, sysText: "", card: "board", board: { id: "brd_1", title: "Plan" } };
const conv = (over: Partial<Conversation> = {}): Conversation => ({
  channelId: "chn_a", label: "eng", kind: "room", project: "test", phase: "", needsYou: false, sendTo: "chn_a", messages: [card], lastSeq: 4, earlier: false,
  sessionIds: [], sessions: [], canJoin: false, canLeave: true, callIn: [], openAsks: [], session: "", newBoard: true, ...over,
});
function serve(c: Conversation) {
  const created: unknown[] = [];
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(c)),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.get("/app/user/api/boards/:id", ({ params }) => HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly: false })),
    http.post("/app/user/api/boards", async ({ request }) => {
      created.push(await request.json());
      return HttpResponse.json({ board: "brd_9", channel: "chn_a" }, { status: 201 });
    }),
  );
  return created;
}
const withBoards = { ...boot, boards: true };

describe("boards in a conversation", () => {
  it("shows a board's card: title, who and when, the thumbnail, linking to the board", async () => {
    serve(conv());
    renderApp(withBoards, "/app/next/c/chn_a");
    const c = (await screen.findByText(/Plan/)).closest("[data-board]") as HTMLElement;
    expect(within(c).getByText(/alice/)).toBeInTheDocument();
    expect(within(c).getByRole("img", { name: "Plan" })).toHaveAttribute("src", "/app/user/boards/brd_1/thumb.png");
    expect(within(c).getByRole("link", { name: "Open" })).toHaveAttribute("href", "/app/next/boards/brd_1");
  });

  it("starts a whiteboard with a title and opens it", async () => {
    const created = serve(conv());
    const user = userEvent.setup();
    renderApp(withBoards, "/app/next/c/chn_a");
    await user.click(await screen.findByRole("button", { name: "New whiteboard" }));
    await user.type(await screen.findByLabelText("Title"), "Sketch");
    await user.click(screen.getByRole("button", { name: "Start" }));
    await waitFor(() => expect(created).toEqual([{ channel: "chn_a", title: "Sketch" }]));
  });

  it("offers New whiteboard only where the viewer may start one", async () => {
    serve(conv({ newBoard: false }));
    renderApp(withBoards, "/app/next/c/chn_a");
    await screen.findByText(/Plan/);
    expect(screen.queryByRole("button", { name: "New whiteboard" })).toBeNull();
  });
});
