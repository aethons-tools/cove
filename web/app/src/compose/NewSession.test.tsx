import { describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, SessionRolesResponse, StartProgress, StartSessionRequest } from "../api/types";

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
      http.get("/app/user/api/sessions/start-progress", () => HttpResponse.json({})),
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

  it("shows the kit image build a Start waits on, polled while it runs", async () => {
    let finish!: () => void;
    const done = new Promise<void>((r) => { finish = r; });
    let asked = "";
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/session-roles", () => HttpResponse.json(roles)),
      http.get("/app/user/api/sessions/start-progress", ({ request }) => {
        asked = new URL(request.url).search;
        return HttpResponse.json({ step: "building", detail: "step 7/12", since: "2026-11-05T12:00:00Z", age: "2m" } satisfies StartProgress);
      }),
      http.post("/app/user/api/sessions", async () => {
        await done;
        return HttpResponse.json({ error: { code: "conflict", message: "at capacity" } }, { status: 409 });
      }),
    );
    renderApp({ ...boot, newSession: true });
    await userEvent.click(await screen.findByRole("button", { name: "New session" }));
    await userEvent.click(await screen.findByRole("radio", { name: "pair" }));
    expect(screen.queryByText(/Preparing the kit image/)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText("Preparing the kit image — building step 7/12 (2m)")).toBeInTheDocument();
    expect(asked).toBe("?project=test&role=pair");
    finish();
    await waitFor(() => expect(screen.queryByText(/Preparing the kit image/)).toBeNull());
  });
});
