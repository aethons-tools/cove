import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { AgentListRow, AgentsResponse } from "../api/types";

const at = ADMIN + "agents";
const filters = [{ key: "", value: "All" }, { key: "running", value: "Running" }, { key: "waiting", value: "Waiting" }];
const row = (r: Partial<AgentListRow> = {}): AgentListRow => ({ id: "ses_1", name: "ic", kind: "standing", project: "acme", role: "coder", unit: "", status: "running", connector: "ok", image: "ok", lastSeen: "Oct 28 10:00:00", moreGrants: 0, hasStudio: true, attn: null, ...r });
const list = (l: Partial<AgentsResponse> = {}): AgentsResponse => ({ agents: [row()], filters, status: "", canEdit: true, ...l });
const base = (l: AgentsResponse = list()) => server.use(
  http.get("/app/admin/api/agents", ({ request }) => HttpResponse.json({ ...l, status: new URL(request.url).searchParams.get("status") ?? "" })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);

describe("the Agents list", () => {
  it("lists agents with their links, and the filters", async () => {
    base();
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "ic" })).toHaveAttribute("href", at + "/ses_1");
    expect(screen.getByRole("link", { name: "acme" })).toHaveAttribute("href", ADMIN + "config/projects/acme");
    expect(screen.getByRole("link", { name: "coder" })).toHaveAttribute("href", ADMIN + "config/projects/acme/agents/coder");
    expect(screen.getByRole("link", { name: "Running" })).toHaveAttribute("href", at + "?status=running");
    expect(screen.getByRole("link", { name: "All" })).toHaveAttribute("aria-current", "page");
  });

  it("enrolls and shows the token once", async () => {
    base(list({ agents: [] }));
    server.use(http.post("/app/admin/api/enrollments", () => HttpResponse.json({ id: "bot-1", token: "tok-SECRET" })));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Enroll agent", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Agent id/), "bot-1");
    await user.type(screen.getAllByLabelText(/^Role/)[1]!, "coder");
    await user.click(screen.getByRole("button", { name: "Enroll" }));
    expect(await screen.findByText(/Copy this identity token now — it is not shown again\./)).toBeInTheDocument();
    expect(screen.getByText("Enrolled bot-1.")).toBeInTheDocument();
    expect(screen.getByText("tok-SECRET")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
  });

  it("raises an agent and opens it", async () => {
    base(list({ agents: [] }));
    let sent: unknown;
    server.use(
      http.post("/app/admin/api/agents", async ({ request }) => { sent = await request.json(); return HttpResponse.json({ id: "ses_9", path: "agents/ses_9" }, { status: 201 }); }),
      http.get("/app/admin/api/agents/ses_9", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Raise agent", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Label/), "w1");
    await user.type(screen.getAllByLabelText(/^Role/)[0]!, "coder");
    await user.click(screen.getByRole("button", { name: "Raise" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(at + "/ses_9"));
    expect(sent).toEqual({ id: "w1", role: "coder", project: "default", unit: "", prompt: "" });
  });

  it("tears down after confirming and refreshes", async () => {
    let gets = 0;
    server.use(
      http.get("/app/admin/api/agents", () => { gets++; return HttpResponse.json(list()); }),
      http.delete("/app/admin/api/agents/ses_1/studio", () => new HttpResponse(null, { status: 204 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Teardown" }));
    expect(await screen.findByText("Tear down ses_1?")).toBeInTheDocument();
    const before = gets;
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Teardown" }));
    await waitFor(() => expect(gets).toBeGreaterThan(before));
  });

  it("offers no Raise or Teardown without a supervisor, and says when empty", async () => {
    base(list({ canEdit: false }));
    const { unmount } = renderAdmin(adminBoot, at);
    await screen.findByRole("link", { name: "ic" });
    expect(screen.queryByText("Raise agent")).toBeNull();
    expect(screen.queryByRole("button", { name: "Teardown" })).toBeNull();
    unmount();
    base(list({ agents: [] }));
    renderAdmin(adminBoot, at + "?status=waiting");
    expect(await screen.findByText("No agents with this status.")).toBeInTheDocument();
  });
});
