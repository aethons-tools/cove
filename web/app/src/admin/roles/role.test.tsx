import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { role } from "./fixtures";
import type { RoleDetail } from "../api/types";

const tree = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);
const at = ADMIN + "config/projects/acme/agents/coder";
const api = "/app/admin/api/projects/acme/roles/coder";
const serve = (d: RoleDetail = role()) => server.use(http.get(api, () => HttpResponse.json(d)));
const ok = (d: RoleDetail, message = "") => HttpResponse.json({ role: d, message });

describe("a role's page", () => {
  it("shows its facts and scope", async () => {
    tree(); serve();
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("heading", { name: "coder" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "acme" })[0]).toHaveAttribute("href", ADMIN + "config/projects/acme");
    expect(screen.getByRole("link", { name: "web" })).toHaveAttribute("href", ADMIN + "config/kits/web");
    expect(screen.getByRole("link", { name: "claude-default" })).toHaveAttribute("href", ADMIN + "config/model-specs/claude-default");
    expect(screen.getByText("TTL 1h")).toBeInTheDocument();
    expect(screen.getByText("unregistered")).toBeInTheDocument();
    expect(screen.getByText("role mapping")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "ses_9" })).toHaveAttribute("href", ADMIN + "agents/ses_9");
  });

  it("saves the scope from its prefilled form, and shows a refusal inline", async () => {
    tree(); serve();
    const sent: unknown[] = [];
    server.use(http.post(api + "/scope", async ({ request }) => {
      const b = await request.json(); sent.push(b);
      return sent.length === 1 ? HttpResponse.json({ error: 'kit "nope" does not exist', code: "bad_request" }, { status: 400 }) : ok(role({ ttl: "2h" }));
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit scope"));
    await user.click(screen.getByRole("button", { name: "Save scope" }));
    expect(await screen.findByText('kit "nope" does not exist')).toBeInTheDocument();
    expect(sent[0]).toEqual({ destinations: "gh=gh-pat,old", addressing: "user:*", ttl: "1h", kit: "web", modelSpec: "" });
    await user.click(screen.getByRole("button", { name: "Save scope" }));
    expect(await screen.findByText("TTL 2h")).toBeInTheDocument();
  });

  it("resets egress to the kit default after confirming", async () => {
    tree(); serve();
    server.use(http.delete(api + "/egress", () => ok(role({ egressManaged: false, egressDomains: [] }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit egress"));
    await user.click(screen.getByRole("button", { name: "Reset to kit default" }));
    expect(await screen.findByText("Drop coder's egress policy and use the kit default?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Reset to kit default" }));
    expect(await screen.findByText("Kit default — the role sets no egress policy.")).toBeInTheDocument();
  });

  it("saves allocation", async () => {
    tree(); serve();
    let sent: unknown;
    server.use(http.post(api + "/allocation", async ({ request }) => { sent = await request.json(); return ok(role()); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit allocation"));
    await user.type(screen.getByLabelText("Idle after"), "4h");
    await user.click(screen.getByRole("button", { name: "Save allocation" }));
    await waitFor(() => expect(sent).toEqual({ maxEphemeral: "", maxPersonal: "2", maxPersonalPerOwner: "", idleAfter: "4h", nagEvery: "", reclaimAfter: "" }));
  });

  it("declares, upgrades and dismisses standing agents", async () => {
    const managed = role({ managed: { commit: "abc1234", applied: "x" } });
    tree(); serve(managed);
    server.use(
      http.post(api + "/standing", () => ok(managed)),
      http.post(api + "/standing/ic/upgrade", () => ok(managed, "upgrade of standing agent ic queued; Jam restarts it once the image is ready and the agent is idle")),
      http.delete(api + "/standing/ic", () => ok(role({ standing: [] }))),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Upgrade" }));
    expect(await screen.findByText("Upgrade standing agent ic? It restarts on the current image; its conversation and workspace are kept.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Upgrade" }));
    expect(await screen.findByText(/upgrade of standing agent ic queued/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(await screen.findByText("Dismiss standing agent ic? It stops, and its conversation and workspace are deleted. This will drift from the config repo (managed · config @abc1234).")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Dismiss" }));
    expect(await screen.findByText("No standing agents.")).toBeInTheDocument();
  });

  it("deletes and goes back to Agents", async () => {
    tree(); serve();
    server.use(
      http.delete(api, () => new HttpResponse(null, { status: 204 })),
      http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json({ project: "acme", roles: [], agents: [], canRequest: false })),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Delete role coder from acme?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/projects/acme/agents"));
  });

  it("says when the role doesn't exist", async () => {
    tree();
    server.use(http.get(api, () => HttpResponse.json({ error: "no such role", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("Role not found")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to roles" })).toHaveAttribute("href", ADMIN + "config/projects/acme/agents");
  });
});
