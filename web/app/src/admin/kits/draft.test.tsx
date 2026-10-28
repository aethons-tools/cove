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
  http.get("/app/admin/api/kits/web", () => HttpResponse.json(kit({ draft: p.draft ? { base: p.draft.base } : null }))),
  http.get(api, () => HttpResponse.json(p)),
);
const ok = (p: DraftPage, message: string) => HttpResponse.json({ page: p, message });

describe("a kit's Draft", () => {
  it("opens a draft from a version", async () => {
    base(draftPage({ draft: null }));
    let sent: unknown;
    server.use(http.post(api, async ({ request }) => { sent = await request.json(); return ok(draftPage(), "opened a draft of web from v2"); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("No draft")).toBeInTheDocument();
    expect((screen.getByLabelText("Open from") as HTMLSelectElement).value).toBe("2");
    await user.click(screen.getByRole("button", { name: "Open draft" }));
    await waitFor(() => expect(sent).toEqual({ from: 2 }));
    expect(await screen.findByText("opened a draft of web from v2")).toBeInTheDocument();
    expect(screen.getByText(/base v2, current v2 · last saved by local/)).toBeInTheDocument();
  });

  it("discards after confirming", async () => {
    base(draftPage());
    let sent: unknown;
    server.use(http.post(api + "/discard", async ({ request }) => { sent = await request.json(); return ok(draftPage({ draft: null }), "discarded the draft of web"); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Discard" }));
    expect(await screen.findByText("Discard the draft of web? Its changes are lost.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(sent).toEqual({ etag: "e1" }));
    expect(await screen.findByText("No draft")).toBeInTheDocument();
  });

  it("shows the tree, nested, each file opening in place", async () => {
    base(draftPage());
    renderAdmin(adminBoot, at);
    const tree = await screen.findByRole("table", { name: "Draft files" });
    expect(within(tree).getByText("docs/")).toBeInTheDocument();
    expect(within(tree).getByRole("link", { name: "a.md" })).toHaveAttribute("href", at + "?dfile=docs%2Fa.md");
    expect(within(tree).getByRole("link", { name: "gone.txt" })).toHaveAttribute("href", at + "?dfile=gone.txt&ddiff=1");
    expect(within(tree).getAllByText("modified").length).toBeGreaterThan(0);
  });

  it("adds a file as an upload form", async () => {
    base(draftPage());
    let form: FormData | undefined;
    server.use(http.post(api + "/add", async ({ request }) => { form = await request.formData(); return ok(draftPage(), "added notes/n.md"); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Add file…", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Path/), "notes/n.md");
    await user.click(screen.getByLabelText("Executable (0755)"));
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(form?.get("path")).toBe("notes/n.md"));
    expect(form?.get("etag")).toBe("e1");
    expect(form?.get("executable")).toBe("1");
    expect(await screen.findByText("added notes/n.md")).toBeInTheDocument();
  });

  it("links each changed file to its diff, and warns when superseded", async () => {
    base(draftPage({ draft: draftView({ base: 1, current: 2, superseded: true }) }));
    renderAdmin(adminBoot, at);
    const changes = await screen.findByText("Changes vs v1");
    expect(changes).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "docs/a.md" })[0]).toHaveAttribute("href", at + "?dfile=docs%2Fa.md&ddiff=1");
    expect(screen.getByText(/publishing makes the draft's files the next version as they are/)).toBeInTheDocument();
  });
});
