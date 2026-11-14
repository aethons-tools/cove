import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, RecipientsResponse } from "../api/types";

const recipients: RecipientsResponse = { options: [
  { to: "user:usr_bob", label: "bob", kind: "person", project: "test" },
  { to: "user:usr_carol", label: "carol", kind: "person", project: "test" },
  { to: "session:ses_spider", label: "intercom-spider", kind: "agent", project: "test" },
  { to: "chn_cov1", worker: "worker:chn_cov1", label: "COV-1", kind: "work", project: "test" },
  { to: "chn_eng", label: "eng", kind: "room", project: "other" },
] };

describe("New message", () => {
  it("picks a recipient, sends, and lands in the new conversation", async () => {
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/recipients", () => HttpResponse.json(recipients)),
      http.post("/app/user/api/messages", () => HttpResponse.json({ channel: "chn_new", seq: 1 }, { status: 201 })),
      http.get("/app/user/api/conversations/chn_new", () => HttpResponse.json({
        channelId: "chn_new", label: "bob", kind: "chat", project: "test", phase: "", needsYou: false, sendTo: "chn_new",
        messages: [{ from: "alice", body: "<p>hello bob</p>", raw: "hello bob", plain: false, at: "", time: new Date().toISOString(), mine: true, seq: 1, system: false, sysText: "", card: "" }],
        lastSeq: 1, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: "", newBoard: false,
      } satisfies Conversation)),
      http.get("/app/user/api/conversations/chn_new/presence", () => HttpResponse.json({ sessions: [] })),
      http.post("/app/user/api/conversations/chn_new/read", () => new HttpResponse(null, { status: 204 })),
    );
    renderApp(boot);
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.type(await screen.findByPlaceholderText("Search people, agents, work, rooms…"), "bo");
    expect(screen.queryByText("eng")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /bob/ }));
    expect(await screen.findByText("to bob")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message bob" }), "hello bob");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    expect(await screen.findByRole("heading", { name: "bob" })).toBeInTheDocument(); // the conversation view
    expect(screen.getByText("hello bob")).toBeInTheDocument();
  });

  it("ticks several people for a group DM", async () => {
    let sent: unknown;
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/recipients", () => HttpResponse.json(recipients)),
      http.post("/app/user/api/messages", async ({ request }) => {
        sent = await request.json();
        return HttpResponse.json({ error: "stop here", code: "internal" }, { status: 500 });
      }),
    );
    renderApp(boot);
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.click(await screen.findByRole("checkbox", { name: "Add bob to a group DM" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Add carol to a group DM" }));
    expect(screen.queryByRole("checkbox", { name: "Add eng to a group DM" })).toBeNull(); // people only
    await userEvent.click(screen.getByRole("button", { name: "Message bob, carol" }));
    expect(await screen.findByText("to bob, carol")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message bob, carol" }), "standup?");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await screen.findByText("stop here");
    expect(sent).toMatchObject({ to: "chat:user:usr_bob,user:usr_carol", body: "standup?" });
  });

  it("offers a unit of work's log and its worker; with no worker, says Jam's answer", async () => {
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/recipients", () => HttpResponse.json(recipients)),
      http.post("/app/user/api/messages", () => HttpResponse.json({ error: "no agent is working on this now, so the message was not sent", code: "no-worker" }, { status: 409 })),
    );
    const { router } = renderApp(boot);
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    expect(await screen.findByRole("button", { name: "Log of COV-1" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Worker of COV-1" }));
    expect(router.state.location.search).toContain("to=worker%3Achn_cov1");
    expect(await screen.findByText("to the worker of COV-1")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message the worker of COV-1" }), "quick one");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    expect(await screen.findByText("no agent is working on this now, so the message was not sent")).toBeInTheDocument();
  });
});
