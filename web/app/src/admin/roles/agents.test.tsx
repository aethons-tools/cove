import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { StatusBadge } from "../ui/StatusBadge";
import { agents } from "./fixtures";

const tree = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);
const at = ADMIN + "config/projects/acme/agents";

describe("a project's agents", () => {
  it("lists roles and agents with their links", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents())));
    renderAdmin(adminBoot, at);
    expect((await screen.findAllByRole("link", { name: /^coder/ }))[0]).toHaveAttribute("href", at + "/coder");
    expect(screen.getByRole("link", { name: "ic" })).toHaveAttribute("href", ADMIN + "agents/ses_1");
    expect(screen.getByRole("link", { name: "web" })).toHaveAttribute("href", ADMIN + "config/kits/web");
    expect(screen.getByText("gh → gh-pat")).toBeInTheDocument();
    expect(screen.getByTitle("role coder: kit web not found")).toBeInTheDocument();
  });

  it("creates a role and opens it", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents({ roles: [] }))),
      http.post("/app/admin/api/projects/acme/roles", async ({ request }) => { sent = await request.json(); return HttpResponse.json({ id: "rev", path: "config/projects/acme/agents/rev" }, { status: 201 }); }),
      http.get("/app/admin/api/projects/acme/roles/rev", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("New role", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Role name/), "rev");
    await user.type(screen.getByLabelText(/^TTL/), "60");
    await user.click(screen.getByRole("button", { name: "Create role" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(at + "/rev"));
    expect(sent).toEqual({ name: "rev", destinations: "", ttlSeconds: "60", kit: "", modelSpec: "" });
  });

  it("shows a refused create inline", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents())),
      http.post("/app/admin/api/projects/acme/roles", () => HttpResponse.json({ error: "role exists; edit it on its page", code: "conflict" }, { status: 409 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("New role", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Role name/), "coder");
    await user.click(screen.getByRole("button", { name: "Create role" }));
    expect(await screen.findByText("role exists; edit it on its page")).toBeInTheDocument();
  });

  it("requests an agent and says so", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents())),
      http.post("/app/admin/api/projects/acme/roles/coder/request", () => HttpResponse.json({ id: "ses_5", name: "coder-1", message: "Requested agent coder-1 (ses_5); it will squawk you on the intercom." })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Request" }));
    expect(await screen.findByText("Requested agent coder-1 (ses_5); it will squawk you on the intercom.")).toBeInTheDocument();
  });

  it("shows a refused request", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents())),
      http.post("/app/admin/api/projects/acme/roles/coder/request", () => HttpResponse.json({ error: "sign in to request an agent", code: "forbidden" }, { status: 403 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Request" }));
    expect(await screen.findByText("sign in to request an agent")).toBeInTheDocument();
  });

  it("offers no Request without a supervisor", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme/agents", () => HttpResponse.json(agents({ canRequest: false }))));
    renderAdmin(adminBoot, at);
    await screen.findAllByRole("link", { name: /^coder/ });
    expect(screen.queryByRole("button", { name: "Request" })).toBeNull();
  });
});

describe("StatusBadge", () => {
  it("shows lost in red", () => {
    render(<MantineProvider theme={theme} env="test"><StatusBadge status="lost" /></MantineProvider>);
    expect(screen.getByText("lost").closest(".mantine-Badge-root")).toHaveAttribute("data-tone", "red");
  });
});
