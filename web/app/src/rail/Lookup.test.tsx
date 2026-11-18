import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { ChannelRow, Conversation, LookupResponse, RailResponse } from "../api/types";

const row = (id: string, label: string, kind: string): ChannelRow => ({
  id, label, project: "acme", kind, needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "",
});
const rail: RailResponse = { unreadTotal: 0, me: [row("chn_ops", "ops", "room")], work: [], agents: [], history: [] };
const found: LookupResponse = {
  conversations: [
    { row: row("chn_w", "ACME-42", "work"), title: "Fix the flaky login test", finished: "2026-11-12T16:02:00Z", why: "done", match: null },
    { row: row("chn_dm", "acme-42", "dm"), finished: "2026-11-12T16:00:00Z", why: "ended", match: { from: "you", text: "Is the login fix ready for review?" } },
  ],
  sessions: [{ session: "ses_42", label: "acme-42", kind: "ticket", mission: "Fix the flaky login test", ended: "2026-11-12T16:05:00Z", match: null }],
};
const finished: Conversation = {
  channelId: "chn_w", label: "ACME-42", kind: "work", project: "acme", phase: "", needsYou: false, sendTo: "", finished: "done",
  messages: [], lastSeq: 1, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: "", newBoard: false,
};

function serve(asked: string[] = []) {
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
    http.get("/app/user/api/lookup", ({ request }) => {
      asked.push(new URL(request.url).searchParams.get("q") ?? "");
      return HttpResponse.json(found);
    }),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(finished)),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
  );
  return asked;
}

describe("the rail's lookup", () => {
  it("replaces the sections with the results while the box has text", async () => {
    const asked = serve();
    renderApp(boot, "/app/user/");
    const box = await screen.findByRole("textbox", { name: "Look up conversations and sessions" });
    expect(await screen.findByRole("list", { name: "Me" })).toBeInTheDocument();
    await userEvent.type(box, "login");
    await waitFor(() => expect(asked.at(-1)).toBe("login"));
    const convs = await screen.findByRole("list", { name: "Finished conversations" });
    expect(screen.queryByRole("list", { name: "Me" })).not.toBeInTheDocument();
    expect(within(convs).getByText("done")).toBeInTheDocument();
    expect(within(convs).getByText("agent ended")).toBeInTheDocument();
    // The words looked up are highlighted in the matching message.
    const marks = within(convs).getAllByText("login", { selector: "mark" });
    expect(marks.length).toBeGreaterThan(0);
    const sessions = screen.getByRole("list", { name: "Ended sessions" });
    expect(within(sessions).getByText("ticket session")).toBeInTheDocument();
    expect(within(sessions).getByRole("link")).toHaveAttribute("href", "/app/user/s/ses_42");

    await userEvent.click(screen.getByRole("radio", { name: "Sessions" }));
    expect(screen.queryByRole("list", { name: "Finished conversations" })).not.toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Ended sessions" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByRole("list", { name: "Me" })).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Ended sessions" })).not.toBeInTheDocument();
  });

  it("opens a finished conversation read-only", async () => {
    serve();
    renderApp(boot, "/app/user/");
    await userEvent.type(await screen.findByRole("textbox", { name: "Look up conversations and sessions" }), "login");
    const convs = await screen.findByRole("list", { name: "Finished conversations" });
    await userEvent.click(within(convs).getAllByRole("link")[0]!);
    expect(await screen.findByText("This conversation has finished. You can read it but not post to it.")).toBeInTheDocument();
    expect(screen.getByText("Finished · read only")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /^Message / })).not.toBeInTheDocument();
    await waitFor(() => expect(within(convs).getAllByRole("link")[0]).toHaveAttribute("aria-current", "page"));
  });
});
