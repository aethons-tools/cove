import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { project } from "./fixtures";
import type { UserDetail } from "../api/types";
import { setBranch } from "../branch/branch";

const tree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const at = ADMIN + "config/projects/acme/members";
const alice: UserDetail = { user: { id: "usr_1", name: "alice", logins: [], accounts: [], projects: ["acme"] }, removed: false, loginSpec: "", oidcSpec: "", connections: [], appTokens: [] };

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("project members", () => {
  it("adds a member", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ members: [] }))),
      http.get("/app/admin/suggest", () => HttpResponse.json(["alice"])),
      http.post("/app/admin/api/projects/acme/members", async ({ request }) => { sent = await request.json(); return HttpResponse.json(project()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("No members.")).toBeInTheDocument();
    await user.click(screen.getByText("Add member", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^User/), "alice");
    await user.type(screen.getByLabelText(/^Delivery/), "discord:chan-1");
    await user.click(screen.getByRole("button", { name: "Add member" }));
    await waitFor(() => expect(sent).toEqual({ user: "alice", delivery: "discord:chan-1", add: true }));
    expect(await screen.findByRole("link", { name: "alice" })).toHaveAttribute("href", at + "/usr_1");
  });

  it("edits a member's delivery", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.post("/app/admin/api/projects/acme/members", async ({ request }) => { sent = await request.json(); return HttpResponse.json(project()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit"));
    const box = screen.getByDisplayValue("discord:chan-1");
    await user.clear(box);
    await user.type(box, "discord:chan-2");
    await user.click(screen.getByRole("button", { name: "Save alice" }));
    await waitFor(() => expect(sent).toEqual({ user: "usr_1", delivery: "discord:chan-2", add: false }));
  });

  it("switches to Project not found when a write finds the project gone", async () => {
    tree();
    let gets = 0;
    server.use(
      http.get("/app/admin/api/projects/acme", () => (++gets === 1 ? HttpResponse.json(project()) : HttpResponse.json({ error: "no such project", code: "not_found" }, { status: 404 }))),
      http.post("/app/admin/api/projects/acme/members", () => HttpResponse.json({ error: 'project "acme" not found', code: "not_found" }, { status: 404 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit"));
    await user.click(screen.getByRole("button", { name: "Save alice" }));
    expect(await screen.findByText("Project not found")).toBeInTheDocument();
  });

  it("refreshes a member's edit box when their delivery changes", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.get("/app/admin/suggest", () => HttpResponse.json([])),
      http.post("/app/admin/api/projects/acme/members", () => HttpResponse.json(project({ members: [{ ...project().members[0]!, deliverySpec: "discord:new", delivery: [{ service: "discord", address: "new" }] }] }))),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Add member", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^User/), "alice");
    await user.click(screen.getByRole("button", { name: "Add member" }));
    await screen.findByText("new");
    await user.click(screen.getByText("Edit"));
    expect(screen.getByDisplayValue("discord:new")).toBeInTheDocument();
  });

  it("removes a member after confirming", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.delete("/app/admin/api/projects/acme/members/usr_1", () => HttpResponse.json(project({ members: [] }))),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Remove" }));
    expect(await screen.findByText("Remove alice from acme?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("No members.")).toBeInTheDocument();
  });

  it("shows a member leaf as the user's page in the project", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(alice)),
      http.delete("/app/admin/api/projects/acme/members/usr_1", () => HttpResponse.json(project({ members: [] }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at + "/usr_1");
    expect(await screen.findByText("Member of acme")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Rename" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove from acme" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove from acme" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(at));
  });

  it("says when the user isn't a member", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ members: [] }))),
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(alice)),
    );
    renderAdmin(adminBoot, at + "/usr_1");
    expect(await screen.findByText("alice is not a member of acme.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove from acme" })).toBeNull();
  });
});
