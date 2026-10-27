import { describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { focusManager } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { AskCard, Conversation, MessageRow } from "../api/types";

const card = (over: Partial<AskCard> = {}): AskCard => ({
  id: "ask_1", question: "Merge PR #12?", options: [{ id: "yes", label: "Ship it" }, { id: "no", label: "Hold", detail: "wait for CI" }],
  version: 1, state: "open", answeredBy: "", choice: "", comment: "", reason: "", edited: false, canAnswer: true, for: "", ...over,
});
const row = (c: AskCard, seq = 1): MessageRow => ({
  from: "spider", body: "<p>asked</p>", raw: "asked", plain: false, at: "", time: new Date().toISOString(), mine: false, seq,
  system: false, sysText: "", card: "ask", ask: c,
});
const conv = (cards: AskCard[]): Conversation => ({
  channelId: "chn_a", label: "eng", kind: "room", project: "test", phase: "", needsYou: true, sendTo: "chn_a",
  messages: cards.map((c, i) => row(c, i + 1)), lastSeq: cards.length, earlier: false, sessionIds: [], sessions: [],
  canJoin: false, canLeave: true, callIn: [], openAsks: cards.filter((c) => c.state === "open"), session: "", newBoard: false,
});

function serve(get: () => Conversation, answer?: (body: unknown) => Response) {
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(get())),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.post("/app/user/api/asks/:id/answer", async ({ request }) => (answer ? answer(await request.json()) : new HttpResponse(null, { status: 204 }))),
  );
}

describe("ask cards", () => {
  it("answers with an option, then shows the answered state", async () => {
    let c = card();
    let got: unknown;
    serve(() => conv([c]), (body) => {
      got = body;
      c = card({ state: "answered", answeredBy: "alice", choice: "Hold", comment: "after lunch", canAnswer: false });
      return new HttpResponse(null, { status: 204 });
    });
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("wait for CI")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Comment on Merge PR #12?" }), "after lunch");
    await userEvent.click(screen.getByRole("button", { name: /Hold/ }));
    expect(got).toEqual({ version: 1, choice: "no", comment: "after lunch" });
    expect(await screen.findByText(/alice chose/)).toBeInTheDocument();
    expect(screen.getByText("Hold")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ship it/ })).toBeNull();
  });

  it("refuses Other… without a comment", async () => {
    let posts = 0;
    serve(() => conv([card()]), () => { posts++; return new HttpResponse(null, { status: 204 }); });
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Other…" }));
    expect(await screen.findByText("Write a comment for Other…")).toBeInTheDocument();
    expect(posts).toBe(0);
  });

  it("says why when the ask changed, and shows the new version", async () => {
    let c = card();
    serve(() => conv([c]), () => {
      c = card({ version: 2, question: "Merge PR #12 now?", edited: true });
      return HttpResponse.json({ error: "this ask changed — review the new version", code: "ask_changed" }, { status: 409 });
    });
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: /Ship it/ }));
    expect(await screen.findByText("this ask changed — review the new version")).toBeInTheDocument();
    // (The open-asks panel lists the question too: look in the card.)
    await waitFor(() => expect(within(document.getElementById("ask-ask_1")!).getByText("Merge PR #12 now?")).toBeInTheDocument());
    expect(within(document.getElementById("ask-ask_1")!).getByText("edited")).toBeInTheDocument();
  });

  it("is read-only, and says for whom, when the viewer may not answer", async () => {
    serve(() => conv([card({ canAnswer: false, for: "carol" })]));
    renderApp(boot, "/app/next/c/chn_a");
    await screen.findAllByText("for carol"); // the card's and the open-asks panel's
    expect(within(document.getElementById("ask-ask_1")!).getByText("for carol")).toBeInTheDocument();
    expect(screen.getAllByText("Ship it").every((el) => el.closest("button") === null)).toBe(true);
    expect(screen.queryByRole("textbox", { name: /Comment on/ })).toBeNull();
  });

  it("shows a withdrawn ask with its reason", async () => {
    serve(() => conv([card({ state: "withdrawn", reason: "superseded", canAnswer: false })]));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("withdrawn: superseded")).toBeInTheDocument();
  });

  it("keeps a half-typed comment across a live refresh", async () => {
    serve(() => conv([card()]));
    renderApp(boot, "/app/next/c/chn_a");
    const box = await screen.findByRole("textbox", { name: "Comment on Merge PR #12?" });
    await userEvent.type(box, "half");
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByRole("textbox", { name: "Comment on Merge PR #12?" })).toHaveValue("half");
  });

  it("lists open asks in a panel that jumps to the card", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    serve(() => conv([card(), card({ id: "ask_2", question: "Deploy?", for: "carol", canAnswer: false })]));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("Open asks")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Deploy?" }));
    await waitFor(() => expect(scroll).toHaveBeenCalled());
  });

  it("Discuss starts a reply about the ask in the composer", async () => {
    serve(() => conv([card()]));
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Discuss" }));
    expect(screen.getByRole("textbox", { name: "Message eng" })).toHaveValue("re: Merge PR #12?\n");
  });

  it("takes one answer: the other choices wait while it's sent, and the rail refreshes", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    let posts = 0, railGets = 0;
    serve(() => conv([card()]), () => { posts++; return new HttpResponse(null, { status: 204 }); });
    server.use(
      http.post("/app/user/api/asks/:id/answer", async () => { posts++; await gate; return new HttpResponse(null, { status: 204 }); }),
      http.get("/app/user/api/rail", () => { railGets++; return HttpResponse.json({ groups: [], unreadTotal: 0 }); }),
    );
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: /Hold/ }));
    expect(screen.getByRole("button", { name: /Ship it/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Other…" })).toBeDisabled();
    const before = railGets;
    release();
    await waitFor(() => expect(railGets).toBeGreaterThan(before));
    expect(posts).toBe(1);
  });
});
