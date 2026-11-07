import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { useState } from "react";
import { theme } from "../../theme";
import { QuietGrid, type GridColumn, type GridErrors } from "./QuietGrid";

type Res = { name: string; kind: string; ref: string };
const columns: GridColumn<Res>[] = [
  { key: "name", label: "Name" },
  { key: "kind", label: "Kind", choices: ["repo", "doc", "tracker"] },
  { key: "ref", label: "Ref", mono: true, placeholder: "owner/repo, URL, …" },
];
const two: Res[] = [{ name: "cove", kind: "repo", ref: "aethons-tools/cove" }, { name: "board", kind: "tracker", ref: "linear:COV" }];

// changed records each onChange; last() is the rows after the latest.
const changed = vi.fn<(rows: Res[]) => void>();
const last = () => changed.mock.lastCall?.[0] ?? [];
function Grid({ initial = two, max, errors }: { initial?: Res[]; max?: number; errors?: GridErrors<Res> }) {
  const [rows, setRows] = useState(initial);
  return <QuietGrid columns={columns} rows={rows} onChange={(r) => { setRows(r); changed(r); }} blank={() => ({ name: "", kind: "repo", ref: "" })} noun="resource" max={max} errors={errors} />;
}
// cell is a cell's button out of editing: named by its column, row and value.
const cell = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}: `) });
const show = (ui: React.ReactElement) => render(<MantineProvider theme={theme} env="test">{ui}</MantineProvider>);

describe("QuietGrid", () => {
  it("is a plain table until a cell is clicked", async () => {
    const user = userEvent.setup();
    show(<Grid />);
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(cell("Name, row 1")).toHaveAccessibleName("Name, row 1: cove");
    await user.click(cell("Name, row 1"));
    const box = screen.getByRole("textbox", { name: "Name, row 1" });
    expect(box).toHaveFocus();
    await user.clear(box);
    await user.type(box, "jam");
    expect(last()[0]!.name).toBe("jam");
  });

  it("moves on with Tab and Enter, and Esc leaves the cell", async () => {
    const user = userEvent.setup();
    show(<Grid />);
    await user.click(cell("Name, row 1"));
    await user.tab();
    const kind = screen.getByRole("combobox", { name: "Kind, row 1" });
    expect(kind).toHaveFocus();
    expect(screen.queryByRole("textbox", { name: "Name, row 1" })).toBeNull();
    await user.selectOptions(kind, "doc");
    expect(last()[0]!.kind).toBe("doc");
    await user.tab();
    expect(screen.getByRole("textbox", { name: "Ref, row 1" })).toHaveFocus();
    await user.tab(); // past the row's end: the next row's first cell
    expect(screen.getByRole("textbox", { name: "Name, row 2" })).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByRole("textbox", { name: "Ref, row 1" })).toHaveFocus();
    await user.keyboard("{Enter}"); // down the column
    expect(screen.getByRole("textbox", { name: "Ref, row 2" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(cell("Ref, row 2")).toHaveFocus();
  });

  it("adds from the bottom row, editing the new item, until max", async () => {
    const user = userEvent.setup();
    show(<Grid max={3} />);
    await user.click(screen.getByRole("button", { name: "+ new resource…" }));
    expect(last()).toHaveLength(3);
    const box = screen.getByRole("textbox", { name: "Name, row 3" });
    expect(box).toHaveFocus();
    await user.type(box, "manual");
    expect(last()[2]).toEqual({ name: "manual", kind: "repo", ref: "" });
    expect(screen.queryByRole("button", { name: "+ new resource…" })).toBeNull();
  });

  it("asks for a choice when the new row has none", async () => {
    const user = userEvent.setup();
    show(<QuietGrid columns={columns} rows={[{ name: "x", kind: "", ref: "" }]} onChange={changed} blank={() => two[0]!} noun="resource" />);
    await user.click(cell("Kind, row 1"));
    const kind = screen.getByRole("combobox", { name: "Kind, row 1" });
    expect(kind).toHaveValue("");
    await user.selectOptions(kind, "repo");
    expect(last()[0]!.kind).toBe("repo");
  });

  it("leaves the last cell for the add row on Tab", async () => {
    const user = userEvent.setup();
    show(<Grid />);
    await user.click(cell("Ref, row 2"));
    await user.tab();
    expect(screen.getByRole("button", { name: "+ new resource…" })).toHaveFocus();
  });

  it("removes a row", async () => {
    const user = userEvent.setup();
    show(<Grid />);
    await user.click(screen.getByRole("button", { name: "Remove row 1" }));
    expect(last()).toEqual([two[1]]);
    expect(cell("Name, row 1")).toHaveAccessibleName("Name, row 1: board");
    expect(screen.getByRole("button", { name: "Remove row 1" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Remove row 1" }));
    expect(last()).toEqual([]);
    expect(screen.getByRole("button", { name: "+ new resource…" })).toHaveFocus();
  });

  it("shows a cell's error at the cell", async () => {
    const user = userEvent.setup();
    show(<Grid errors={[undefined, { ref: "Not a repo or URL" }]} />);
    expect(cell("Ref, row 2")).toHaveAccessibleDescription("Not a repo or URL");
    expect(cell("Ref, row 2").closest("td")).toHaveAttribute("data-error", "true");
    expect(cell("Ref, row 1").closest("td")).not.toHaveAttribute("data-error");
    await user.click(cell("Ref, row 2"));
    const box = screen.getByRole("textbox", { name: "Ref, row 2" });
    expect(box).toHaveAttribute("aria-invalid", "true");
    expect(box).toHaveAccessibleDescription("Not a repo or URL");
  });

  it("shows a fixed-choice column's ▾, and an empty cell's placeholder", () => {
    show(<Grid initial={[{ name: "x", kind: "doc", ref: "" }]} />);
    expect(cell("Kind, row 1").querySelector(".caret")).toHaveTextContent("▾");
    expect(cell("Name, row 1").querySelector(".caret")).toBeNull();
    expect(cell("Ref, row 1")).toHaveTextContent("owner/repo, URL, …");
  });

  it("has no editing, add or remove when read-only", () => {
    show(<QuietGrid columns={columns} rows={two} onChange={() => {}} blank={() => two[0]!} noun="resource" readOnly />);
    expect(screen.getByText("cove")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
