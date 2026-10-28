import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { draftPage, draftView, kit } from "./fixtures";
import type { DraftPage } from "../api/types";

const at = ADMIN + "config/kits/web";
const api = "/app/admin/api/kits/web/draft";
const base = (p: DraftPage) => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/api/kits/web", () => HttpResponse.json(kit({ draft: { base: 2 } }))),
  http.get(api, () => HttpResponse.json(p)),
);
const ok = (p: DraftPage, message: string) => HttpResponse.json({ page: p, message });
const publishButton = () => screen.findByRole("button", { name: "Publish" });

describe("publishing a draft", () => {
  it("is blocked by problems", async () => {
    base(draftPage({ draft: draftView({ problems: ["kit.yml: unknown field bogus"] }) }));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("kit.yml: unknown field bogus")).toBeInTheDocument();
    const b = await publishButton();
    expect(b).toBeDisabled();
    expect(b).toHaveAttribute("title", "Fix the problems first");
  });

  it("needs the drift box on a managed kit, then confirms with the warning", async () => {
    const d = draftView({ managed: true });
    base(draftPage({ draft: d }));
    let sent: unknown;
    server.use(http.post(api + "/publish", async ({ request }) => {
      sent = await request.json();
      return ok(draftPage({ draft: null, published: { version: 3, base: 2, pinned: [], warning: "", drift: true, configFiles: [{ path: "kits/web/kit.yml", change: "modified" }] } }), "published web v3");
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findAllByText(d.driftWarning)).not.toHaveLength(0);
    expect(await publishButton()).toBeDisabled();
    await user.click(screen.getByLabelText("I understand this drifts from the config repo"));
    await user.click(await publishButton());
    expect(await screen.findByText(`Publish the draft as the next version of web? ${d.driftWarning}`)).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(sent).toEqual({ etag: "e1", pin: false, drift: true }));
    expect(await screen.findByText("Published v3")).toBeInTheDocument();
    expect(screen.getByText("kits/web/kit.yml")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "view v3" })).toHaveAttribute("href", at + "?v=3");
  });

  it("needs the drift box only with Pin when pinning drifts followers", async () => {
    base(draftPage({ draft: draftView({ pinDrift: ["child"], pinDriftNote: "Pinning rewrites config-managed child: with Pin followers, this publish drifts from the config repo." }) }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await publishButton()).toBeEnabled();
    await user.click(screen.getByLabelText(/^Pin followers/));
    expect(await publishButton()).toBeDisabled();
    expect(screen.getByText(/Pinning rewrites config-managed child/)).toBeInTheDocument();
    await user.click(screen.getByLabelText(/^I understand this drifts from the config repo/));
    expect(await publishButton()).toBeEnabled();
  });

  it("shows an upload over the limit inline", async () => {
    base(draftPage());
    server.use(http.post(api + "/add", () => HttpResponse.json({ error: "draft web: the file is over 4 MiB (limit 4 MiB per file)", code: "too_large" }, { status: 413 })));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Add file…", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Path/), "big.bin");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("draft web: the file is over 4 MiB (limit 4 MiB per file)")).toBeInTheDocument();
  });
});
