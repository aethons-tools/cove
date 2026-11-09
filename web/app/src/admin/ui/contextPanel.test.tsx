import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { theme } from "../../theme";
import { ApiError } from "../../api/client";
import type { ContextPanel as Panel, ContextSave } from "../api/types";
import { setBranch } from "../branch/branch";
import { emptyContext, filledContext } from "../projects/fixtures";
import { ContextPanel } from "./ContextPanel";
import { coreBytes, problemsOf, draftOf } from "./contextDraft";

const role: Panel = { ...filledContext, scope: "role", showResources: false, resources: [], coreBytes: coreBytes(filledContext.core, 0), limits: { ...filledContext.limits, maxResources: 0 } };
const jam: Panel = { ...role, scope: "Jam-wide", budget: 800, limits: { ...role.limits, coreBudget: 800 } };

let onSave: ReturnType<typeof vi.fn<(s: ContextSave) => Promise<unknown>>>;
let onClear: ReturnType<typeof vi.fn<() => Promise<unknown>>>;
const show = (panel: Panel = filledContext) => {
  const user = userEvent.setup();
  render(<MantineProvider theme={theme} env="test"><Notifications /><ContextPanel panel={panel} onSave={onSave} onClear={onClear} /></MantineProvider>);
  return user;
};
// row is an instruction's summary row (its ▸/▾ toggle), by name.
const row = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name.replace(".", "\\.")}`) });
const save = () => screen.getByRole("button", { name: "Save context" });

beforeEach(() => {
  setBranch("jam/test");
  onSave = vi.fn(() => Promise.resolve());
  onClear = vi.fn(() => Promise.resolve());
});
afterEach(() => setBranch(""));

describe("contextDraft", () => {
  it("counts the core as the server does, with the resources pointer", () => {
    expect(coreBytes(filledContext.core, 2)).toBe(filledContext.coreBytes);
    expect(coreBytes("  hi \n", 0)).toBe(2);
  });

  it("finds bad names, duplicates, missing read-whens and the reserved resources.md", () => {
    const d = draftOf(filledContext);
    d.leaves[1]!.name = "release.md";
    d.leaves.push({ key: 99, name: "resources.md", readWhen: " ", body: "", open: false });
    d.leaves.push({ key: 98, name: "Bad", readWhen: "x".repeat(161), body: "", open: false });
    expect(problemsOf(d, filledContext)).toEqual({
      "leaves[1].name": "Name already used",
      "leaves[2].name": "Reserved for the resources list", "leaves[2].readWhen": "Required",
      "leaves[3].name": "Lowercase a–z, 0–9, . _ or -, ending .md", "leaves[3].readWhen": "Over 160 bytes",
    });
    expect(problemsOf({ ...d, leaves: d.leaves.slice(0, 1) }, { ...filledContext, showResources: false })).toEqual({});
  });
});

describe("ContextPanel", () => {
  it("shows the context read-only: rendered markdown, collapsed rows, resources", async () => {
    const user = show();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("user-visible").tagName).toBe("STRONG");
    expect(screen.getByText("Core instructions")).toBeInTheDocument();
    expect(screen.getByText("275 / 1,200 bytes")).toBeInTheDocument();
    expect(screen.getByText("includes the one-line resources pointer")).toBeInTheDocument();
    expect(row("release.md")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("heading", { name: "Cutting a release" })).toBeNull();
    expect(screen.getByText("aethons-tools/cove")).toBeInTheDocument();
    expect(screen.queryByText("+ new instruction…")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save context" })).toBeNull();
    await user.click(row("release.md"));
    expect(row("release.md")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("heading", { name: "Cutting a release" })).toBeInTheDocument();
    expect(screen.getByText("25 / 160 bytes")).toBeInTheDocument();
    await user.click(row("release.md"));
    expect(screen.queryByRole("heading", { name: "Cutting a release" })).toBeNull();
  });

  it("offers no Edit off a branch", () => {
    setBranch("");
    show();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.getByRole("button", { name: "View as YAML" })).toBeInTheDocument();
  });

  it("edits the whole card, and Cancel drops the changes", async () => {
    const user = show();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByText("Editing")).toBeInTheDocument();
    const core = screen.getByRole("textbox", { name: "Core instructions" });
    await user.clear(core);
    await user.type(core, "Be kind.");
    expect(screen.getByText("Ready to save")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("user-visible")).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("adds an instruction and a resource, and saves the whole context in one request", async () => {
    const user = show();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "+ new instruction…" }));
    const name = screen.getByRole("textbox", { name: "Name, instruction 3" });
    expect(name).toHaveFocus();
    await user.type(name, "style.md");
    await user.type(screen.getByRole("textbox", { name: "Read when, style.md" }), "you write user-facing text");
    await user.type(screen.getByRole("textbox", { name: "Body, style.md" }), "Sentence case.");
    await user.click(row("style.md"));
    expect(row("style.md")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("textbox", { name: "Body, style.md" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "+ new resource…" }));
    await user.type(screen.getByRole("textbox", { name: "Name, row 3" }), "manual");
    await user.tab();
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind, row 3" }), "doc");
    await user.tab();
    await user.type(screen.getByRole("textbox", { name: "Ref, row 3" }), "manual/");
    await user.click(save());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0]![0]).toEqual({
      core: filledContext.core,
      leaves: [...filledContext.leaves.map(({ name, readWhen, body }) => ({ name, readWhen, body })), { name: "style.md", readWhen: "you write user-facing text", body: "Sentence case." }],
      resources: [...filledContext.resources, { name: "manual", kind: "doc", ref: "manual/", note: "" }],
    });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Save context" })).toBeNull());
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  it("blocks saving while a field has a problem, shown at the field", async () => {
    const user = show();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(row("release.md"));
    const when = screen.getByRole("textbox", { name: "Read when, release.md" });
    await user.clear(when);
    expect(screen.getByText("Required")).toBeInTheDocument();
    expect(when).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("1 problem to fix before saving")).toBeInTheDocument();
    expect(save()).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Remove row 1" }));
    await user.click(screen.getByRole("button", { name: "+ new resource…" }));
    expect(screen.getByText("3 problems to fix before saving")).toBeInTheDocument();
    await user.type(when, "you release");
    await user.click(screen.getByRole("button", { name: "Remove row 2" }));
    expect(screen.getByText("Ready to save")).toBeInTheDocument();
    expect(save()).toBeEnabled();
  });

  it("shows the server's field errors at their fields until the field changes", async () => {
    onSave = vi.fn(() => Promise.reject(new ApiError(400, "bad_request", "project context: …", [
      { field: "leaves[1].readWhen", message: "leaf \"manual.md\" needs a better read-when" },
      { field: "resources[0].ref", message: "ref is not a repo" },
    ])));
    const user = show();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(save());
    expect(await screen.findByText("Leaf \"manual.md\" needs a better read-when")).toBeInTheDocument();
    expect(row("manual.md")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Ref is not a repo")).toBeInTheDocument();
    expect(screen.getByText("2 problems to fix before saving")).toBeInTheDocument();
    expect(save()).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "Read when, manual.md" }), "!");
    expect(screen.queryByText("Leaf \"manual.md\" needs a better read-when")).toBeNull();
    expect(screen.getByText("1 problem to fix before saving")).toBeInTheDocument();
  });

  it("shows a refusal without fields above the footer, keeping the edits", async () => {
    onSave = vi.fn(() => Promise.reject(new ApiError(412, "stale", "The branch moved: reload")));
    const user = show();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.type(screen.getByRole("textbox", { name: "Core instructions" }), " More.");
    await user.click(save());
    expect(await screen.findByText("The branch moved: reload")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Core instructions" })).toHaveValue(filledContext.core + " More.");
  });

  it("shows the saved YAML, and clears the context after confirming", async () => {
    const user = show();
    await user.click(screen.getByRole("button", { name: "View as YAML" }));
    expect(screen.getByText(/read-when: you are cutting a release/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Hide YAML" }));
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Clear context" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Remove this project agent context?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Clear context" }));
    await waitFor(() => expect(onClear).toHaveBeenCalledTimes(1));
  });

  it("clears rather than saves a card emptied of everything", async () => {
    const user = show({ ...role, leaves: [] });
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.clear(screen.getByRole("textbox", { name: "Core instructions" }));
    await user.click(save());
    await waitFor(() => expect(onClear).toHaveBeenCalledTimes(1));
    expect(onSave).not.toHaveBeenCalled();
  });

  it("is the same card without Resources for a role", async () => {
    const user = show(role);
    expect(screen.getByText("role — what every agent of this role is told when raised")).toBeInTheDocument();
    expect(screen.queryByText("Resources")).toBeNull();
    expect(screen.queryByText("includes the one-line resources pointer")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.queryByText("+ new resource…")).toBeNull();
    await user.click(save());
    expect(onSave.mock.calls[0]![0]).not.toHaveProperty("resources");
  });

  it("is the same card without Resources for the Jam-wide context, against its budget", async () => {
    const user = show(jam);
    expect(screen.getByText("Jam-wide — what every agent on this Jam is told, last")).toBeInTheDocument();
    expect(screen.queryByText("Resources")).toBeNull();
    expect(screen.getByText(`${jam.coreBytes} / 800 bytes`)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("textbox", { name: "Core instructions" }));
    await user.paste("x".repeat(700));
    expect(screen.getByText("Over budget: move detail into progressive instructions")).toBeInTheDocument();
    expect(save()).toBeDisabled();
  });

  it("says what is missing when empty", () => {
    show(emptyContext);
    expect(screen.getByText("No core instructions.")).toBeInTheDocument();
    expect(screen.getAllByText("None.")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "View as YAML" })).toBeNull();
  });
});
