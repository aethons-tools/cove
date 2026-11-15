import type React from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import type { AgentItem, ChannelRow, RailResponse, WorkRow } from "../api/types";
import { card } from "../test/card";
import { RailSections } from "./RailSections";

const row = (id: string, label: string, over: Partial<ChannelRow> = {}): ChannelRow => ({
  id, label, project: "p", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const work = (key: string, over: Partial<WorkRow> = {}): WorkRow => ({
  key, title: `Title of ${key}`, log: `chn_${key}`, worker: { id: `ses_${key}`, label: key.toLowerCase(), state: "waiting" }, ...over,
});
const agent = (session: string, name: string): AgentItem => ({ session, ssh: false, card: card({ session, name }) });

const rail = (over: Partial<RailResponse> = {}): RailResponse => ({
  groups: [], unreadTotal: 0, history: [],
  me: [
    row("chn_ed", "editor", { kind: "dm", card: card({ name: "editor", pill: "working", state: "working" }) }),
    row("chn_ops", "ops", { unread: 2 }),
    row("chn_bob", "bob", { kind: "dm" }),
    row("chn_42", "ACME-42", { kind: "work", needsYou: true }),
  ],
  work: [work("ACME-42", { unread: 1 }), work("ACME-51")],
  agents: [agent("ses_ed", "editor"), agent("ses_rb", "review-bot")],
  ...over,
});

type Props = Partial<React.ComponentProps<typeof RailSections>>;
const show = (r: RailResponse, p: Props = {}) => {
  const props = {
    rowProps: (id: string) => ({ component: "button", "data-row": id }),
    sessionProps: (s: string) => ({ component: "button", "data-card": s }),
    openSession: vi.fn(), onReorder: vi.fn(), ...p,
  };
  render(<MantineProvider env="test"><RailSections rail={r} {...props} /></MantineProvider>);
  return props;
};
const section = (name: string) => screen.getByRole("region", { name });
const at = (name: string, id: string) => section(name).querySelector<HTMLElement>(`[data-row="${id}"], [data-card="${id}"]`)!;
const rowsOf = (name: string) => [...section(name).querySelectorAll("[data-row]")].map((e) => e.getAttribute("data-row"));

describe("RailSections", () => {
  it("shows Me as compact rows: label, the kind or the agent's state, the needs-you dot, and unread", () => {
    show(rail(), { active: "chn_ops" });
    const me = section("Me");
    expect(rowsOf("Me")).toEqual(["chn_ed", "chn_ops", "chn_bob", "chn_42"]);
    const ed = at("Me", "chn_ed");
    expect(within(ed).getByText("working")).toBeInTheDocument();
    expect(ed.querySelector(".sc")).toBeNull(); // a compact row, not the card
    expect(within(at("Me", "chn_bob")).getByText("DM")).toBeInTheDocument();
    expect(within(at("Me", "chn_ops")).getByText("room")).toBeInTheDocument();
    expect(within(at("Me", "chn_42")).getByLabelText("Needs you")).toBeInTheDocument();
    expect(within(me).getAllByLabelText("Needs you")).toHaveLength(1);
    expect(within(at("Me", "chn_ops")).getByLabelText("2 unread")).toBeInTheDocument();
    expect(at("Me", "chn_ops")).toHaveAttribute("data-active", "true");
    expect(at("Me", "chn_bob")).not.toHaveAttribute("data-active");
  });

  it("filters Me to the rows with unread or needing you, and back", async () => {
    show(rail());
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    expect(rowsOf("Me")).toEqual(["chn_ops", "chn_42"]);
    await userEvent.click(screen.getByRole("radio", { name: "All" }));
    expect(rowsOf("Me")).toHaveLength(4);
  });

  it("says so when Unread leaves nothing", async () => {
    show(rail({ me: [row("chn_bob", "bob")] }));
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    expect(within(section("Me")).getByText("Nothing unread.")).toBeInTheDocument();
  });

  it("shows Work rows with key, title, the worker and its state, and unread when present", async () => {
    const p = show(rail());
    expect(rowsOf("Work")).toEqual(["chn_ACME-42", "chn_ACME-51"]);
    const r42 = at("Work", "chn_ACME-42");
    expect(r42).toHaveTextContent("ACME-42");
    expect(r42).toHaveTextContent("Title of ACME-42");
    expect(within(r42).getByLabelText("1 unread")).toBeInTheDocument();
    expect(within(at("Work", "chn_ACME-51")).queryByLabelText(/unread/)).toBeNull();
    const worker = within(r42).getByRole("button", { name: "Open acme-42's session view" });
    expect(worker).toHaveTextContent("acme-42 · waiting");
    await userEvent.click(worker);
    expect(p.openSession).toHaveBeenCalledWith("ses_ACME-42");
  });

  it("shows Agents as status cards that open the session view", () => {
    show(rail(), { activeSession: "ses_rb" });
    const cards = [...section("Agents").querySelectorAll("[data-card]")];
    expect(cards.map((c) => c.getAttribute("data-card"))).toEqual(["ses_ed", "ses_rb"]);
    expect(cards[0]!.querySelector(".sc")).not.toBeNull();
    expect(cards[1]).toHaveAttribute("data-active", "true");
  });

  it("leaves out an empty Work or Agents, and History when it has no rows", () => {
    show(rail({ work: [], agents: [] }));
    expect(screen.queryByRole("region", { name: "Work" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Agents" })).toBeNull();
    expect(screen.queryByText(/History/)).toBeNull();
  });

  it("keeps History collapsed at the bottom until opened", async () => {
    show(rail({ history: [row("chn_old", "old talk", { kind: "dm" })] }));
    const toggle = screen.getByRole("button", { name: "History (before the upgrade)" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("old talk")).toBeNull();
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("old talk")).toBeInTheDocument();
  });

  it("reorders Me by drag and drop, saving the whole section's order", () => {
    const p = show(rail());
    fireEvent.dragStart(at("Me", "chn_ed"));
    fireEvent.dragOver(at("Me", "chn_bob"));
    fireEvent.drop(at("Me", "chn_bob"));
    expect(p.onReorder).toHaveBeenCalledWith("me", ["chn_ops", "chn_bob", "chn_ed", "chn_42"]);
  });

  it("reorders within the whole section while filtered to Unread", async () => {
    const p = show(rail());
    await userEvent.click(screen.getByRole("radio", { name: "Unread" }));
    fireEvent.dragStart(at("Me", "chn_42"));
    fireEvent.drop(at("Me", "chn_ops"));
    expect(p.onReorder).toHaveBeenCalledWith("me", ["chn_ed", "chn_42", "chn_ops", "chn_bob"]);
  });

  it("reorders Agents by drag and drop", () => {
    const p = show(rail());
    fireEvent.dragStart(at("Agents", "ses_rb"));
    fireEvent.drop(at("Agents", "ses_ed"));
    expect(p.onReorder).toHaveBeenCalledWith("agents", ["ses_rb", "ses_ed"]);
  });

  it("reorders from the keyboard with the grip's arrow keys", () => {
    const p = show(rail());
    fireEvent.keyDown(within(section("Me")).getByRole("button", { name: "Move ops" }), { key: "ArrowUp" });
    expect(p.onReorder).toHaveBeenLastCalledWith("me", ["chn_ops", "chn_ed", "chn_bob", "chn_42"]);
    fireEvent.keyDown(within(section("Agents")).getByRole("button", { name: "Move editor" }), { key: "ArrowDown" });
    expect(p.onReorder).toHaveBeenLastCalledWith("agents", ["ses_rb", "ses_ed"]);
    vi.mocked(p.onReorder).mockClear();
    fireEvent.keyDown(within(section("Me")).getByRole("button", { name: "Move ACME-42" }), { key: "ArrowDown" }); // already last
    expect(p.onReorder).not.toHaveBeenCalled();
  });
});
