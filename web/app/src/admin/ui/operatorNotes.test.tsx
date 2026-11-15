import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { dest } from "../specs/fixtures";
import { kit } from "../kits/fixtures";
import { setBranch } from "../branch/branch";

const notes = { markdown: "Ask **ops** first.", html: "<p>Ask <strong>ops</strong> first.</p>" };
const base = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);

beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("operator notes", () => {
  it("are shown rendered, read-only outside a branch", async () => {
    setBranch("");
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ notes }))));
    renderAdmin(adminBoot, ADMIN + "config/destinations/gh");
    expect(await screen.findByText("ops", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText(/never shown to agents/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });

  it("are hidden outside a branch when there are none", async () => {
    setBranch("");
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest())));
    renderAdmin(adminBoot, ADMIN + "config/destinations/gh");
    await screen.findByRole("heading", { name: "gh" });
    expect(screen.queryByText("Operator notes")).toBeNull();
  });

  it("are added on a branch, posted to the object's write, and the page reloaded", async () => {
    base();
    let sent: unknown;
    let now = dest();
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(now)),
      http.post("/app/admin/api/destinations/gh/operator-notes", async ({ request }) => {
        sent = await request.json();
        now = dest({ notes });
        return HttpResponse.json(notes);
      }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/destinations/gh");
    await user.click(await screen.findByRole("button", { name: "Add notes" }));
    await user.type(screen.getByLabelText(/^Notes/), "Ask **ops** first.");
    await user.click(screen.getByRole("button", { name: "Save notes" }));
    await waitFor(() => expect(sent).toEqual({ notes: "Ask **ops** first." }));
    expect(await screen.findByText("ops", { selector: "strong" })).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Notes/)).toBeNull();
    const card = screen.getByRole("heading", { name: "Operator notes" }).closest<HTMLElement>(".mantine-Card-root")!;
    expect(within(card).getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  it("of a kit are shown only, naming the file that holds them", async () => {
    base();
    server.use(http.get("/app/admin/api/kits/web", () => HttpResponse.json(kit({ notes }))));
    renderAdmin(adminBoot, ADMIN + "config/kits/web");
    expect(await screen.findByText(/edit operator-notes in its kit.yml/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });
});
