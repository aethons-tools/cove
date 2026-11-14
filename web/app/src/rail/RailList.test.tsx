import type React from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import type { RailGroup } from "../api/types";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { card } from "../test/card";
import { RailList } from "./RailList";

const row = (id: string, label: string, over = {}) => ({
  id, label, project: "p", kind: "studio", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const groups: RailGroup[] = [
  { bucket: "needs-you", title: "Needs you", rows: [row("chn_a", "Ada", { unread: 2, mission: "fixing it" })] },
  { bucket: "channels", title: "Channels", rows: [row("chn_b", "#design")] },
];
const show = (ui: React.ReactElement) => render(<MantineProvider>{ui}</MantineProvider>);

describe("RailList", () => {
  it("renders groups, rows, missions and unread; active marks the row; marker renders", () => {
    show(<RailList groups={groups} active="chn_b" rowProps={() => ({ component: "button" })} marker={(r) => (r.id === "chn_a" ? <span aria-label="mark">▣</span> : null)} />);
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByText("Channels")).toBeInTheDocument();
    expect(screen.getByText("fixing it")).toBeInTheDocument();
    expect(screen.getByLabelText("2 unread")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /#design/ })).toHaveAttribute("data-active", "true");
    expect(screen.getByRole("button", { name: /Ada/ })).not.toHaveAttribute("data-active");
    expect(screen.getAllByLabelText("mark")).toHaveLength(1);
  });

  it("rowProps are spread onto each row", () => {
    show(<RailList groups={groups} rowProps={(r) => ({ component: "button", "data-row": r.id })} />);
    expect(document.querySelectorAll("[data-row]")).toHaveLength(2);
    expect(document.querySelector('[data-row="chn_b"]')).toHaveTextContent("#design");
  });

  it("a dm with an agent is its status card, whose name opens the session view", async () => {
    const openSession = vi.fn();
    const rowOpen = vi.fn();
    const agents: RailGroup[] = [{ bucket: "active", title: "Agents", rows: [
      row("chn_dm", "spider", { kind: "dm", card: card({ name: "spider" }), sessionView: "chn_home" }),
      row("chn_old", "gone", { kind: "dm", card: card({ name: "nameless" }), sessionView: "" }),
    ] }];
    show(<RailList groups={agents} rowProps={() => ({ component: "div", onClick: rowOpen })} openSession={openSession} />);
    await userEvent.click(screen.getByRole("button", { name: "Open spider's session view" }));
    expect(openSession).toHaveBeenCalledWith("chn_home", "ses_1");
    expect(rowOpen).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Open nameless's session view" })).toBeNull(); // not the viewer's to open
  });
});
