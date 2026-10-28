import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, SessionRolesResponse, StartSessionRequest } from "../api/types";

const roles: SessionRolesResponse = { roles: [
  { project: "test", role: "pair", full: "" },
  { project: "test", role: "ops", full: "all 1 running" },
] };

describe("New session", () => {
  it("is hidden when the viewer may not start sessions", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })));
    renderApp(boot);
    expect(await screen.findByRole("button", { name: "New message" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New session" })).toBeNull();
  });

  it("picks a role, starts it with a first message, and lands in its conversation", async () => {
    let sent: StartSessionRequest | undefined;
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/session-roles", () => HttpResponse.json(roles)),
      http.post("/app/user/api/sessions", async ({ request }) => {
        sent = await request.json() as StartSessionRequest;
        return HttpResponse.json({ id: "ses_new", name: "pair-01", channel: "chn_ses" }, { status: 201 });
      }),
      http.get("/app/user/api/conversations/chn_ses", () => HttpResponse.json({
        channelId: "chn_ses", label: "pair-01", kind: "session", project: "test", phase: "raising", needsYou: false, sendTo: "chn_ses",
        messages: [], lastSeq: 0, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: true, callIn: [], openAsks: [], session: "", newBoard: false,
      } satisfies Conversation)),
      http.get("/app/user/api/conversations/chn_ses/presence", () => HttpResponse.json({ sessions: [] })),
    );
    renderApp({ ...boot, newSession: true });
    await userEvent.click(await screen.findByRole("button", { name: "New session" }));
    expect(await screen.findByRole("radio", { name: /ops/ })).toBeDisabled();
    expect(screen.getByText("Full: all 1 running")).toBeInTheDocument();
    const start = screen.getByRole("button", { name: "Start" });
    expect(start).toBeDisabled();
    await userEvent.click(screen.getByRole("radio", { name: "pair" }));
    await userEvent.type(screen.getByRole("textbox", { name: /First message/ }), "fix the build");
    await userEvent.click(start);
    expect(await screen.findByRole("heading", { name: "pair-01" })).toBeInTheDocument();
    expect(sent).toEqual({ project: "test", role: "pair", message: "fix the build" });
  });
});
