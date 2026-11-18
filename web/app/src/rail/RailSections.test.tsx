import type React from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import type { AgentItem, ChannelRow, RailResponse, WorkRow } from "../api/types";
import { card } from "../test/card";
import { RailSections } from "./RailSections";

const row = (id: string, label: string, over: Partial<ChannelRow> = {}): ChannelRow => ({
  id, label, project: "p", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const work = (key: string, over: Partial<WorkRow> = {}): WorkRow => ({
  key, title: `Title of ${key}`, log: `chn_${key}`, worker: { id: `ses_${key}`, label: key.toLowerCase(), state: "waiting" }, state: "in-progress", ...over,
});
const agent = (session: string, name: string): AgentItem => ({ session, ssh: false, card: card({ session, name }) });

const rail = (over: Partial<RailResponse> = {}): RailResponse => ({
  unreadTotal: 0, history: [],
  me: [
    row("chn_ed", "editor", { kind: "dm", session: "ses_ed", card: card({ name: "editor", state: "working" }) }),
    row("chn_ops", "ops", { unread: 2 }),
    row("chn_bob", "bob", { kind: "dm" }),
    row("chn_42", "ACME-42", { kind: "work", needsYou: true }),
  ],
  work: [work("ACME-42", { unread: 1 }), work("ACME-51")],
  agents: [agent("ses_ed", "editor"), agent("ses_rb", "review-bot")],
  ...over,
});

type Props = Partial<React.ComponentProps<typeof RailSections>>;
// show renders the sections with buttons for items, and returns the
// callbacks and the saved orders (PUT api/rail/order).
const show = (r: RailResponse, p: Props = {}, status = 204) => {
  const saved: unknown[] = [];
  server.use(http.put("/app/user/api/rail/order", async ({ request }) => {
    saved.push(await request.json());
    return status === 204 ? new HttpResponse(null, { status }) : HttpResponse.json({ error: "no", code: "bad_request" }, { status });
  }));
  const props = {
    rowProps: (c: ChannelRow) => ({ component: "button", "data-row": c.id }),
    workProps: (w: WorkRow) => ({ component: "button", "data-row": w.log }),
    cardProps: (a: AgentItem) => ({ "data-card": a.session }),
    onWorker: vi.fn(), ...p,
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MantineProvider env="test">
      <Notifications />
      <QueryClientProvider client={client}><RailSections rail={r} {...props} /></QueryClientProvider>
    </MantineProvider>,
  );
  return { ...props, saved };
};
const list = (name: string) => screen.getByRole("list", { name });
const at = (name: string, id: string) => list(name).querySelector<HTMLElement>(`[data-row="${id}"], [data-card="${id}"]`)!;
const ids = (name: string) => [...list(name).querySelectorAll("[data-row], [data-card]")].map((e) => e.getAttribute("data-row") ?? e.getAttribute("data-card"));
const item = (name: string, id: string) => at(name, id).closest("li")!;
const grip = (name: string, id: string) => within(item(name, id)).getByRole("button", { name: "Reorder" });

describe("RailSections", () => {
  it("shows the sections in order, History last", () => {
    show(rail({ history: [row("chn_old", "old talk")] }));
    expect(screen.getAllByRole("heading").map((h) => h.textContent)).toEqual(["Me", "Work", "Agents", "History (before the upgrade)"]);
  });

  it("shows Me as compact rows: label, the kind or the agent's state, the needs-you dot, and unread", () => {
    show(rail(), { active: "chn_ops", marker: (s) => (s === "ses_ed" ? <span aria-label="mark">▣</span> : null) });
    expect(ids("Me")).toEqual(["chn_ed", "chn_ops", "chn_bob", "chn_42"]);
    expect(within(at("Me", "chn_ed")).getByText("working")).toBeInTheDocument();
    expect(at("Me", "chn_ed").querySelector(".sc")).toBeNull(); // a compact row, not the card
    expect(within(at("Me", "chn_ed")).getByLabelText("mark")).toBeInTheDocument();
    expect(within(at("Me", "chn_bob")).getByText("DM")).toBeInTheDocument();
    expect(within(at("Me", "chn_ops")).getByText("room")).toBeInTheDocument();
    expect(within(at("Me", "chn_42")).getByLabelText("Needs you")).toBeInTheDocument();
    expect(within(list("Me")).getAllByLabelText("Needs you")).toHaveLength(1);
    expect(within(at("Me", "chn_ops")).getByLabelText("2 unread")).toBeInTheDocument();
    expect(at("Me", "chn_ops")).toHaveAttribute("data-active", "true");
    expect(at("Me", "chn_bob")).not.toHaveAttribute("data-active");
  });

  it("filters Me to the rows with unread or needing you, and back", async () => {
    show(rail());
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    expect(ids("Me")).toEqual(["chn_ops", "chn_42"]);
    await userEvent.click(screen.getByRole("radio", { name: "All" }));
    expect(ids("Me")).toHaveLength(4);
  });

  it("says so when Unread leaves nothing", async () => {
    show(rail({ me: [row("chn_bob", "bob")] }));
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    expect(screen.getByText("Nothing unread.")).toBeInTheDocument();
  });

  it("shows Work rows with key, title, the worker and its state, and unread when present", async () => {
    const p = show(rail());
    expect(ids("Work")).toEqual(["chn_ACME-42", "chn_ACME-51"]);
    expect(at("Work", "chn_ACME-42")).toHaveTextContent("ACME-42 Title of ACME-42");
    expect(within(at("Work", "chn_ACME-42")).getByLabelText("1 unread")).toBeInTheDocument();
    expect(within(at("Work", "chn_ACME-51")).queryByLabelText(/unread/)).toBeNull();
    const worker = within(item("Work", "chn_ACME-42")).getByRole("button", { name: "Open acme-42's session view" });
    expect(worker).toHaveTextContent("acme-42 · waiting");
    await userEvent.click(worker);
    expect(p.onWorker).toHaveBeenCalledWith(expect.objectContaining({ key: "ACME-42" }));
  });

  it("marks the Work row whose unit's page is open", () => {
    show(rail(), { activeWork: "ACME-51" });
    expect(at("Work", "chn_ACME-51")).toHaveAttribute("data-active");
    expect(at("Work", "chn_ACME-42")).not.toHaveAttribute("data-active");
  });

  it("shows Agents as status cards, the open one marked", () => {
    show(rail(), { activeSession: "ses_rb" });
    expect(ids("Agents")).toEqual(["ses_ed", "ses_rb"]);
    expect(at("Agents", "ses_ed").querySelector(".sc")).not.toBeNull();
    expect(at("Agents", "ses_ed")).toHaveAccessibleName("editor");
    expect(at("Agents", "ses_rb")).toHaveAttribute("data-active");
    expect(at("Agents", "ses_ed")).not.toHaveAttribute("data-active");
  });

  it("leaves out an empty Work or Agents, and History when it has no rows", () => {
    show(rail({ work: [], agents: [] }));
    expect(screen.queryByRole("list", { name: "Work" })).toBeNull();
    expect(screen.queryByRole("list", { name: "Agents" })).toBeNull();
    expect(screen.queryByText(/History/)).toBeNull();
  });

  it("keeps History collapsed at the bottom until opened", async () => {
    show(rail({ history: [row("chn_old", "old talk", { kind: "dm" })] }));
    const toggle = screen.getByRole("button", { name: "History (before the upgrade)" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("old talk")).toBeNull();
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(ids("History")).toEqual(["chn_old"]);
  });

  it("reorders Me by drag and drop, saving the whole section's order", async () => {
    const p = show(rail());
    fireEvent.dragStart(item("Me", "chn_ed"));
    fireEvent.dragOver(item("Me", "chn_bob"));
    fireEvent.drop(item("Me", "chn_bob"));
    await waitFor(() => expect(p.saved).toEqual([{ section: "me", ids: ["chn_ops", "chn_bob", "chn_ed", "chn_42"] }]));
  });

  it("reorders within the whole section while filtered to Unread", async () => {
    const p = show(rail());
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    fireEvent.dragStart(item("Me", "chn_42"));
    fireEvent.drop(item("Me", "chn_ops"));
    await waitFor(() => expect(p.saved).toEqual([{ section: "me", ids: ["chn_ed", "chn_42", "chn_ops", "chn_bob"] }]));
  });

  it("ignores a drop from another section", async () => {
    const p = show(rail());
    fireEvent.dragStart(item("Agents", "ses_rb"));
    fireEvent.drop(item("Me", "chn_ed"));
    fireEvent.drop(item("Agents", "ses_ed"));
    await waitFor(() => expect(p.saved).toEqual([{ section: "agents", ids: ["ses_rb", "ses_ed"] }]));
  });

  it("reorders from the keyboard with the grip's arrow keys", async () => {
    const p = show(rail());
    fireEvent.keyDown(grip("Me", "chn_ops"), { key: "ArrowUp" });
    await waitFor(() => expect(p.saved).toHaveLength(1));
    fireEvent.keyDown(grip("Agents", "ses_ed"), { key: "ArrowDown" });
    await waitFor(() => expect(p.saved).toHaveLength(2));
    fireEvent.keyDown(grip("Me", "chn_42"), { key: "ArrowDown" }); // already last
    expect(p.saved).toEqual([
      { section: "me", ids: ["chn_ops", "chn_ed", "chn_bob", "chn_42"] },
      { section: "agents", ids: ["ses_rb", "ses_ed"] },
    ]);
  });

  it("says so when the order can't be saved", async () => {
    show(rail(), {}, 400);
    fireEvent.keyDown(grip("Me", "chn_ops"), { key: "ArrowUp" });
    expect(await screen.findByText(/Couldn't save the order/)).toBeInTheDocument();
  });
});
