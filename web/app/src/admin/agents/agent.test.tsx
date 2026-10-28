import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { AgentDetail, SetupResponse } from "../api/types";

const at = ADMIN + "agents/ses_1";
const api = "/app/admin/api/agents/ses_1";
const agent = (a: Partial<AgentDetail> = {}): AgentDetail => ({
  id: "ses_1", running: true, status: "waiting", kind: "personal", owner: "alice", ownerId: "usr_1", name: "", project: "acme", role: "coder", unit: "",
  hasActor: true, grants: [{ project: "acme", role: "coder", destinations: ["gh"] }], tokenExpiry: "",
  raised: "Oct 28 09:00", lastSeen: "Oct 28 10:00:00", lease: "", backend: "docker", location: "", waitingSince: "", waitSeq: 0, escalationTier: 0, tierPingedAt: "",
  escalationCategory: "", nags: 0, lastNagAt: "", commitSeq: 4, egressId: "abc", egressKnown: true, egressFailures: 0,
  access: { enabled: true, requester: { id: "usr_1", name: "alice" }, membersPreview: "members", membersSsh: "none", roleProject: "acme", roleName: "coder",
    shares: [{ userId: "usr_2", user: "carol", rights: ["ssh"], expiresAt: "2026-10-28 12:00 UTC", expired: false, grantedBy: "op:local" }], live: [], previewsOn: false, previews: [] },
  sessionsEnabled: true, streams: [{ streamId: "0123456789abcdef", started: "Oct 28 09:00", lastEvent: "Oct 28 10:00:00", events: 12 }],
  squawksConfigured: true, squawks: [], squawksMore: false, canEdit: true, setupEnabled: false, ...a,
});
const base = (a = agent()) => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
  http.get("/app/admin/api/agents", () => HttpResponse.json({ agents: [], filters: [], status: "", canEdit: true })),
  http.get(api, () => HttpResponse.json(a)),
);

describe("an agent's page", () => {
  it("shows its facts and links", async () => {
    base();
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("heading", { name: "ses_1" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "alice" })[0]).toHaveAttribute("href", ADMIN + "config/users/usr_1");
    expect(screen.getAllByRole("link", { name: "coder" })[0]).toHaveAttribute("href", ADMIN + "config/projects/acme/agents/coder");
    expect(screen.getByRole("link", { name: "Open live timeline" })).toHaveAttribute("href", at + "/session");
    expect(screen.getByRole("link", { name: "0123456789ab" })).toHaveAttribute("href", at + "/session?stream=0123456789abcdef");
  });

  it("removes a grant after confirming, and adds one", async () => {
    base();
    let added: unknown;
    server.use(
      http.delete(api + "/grants/acme/coder", () => HttpResponse.json(agent({ grants: [] }))),
      http.post(api + "/grants", async ({ request }) => { added = await request.json(); return HttpResponse.json(agent()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Remove grant acme/coder" }));
    expect(await screen.findByText("Remove coder grant on acme from ses_1?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("no grants")).toBeInTheDocument();
    await user.click(screen.getByText("+ Grant", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Role/), "rev");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(added).toEqual({ role: "rev", project: "default", destinations: "" }));
  });

  it("revokes and tears down, then goes to the list", async () => {
    base();
    server.use(http.delete("/app/admin/api/enrollments/ses_1", () => new HttpResponse(null, { status: 204 })));
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    expect(await screen.findByText("Revoke ses_1? This invalidates its token.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "agents"));
  });

  it("shows a gone agent's audit trail only", async () => {
    base(agent({ running: false, hasActor: false, grants: [], status: "", kind: "", canEdit: true }));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/This agent is no longer running/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Teardown" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke" })).toBeNull();
  });

  it("unshares after confirming", async () => {
    base();
    server.use(http.delete(api + "/shares/usr_2", () => HttpResponse.json(agent({ access: { ...agent().access, shares: [] } }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Unshare" }));
    expect(await screen.findByText("Unshare ses_1 from carol? Their open SSH connections close unless they have access another way.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Unshare" }));
    expect(await screen.findByText(/Not shared with anyone/)).toBeInTheDocument();
  });

  it("opens Setup with a diff toggle and a stale section's diff", async () => {
    base(agent({ setupEnabled: true }));
    const urls: string[] = [];
    const setup = (diffPath: string): SetupResponse => ({ note: "", stale: 1, sectionNames: ["egress"], managers: [], manager: "",
      sections: [{ name: "egress", source: "snapshot", reason: "", at: "", stale: true, open: true, body: "policy: role", head: "", files: [], diffed: false, diffTooLarge: false, diffSame: false, diff: [], diffNote: "", diffPath }] });
    server.use(
      http.get(api + "/setup", ({ request }) => { urls.push(new URL(request.url).search); return HttpResponse.json(setup("agents/ses_1/setup/sections/egress")); }),
      http.get(api + "/setup/sections/egress", () => HttpResponse.json({ ...setup("").sections[0], diffed: true, diff: [{ op: "+", text: "new.example.com" }] })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText(/^Setup/, { selector: "summary" }));
    await user.click(await screen.findByRole("button", { name: "stale vs role" }));
    expect(await screen.findByText("+ new.example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Diff vs role" }));
    await waitFor(() => expect(urls).toContain("?diff=role"));
  });

  it("says when nothing is known", async () => {
    base();
    server.use(http.get("/app/admin/api/agents/nobody", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "agents/nobody");
    expect(await screen.findByText("Agent not found")).toBeInTheDocument();
  });
});
