import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { spec, specFields } from "./fixtures";
import { setBranch } from "../branch/branch";

const base = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const at = ADMIN + "config/model-specs/fast";
const row = { name: "fast", type: "claude", version: "2.1.3", versionConstraint: "2.x", principal: "anthropic", modelId: "claude-opus-5-5", effort: "high", mode: "", provider: "anthropic", usedBy: 1 };
const list = (specs = [row]) => ({ specs, new: specFields({ version: "", principal: "" }), choices: spec().choices });

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("model specs", () => {
  it("lists them and creates one", async () => {
    base();
    let sent: Record<string, unknown> = {};
    server.use(
      http.get("/app/admin/api/model-specs", () => HttpResponse.json(list())),
      http.post("/app/admin/api/model-specs", async ({ request }) => { sent = await request.json() as Record<string, unknown>; return HttpResponse.json({ id: "slow", path: "config/model-specs/slow" }, { status: 201 }); }),
      http.get("/app/admin/api/model-specs/slow", () => HttpResponse.json(spec({ name: "slow" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/model-specs");
    expect(await screen.findByRole("link", { name: "fast" })).toHaveAttribute("href", at);
    expect(screen.getByText("2.x")).toBeInTheDocument();
    await user.click(screen.getByText("New model-spec", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Name/), "slow");
    await user.type(screen.getByLabelText(/^Version \*/), "2.1.3");
    await user.selectOptions(screen.getByLabelText(/^Principal credential/), "anthropic");
    await user.click(screen.getByRole("button", { name: "Create model-spec" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/model-specs/slow"));
    expect(sent).toMatchObject({ name: "slow", type: "claude", version: "2.1.3", principal: "anthropic", provider: "anthropic" });
  });

  it("keeps a stale principal selected", async () => {
    base();
    server.use(http.get("/app/admin/api/model-specs/fast", () => HttpResponse.json(spec({ fields: specFields({ principal: "retired" }), choices: { ...spec().choices, principals: ["anthropic", "retired"] } }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit model-spec", { selector: "summary" }));
    expect((screen.getByLabelText(/^Principal credential/) as HTMLSelectElement).value).toBe("retired");
  });

  it("shows a refusal inline and keeps the typed settings", async () => {
    base();
    server.use(
      http.get("/app/admin/api/model-specs/fast", () => HttpResponse.json(spec())),
      http.post("/app/admin/api/model-specs/fast", () => HttpResponse.json({ error: "claude.settings is not a JSON object", code: "bad_request" }, { status: 400 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit model-spec", { selector: "summary" }));
    const settings = screen.getByLabelText(/^Settings/);
    await user.type(settings, "[[]");
    await user.click(screen.getByRole("button", { name: "Save model-spec" }));
    expect(await screen.findByText("claude.settings is not a JSON object")).toBeInTheDocument();
    expect(settings).toHaveValue("[]");
  });

  it("shows a refused delete", async () => {
    base();
    server.use(
      http.get("/app/admin/api/model-specs", () => HttpResponse.json(list())),
      http.delete("/app/admin/api/model-specs/fast", () => HttpResponse.json({ error: "model-spec fast is used by acme/coder", code: "conflict" }, { status: 409 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/model-specs");
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Delete model-spec fast?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    expect(await screen.findByText("model-spec fast is used by acme/coder")).toBeInTheDocument();
  });

  it("shows its uses, labelling implicit ones", async () => {
    base();
    server.use(http.get("/app/admin/api/model-specs/fast", () => HttpResponse.json(spec({ headerRules: [{ name: "anthropic-beta", op: "+=", value: "oauth-1" }] }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "acme/coder" })).toHaveAttribute("href", ADMIN + "config/projects/acme/agents/coder");
    expect(screen.getByText("no model-spec set")).toBeInTheDocument();
    expect(screen.getByText("anthropic-beta += oauth-1")).toBeInTheDocument();
  });

  it("says when it doesn't exist", async () => {
    base();
    server.use(http.get("/app/admin/api/model-specs/gone", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "config/model-specs/gone");
    expect(await screen.findByText("Model-spec not found")).toBeInTheDocument();
  });
});
