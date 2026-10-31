import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { theme } from "../../theme";
import { card } from "../../test/card";
import { contextColor, SessionCard } from "./SessionCard";
import { forget } from "./history";

const show = (c: Parameters<typeof SessionCard>[0]["card"], compact = false) =>
  render(<MantineProvider theme={theme} env="test"><SessionCard card={c} compact={compact} /></MantineProvider>);

describe("SessionCard", () => {
  afterEach(forget);

  it("shows the name, the pill (tool in its title), the mission and the sparkline", () => {
    const { container } = show(card({ name: "spider", pill: "working", tool: "Bash", mission: "Ship the card" }));
    expect(screen.getByText("spider")).toBeInTheDocument();
    expect(screen.getByText("working")).toHaveAttribute("title", "working: Bash");
    expect(screen.getByText("Ship the card")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "recent states, now working" })).toBeInTheDocument();
    expect(container.querySelector(".sc-focus")).toBeNull();
  });

  it("shows the focus under the mission", () => {
    const { container } = show(card({ mission: "Ship the card", focus: "Writing its tests" }));
    expect(container.querySelector(".sc-mission + .sc-focus")).toHaveTextContent("Writing its tests");
  });

  it("shows only the counts above 0, each named in its title", () => {
    show(card({ counts: { asks: 1, alarms: 0, previews: 2, processes: 0, subagents: 3 } }));
    expect(screen.getByTitle("1 open ask")).toHaveTextContent("1");
    expect(screen.getByTitle("2 previews")).toHaveTextContent("2");
    expect(screen.getByTitle("3 subagents")).toHaveTextContent("3");
    expect(screen.queryByTitle(/alarm/)).toBeNull();
    expect(screen.queryByTitle(/kept process/)).toBeNull();
  });

  it("has no count row and no context bar with nothing to show", () => {
    const { container } = show(card());
    expect(container.querySelector(".sc-counts")).toBeNull();
    expect(container.querySelector(".sc-ctx")).toBeNull();
  });

  it("fills the context bar, pulsing from 85%", () => {
    const { container, unmount } = show(card({ context: { used: 50_000, window: 200_000 } }));
    expect(screen.getByTitle("context 25% full")).not.toHaveAttribute("data-hot");
    expect((container.querySelector(".sc-ctx-fill") as HTMLElement).style.height).toBe("25%");
    unmount();
    show(card({ context: { used: 180_000, window: 200_000 } }));
    expect(screen.getByTitle("context 90% full")).toHaveAttribute("data-hot");
  });

  it("shades the context bar green, through yellow, to red", () => {
    expect(contextColor(10)).toBe("hsl(120 70% 45%)");
    expect(contextColor(60)).toBe("hsl(60 70% 45%)");
    expect(contextColor(95)).toBe("hsl(0 70% 45%)");
  });
});
