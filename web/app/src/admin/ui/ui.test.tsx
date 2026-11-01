import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider, TextInput } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { MemoryRouter } from "react-router";
import type { ReactNode } from "react";
import { theme } from "../../theme";
import { ApiError } from "../../api/client";
import { EditForm } from "./EditForm";
import { ConfirmAction } from "./ConfirmAction";
import { DataTable } from "./DataTable";
import { DiffView } from "./DiffView";
import { setBranch } from "../branch/branch";

const wrap = (ui: ReactNode) => render(
  <MantineProvider theme={theme} env="test"><Notifications /><MemoryRouter>{ui}</MemoryRouter></MantineProvider>,
);

// The write controls are config edits: they show on a branch.
beforeEach(() => setBranch("b"));
afterEach(() => setBranch(""));

describe("outside a branch", () => {
  it("an EditForm renders nothing, and its submit once a branch is open", async () => {
    setBranch("");
    wrap(<EditForm submit="Rename" onSubmit={async () => {}}><TextInput label="Name" /></EditForm>);
    expect(screen.queryByRole("button", { name: "Rename" })).toBeNull();
    expect(screen.queryByLabelText("Name")).toBeNull();
    act(() => setBranch("b"));
    expect(await screen.findByRole("button", { name: "Rename" })).toBeInTheDocument();
    act(() => setBranch(""));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Rename" })).toBeNull());
  });

  it("a live EditForm renders", () => {
    setBranch("");
    wrap(<EditForm live submit="Request" onSubmit={async () => {}} />);
    expect(screen.getByRole("button", { name: "Request" })).toBeInTheDocument();
  });

  it("a ConfirmAction renders nothing, and its button once a branch is open", async () => {
    setBranch("");
    wrap(<ConfirmAction label="Delete" confirm="Delete it?" onConfirm={async () => {}} />);
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    act(() => setBranch("b"));
    expect(await screen.findByRole("button", { name: "Delete" })).toBeInTheDocument();
  });

  it("a live ConfirmAction renders", () => {
    setBranch("");
    wrap(<ConfirmAction live label="Reset" confirm="Reset it?" onConfirm={async () => {}} />);
    expect(screen.getByRole("button", { name: "Reset" })).toBeInTheDocument();
  });
});

describe("EditForm", () => {
  it("asks first when it has a confirm", async () => {
    const user = userEvent.setup();
    const submit = vi.fn(async () => {});
    wrap(<EditForm submit="Rename" confirm="Sure?" onSubmit={submit}><TextInput label="Name" /></EditForm>);
    await user.click(screen.getByRole("button", { name: "Rename" }));
    expect(await screen.findByText("Sure?")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(submit).toHaveBeenCalledOnce());
  });

  it("shows a refusal inline and keeps what was typed", async () => {
    const user = userEvent.setup();
    wrap(<EditForm submit="Rename" onSubmit={async () => { throw new ApiError(409, "conflict", "name taken"); }}><TextInput label="Name" /></EditForm>);
    await user.type(screen.getByLabelText("Name"), "bob");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    expect(await screen.findByText("name taken")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("bob");
  });

  it("resets on success", async () => {
    const user = userEvent.setup();
    const reset = vi.fn();
    wrap(<EditForm submit="Save" onSubmit={async () => {}} resetOnSuccess={reset}><TextInput label="Name" /></EditForm>);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(reset).toHaveBeenCalledOnce());
  });
});

describe("ConfirmAction", () => {
  it("runs once however often it is clicked", async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const run = vi.fn(() => new Promise<void>((r) => { release = r; }));
    wrap(<ConfirmAction label="Remove" confirm="Remove it?" onConfirm={run} />);
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("Remove it?")).toBeInTheDocument();
    const buttons = screen.getAllByRole("button", { name: "Remove" });
    const act = buttons[buttons.length - 1]!;
    await user.click(act);
    await user.click(act);
    release();
    await waitFor(() => expect(run).toHaveBeenCalledOnce());
  });

  it("can be disabled", () => {
    wrap(<ConfirmAction label="Remove" confirm="Remove it?" onConfirm={async () => {}} disabled />);
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();
  });

  it("keeps a refusal on screen until dismissed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      wrap(<ConfirmAction label="Remove" confirm="Remove it?" onConfirm={async () => { throw new ApiError(409, "conflict", "in use"); }} />);
      await user.click(screen.getByRole("button", { name: "Remove" }));
      const buttons = await screen.findAllByRole("button", { name: "Remove" });
      await user.click(buttons[buttons.length - 1]!);
      expect(await screen.findByText("in use")).toBeInTheDocument();
      vi.advanceTimersByTime(10_000);
      expect(screen.getByText("in use")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a refusal as a notification", async () => {
    const user = userEvent.setup();
    wrap(<ConfirmAction label="Remove" confirm="Remove it?" onConfirm={async () => { throw new ApiError(409, "conflict", "in use"); }} />);
    await user.click(screen.getByRole("button", { name: "Remove" }));
    const buttons = await screen.findAllByRole("button", { name: "Remove" });
    await user.click(buttons[buttons.length - 1]!);
    expect(await screen.findByText("in use")).toBeInTheDocument();
  });
});

describe("display pieces", () => {
  it("DataTable shows its empty text", () => {
    wrap(<DataTable columns={[{ key: "n", label: "Name", render: (r: { n: string }) => r.n }]} rows={[]} rowKey={(r) => r.n} empty="No users." />);
    expect(screen.getByText("No users.")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Name" })).toBeInTheDocument();
  });

  it("DiffView prefixes lines", () => {
    wrap(<DiffView lines={[{ op: " ", text: "a" }, { op: "+", text: "b" }, { op: "-", text: "c" }, { op: "…", text: "3 lines" }]} />);
    expect(screen.getByText("+ b")).toBeInTheDocument();
    expect(screen.getByText("- c")).toBeInTheDocument();
    expect(screen.getByText("… 3 lines")).toBeInTheDocument();
  });
});
