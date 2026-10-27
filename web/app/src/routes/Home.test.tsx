import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";

vi.mock("../boards/BoardEditor", () => ({ default: () => <div data-testid="editor" /> }));

// The old Inbox's links (bookmarks, notifications, /me/?c= redirects) land in
// the app.
describe("old Inbox links", () => {
  const serve = () => server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/conversations/:c", ({ params }) => HttpResponse.json({ channelId: params.c, label: "eng", kind: "room", project: "p", phase: "", needsYou: false, sendTo: String(params.c),
      messages: [], lastSeq: 0, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: "", newBoard: false })),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.get("/app/user/api/boards/:id", ({ params }) => HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly: false })),
  );
  it("?c= opens the conversation", async () => {
    serve();
    renderApp(boot, "/app/user/?c=chn_a");
    expect(await screen.findByRole("heading", { name: "eng" })).toBeInTheDocument();
  });
  it("&board= opens the board", async () => {
    serve();
    renderApp(boot, "/app/user/?c=chn_a&board=brd_1");
    expect(await screen.findByTestId("editor")).toBeInTheDocument();
  });
  it("with neither, asks for a conversation", async () => {
    serve();
    renderApp(boot, "/app/user/");
    expect(await screen.findByText("Pick a conversation.")).toBeInTheDocument();
  });
});
