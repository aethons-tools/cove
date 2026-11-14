import { afterEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { ActivityRow, SessionActivity, SessionHead, ThinkingEvent } from "../api/types";
import { card } from "../test/card";

const head: SessionHead = {
  id: "ses_1", label: "pair", kind: "personal", ticket: "", role: "pair", project: "test", owner: "alice",
  state: { label: "pair", text: "is idle", tool: "", class: "idle" }, since: "4m", openAsks: 0,
  cost: "", turnCost: "", tokens: "", context: "", contextTip: "", uptime: "1h", mission: "", ssh: "",
  canRelease: true, canRestart: false, canEnd: false, canUpgrade: false, ops: true,
  canInterrupt: false, canCompact: true, canClear: true, restartLabel: "Restart", upgradeState: "", channel: "chn_home", card: card(),
};

const row = (over: Partial<ActivityRow>): ActivityRow => ({
  from: "alice", body: "", raw: "", plain: false, at: "09:00", time: "2026-11-14T09:00:00Z", mine: false, seq: 1,
  system: false, sysText: "", card: "", id: "m1", conversation: { id: "chn_eng", kind: "room", label: "eng" }, out: false, ...over,
});

const t0 = Date.parse("2026-11-14T09:00:00Z");
const activity = (over: Partial<SessionActivity> = {}): SessionActivity => ({
  messages: [
    row({ id: "m1", seq: 1, body: "<p>hello pair</p>", raw: "hello pair", mine: true }),
    row({ id: "m2", seq: 2, from: "pair", body: "<p>hi alice</p>", raw: "hi alice", time: "2026-11-14T09:02:00Z", out: true }),
    row({ id: "m3", seq: 3, body: "<p>a word, privately</p>", raw: "a word, privately", time: "2026-11-14T09:05:00Z", mine: true,
      conversation: { id: "chn_dm", kind: "dm", label: "alice, pair" } }),
  ],
  pageFirst: "m1", pageLast: "m3", earlier: false, later: false,
  turns: [
    { stream: "s1", turn: 1, start: t0 + 30_000, end: t0 + 90_000, after: 0, before: 3, events: 2 },
    { stream: "s1", turn: 2, start: t0 + 6 * 60_000, end: t0 + 7 * 60_000, after: 2, before: 5, events: 2 },
  ],
  limited: [], sendTo: "session:ses_1", ended: false, label: "pair", ...over,
});

const thought = (seq: number, text: string): ThinkingEvent => ({
  stream: "s1", seq, turn: 1, at: t0 + 30_000 + seq, kind: "text", tool: "", toolUseId: "", text, detail: "", detailTruncated: 0,
  isError: false, truncatedBytes: 0, costUsd: 0, durationMs: 0, claudeSession: "",
});

// serve answers the session view's routes; it records the thinking pages
// asked for and the bodies posted.
function serve(a: SessionActivity, h: SessionHead | null = head) {
  const seen = { thinking: [] as string[], sent: [] as unknown[], resumed: [] as string[] };
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/sessions/:id", () =>
      h ? HttpResponse.json(h) : HttpResponse.json({ error: "no such session", code: "not_found" }, { status: 404 })),
    http.get("/app/user/api/sessions/:id/activity", () => HttpResponse.json(a)),
    http.get("/app/user/api/sessions/:id/thinking", ({ request }) => {
      seen.thinking.push(new URL(request.url).search);
      return HttpResponse.json({ events: [thought(1, "I should greet back"), thought(2, "done")], before: 0 });
    }),
    http.post("/app/user/api/messages", async ({ request }) => {
      seen.sent.push(await request.json());
      return HttpResponse.json({ channel: "chn_dm", seq: 9 }, { status: 201 });
    }),
    http.post("/app/user/api/conversations/:id/resume", ({ params }) => {
      seen.resumed.push(params.id as string);
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return seen;
}

afterEach(() => localStorage.clear());

describe("the session view", () => {
  it("shows the activity oldest first, each message with its conversation as a link", async () => {
    serve(activity());
    renderApp(boot, "/app/user/s/ses_1");
    const list = await screen.findByRole("log", { name: "Activity" });
    const texts = within(list).getAllByText(/hello pair|hi alice|a word, privately/).map((n) => n.textContent);
    expect(texts).toEqual(["hello pair", "hi alice", "a word, privately"]);
    expect(within(list).getAllByRole("link", { name: "eng" })[0]).toHaveAttribute("href", "/app/user/c/chn_eng");
    expect(within(list).getByRole("link", { name: "alice, pair" })).toHaveAttribute("href", "/app/user/c/chn_dm");
    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
  });

  it("folds each turn's thinking between the messages, collapsed until opened", async () => {
    const seen = serve(activity());
    const user = userEvent.setup();
    renderApp(boot, "/app/user/s/ses_1");
    const list = await screen.findByRole("log", { name: "Activity" });
    const folds = within(list).getAllByRole("button", { name: /^Thinking · turn/ });
    expect(folds).toHaveLength(2);
    // Turn 1 sits between hello (09:00) and the reply (09:02); turn 2 after the dm.
    const order = [...list.querySelectorAll("[data-activity]")].map((n) => n.getAttribute("data-activity"));
    expect(order).toEqual(["m1", "turn:s1:1", "m2", "m3", "turn:s1:2"]);
    expect(folds[0]).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("I should greet back")).toBeNull();
    expect(seen.thinking).toEqual([]);
    await user.click(folds[0]!);
    expect(await within(list).findByText("I should greet back")).toBeInTheDocument();
    expect(seen.thinking).toEqual(["?stream=s1&after=0&before=3"]);
    expect(folds[0]).toHaveAttribute("aria-expanded", "true");
  });

  it("sends a dm to the session from its composer", async () => {
    const seen = serve(activity());
    const user = userEvent.setup();
    renderApp(boot, "/app/user/s/ses_1");
    await user.type(await screen.findByRole("textbox", { name: "Message pair" }), "are you done?");
    await user.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(seen.sent).toEqual([{ to: "session:ses_1", body: "are you done?" }]));
  });

  it("says where agents stopped waking each other, with Resume", async () => {
    const seen = serve(activity({ limited: [{ id: "chn_eng", kind: "room", label: "eng" }] }));
    const user = userEvent.setup();
    renderApp(boot, "/app/user/s/ses_1");
    const note = await screen.findByRole("status", { name: "Agents stopped waking each other in eng" });
    await user.click(within(note).getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(seen.resumed).toEqual(["chn_eng"]));
  });

  it("opens from an agent's name in the rail", async () => {
    serve(activity());
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [{ bucket: "agents", title: "Agents", rows: [{
      id: "chn_dm", label: "pair", project: "test", kind: "dm", needsYou: false, phase: "live", unread: 0, openAsks: 0, lastSeq: 3, selected: false,
      mission: "", session: "", ssh: false, sessionView: "chn_home", card: card({ session: "ses_1", name: "pair" }),
    }] }] })));
    const user = userEvent.setup();
    const { router } = renderApp(boot, "/app/user/");
    await user.click(await screen.findByRole("button", { name: "Open pair's session view" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/app/user/s/ses_1"));
    expect(await screen.findByRole("log", { name: "Activity" })).toBeInTheDocument();
  });

  it("keeps the activity of an ended session, without the header, pane or composer", async () => {
    serve(activity({ ended: true, sendTo: "" }), null);
    renderApp(boot, "/app/user/s/ses_1");
    expect(await within(await screen.findByRole("log", { name: "Activity" })).findByText("hi alice")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Session" })).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText(/This session has ended/)).toBeInTheDocument();
  });
});
