import { describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { role } from "../roles/fixtures";
import type { SetupResponse, SetupSection } from "../api/types";

const sec = (s: Partial<SetupSection>): SetupSection => ({ name: "identity", source: "config", reason: "", at: "", stale: false, open: false, body: "", head: "", files: [], diffed: false, diffTooLarge: false, diffSame: false, diff: [], diffNote: "", diffPath: "", ...s });
const setup = (s: Partial<SetupResponse> = {}): SetupResponse => ({
  note: "", stale: 0, sectionNames: ["identity", "context", "egress"], managers: [], manager: "",
  sections: [sec({ name: "identity", open: true, body: "role acme/coder" }), sec({ name: "context", head: "2 files", files: [{ path: "CLAUDE.md", body: "Be kind.", bytes: 8 }] }), sec({ name: "egress", source: "unavailable", reason: "no supervisor on this Jam" })],
  ...s,
});
const at = ADMIN + "config/projects/acme/agents/coder";
const base = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
  http.get("/app/admin/api/projects/acme/roles/coder", () => HttpResponse.json(role({ setupEnabled: true }))),
);

describe("a role's Setup", () => {
  it("loads only when opened, then shows its sections", async () => {
    base();
    let reads = 0;
    server.use(http.get("/app/admin/api/projects/acme/roles/coder/setup", () => { reads++; return HttpResponse.json(setup()); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const open = await screen.findByText(/^Setup/, { selector: "summary" });
    expect(reads).toBe(0);
    await user.click(open);
    expect(await screen.findByText("role acme/coder")).toBeInTheDocument();
    expect(reads).toBe(1);
    expect(screen.getByText("no supervisor on this Jam")).toBeInTheDocument();
    expect(screen.getByText("CLAUDE.md")).toBeInTheDocument();
  });

  it("filters to one section and picks the manager", async () => {
    base();
    const urls: string[] = [];
    server.use(http.get("/app/admin/api/projects/acme/roles/coder/setup", ({ request }) => {
      urls.push(new URL(request.url).search);
      return HttpResponse.json(setup({ managers: ["m1", "m2"], manager: "m1" }));
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText(/^Setup/, { selector: "summary" }));
    await user.click(await screen.findByRole("button", { name: "egress" }));
    await waitFor(() => expect(urls).toContain("?sections=egress"));
    await user.click(screen.getByRole("button", { name: "m2" }));
    await waitFor(() => expect(urls).toContain("?sections=egress&manager=m2"));
  });

  it("says why it can't load", async () => {
    base();
    server.use(http.get("/app/admin/api/projects/acme/roles/coder/setup", () => HttpResponse.json({ error: 'unknown section "x"', code: "bad_request" }, { status: 400 })));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText(/^Setup/, { selector: "summary" }));
    expect(await screen.findByText('unknown section "x"')).toBeInTheDocument();
  });

  it("is not offered without setup", async () => {
    base();
    server.use(http.get("/app/admin/api/projects/acme/roles/coder", () => HttpResponse.json(role())));
    renderAdmin(adminBoot, at);
    await screen.findByRole("heading", { name: "coder" });
    expect(screen.queryByText(/^Setup/, { selector: "summary" })).toBeNull();
  });
});
