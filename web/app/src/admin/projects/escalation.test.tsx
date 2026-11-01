import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { project } from "./fixtures";
import type { ChainRow } from "../api/types";
import { setBranch } from "../branch/branch";

const tree = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json(["user:alice", "channel:eng"])),
);
const at = ADMIN + "config/projects/acme/escalation";
const def: ChainRow = { category: "", spec: "user:alice,user:ghost@15m", tiers: [{ timeout: "15m", targets: [{ text: "user:alice", unknown: false }, { text: "user:ghost", unknown: true }] }] };
const ops: ChainRow = { category: "ops", spec: "channel:eng@1h", tiers: [{ timeout: "1h", targets: [{ text: "channel:eng", unknown: false }] }] };

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("project escalation", () => {
  it("shows chains and flags unknown targets", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ escalation: [def, ops], defaultChain: def }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("default chain")).toBeInTheDocument();
    expect(screen.getByText("category")).toBeInTheDocument();
    expect(screen.getAllByText("user:ghost")[0]!.closest(".mantine-Badge-root")).toHaveAttribute("title", "Not on this project's roster");
    expect(screen.getAllByText("then wait 15m").length).toBeGreaterThan(0);
  });

  it("says when there is no policy", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("No escalation policy.")).toBeInTheDocument();
  });

  it("saves the default chain, keeping a refused spec", async () => {
    tree();
    const sent: unknown[] = [];
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.post("/app/admin/api/projects/acme/escalation", async ({ request }) => {
        const b = await request.json() as { tiers: string };
        sent.push(b);
        return b.tiers.includes("@soon")
          ? HttpResponse.json({ error: `tier "user:alice@soon": bad timeout`, code: "bad_request" }, { status: 400 })
          : HttpResponse.json(project({ escalation: [def], defaultChain: def }));
      }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit default chain"));
    const box = screen.getAllByLabelText(/^Tiers/)[0]!; // the default chain's form comes first
    await user.type(box, "user:alice@soon");
    await user.click(screen.getByRole("button", { name: "Save chain" }));
    expect(await screen.findByText(/bad timeout/)).toBeInTheDocument();
    expect(box).toHaveValue("user:alice@soon");
    await user.clear(box);
    await user.type(box, "user:alice@15m");
    await user.click(screen.getByRole("button", { name: "Save chain" }));
    await waitFor(() => expect(sent[1]).toEqual({ category: "", tiers: "user:alice@15m" }));
    expect(await screen.findByText("default chain")).toBeInTheDocument();
  });

  it("clears a chain after confirming", async () => {
    tree();
    let cleared = "";
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ escalation: [def], defaultChain: def }))),
      http.delete("/app/admin/api/projects/acme/escalation", ({ request }) => { cleared = new URL(request.url).search; return HttpResponse.json(project()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit default chain"));
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("Clear the default chain?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("No escalation policy.")).toBeInTheDocument();
    expect(cleared).toBe("?category=");
  });

  it("adds a category chain", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.post("/app/admin/api/projects/acme/escalation", async ({ request }) => { sent = await request.json(); return HttpResponse.json(project({ escalation: [ops] })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Add category chain"));
    await user.type(screen.getByLabelText(/^Category/), "ops");
    const boxes = screen.getAllByLabelText(/^Tiers/);
    await user.type(boxes[boxes.length - 1]!, "channel:eng@1h");
    await user.click(screen.getByRole("button", { name: "Add chain" }));
    await waitFor(() => expect(sent).toEqual({ category: "ops", tiers: "channel:eng@1h" }));
  });
});
