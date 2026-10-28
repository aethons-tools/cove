import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { draftPage, draftView, kit } from "./fixtures";
import type { DraftEditor, DraftPage } from "../api/types";

const at = ADMIN + "config/kits/web";
const api = "/app/admin/api/kits/web/draft";
const ed = (e: Partial<DraftEditor> = {}): DraftEditor => ({ path: "docs/a.md", label: "docs/a.md", mode: "0644", exec: false, size: 4, etag: "e1", editable: true, text: "# a\n", invisible: false, note: "", ...e });
const withEditor = (e: Partial<DraftEditor> = {}) => draftPage({ draft: draftView({ editor: ed(e) }) });
const base = (page: (q: URLSearchParams) => DraftPage) => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/api/kits/web", () => HttpResponse.json(kit({ draft: { base: 2 } }))),
  http.get(api, ({ request }) => HttpResponse.json(page(new URL(request.url).searchParams))),
);
const ok = (p: DraftPage, message: string) => HttpResponse.json({ page: p, message });
const editorOn = (q: URLSearchParams) => { const f = q.get("file"); return f ? withEditor({ path: f, label: f }) : draftPage(); };

describe("the draft editor", () => {
  it("saves the text with the draft's etag", async () => {
    base(editorOn);
    let sent: unknown;
    server.use(http.post(api + "/save", async ({ request }) => { sent = await request.json(); return ok(withEditor({ text: "# b\n", etag: "e2" }), "saved docs/a.md"); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "?dfile=docs%2Fa.md");
    const box = await screen.findByLabelText("docs/a.md");
    await user.clear(box);
    await user.type(box, "# b");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toEqual({ etag: "e1", path: "docs/a.md", content: "# b", executable: false }));
    expect(await screen.findByText("saved docs/a.md")).toBeInTheDocument();
  });

  it("keeps the text when the save is stale, and offers Reload", async () => {
    base(editorOn);
    server.use(http.post(api + "/save", () => HttpResponse.json({ error: "the draft changed since it was read: your text is still in the editor — copy it before reloading", code: "stale" }, { status: 412 })));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "?dfile=docs%2Fa.md");
    const box = await screen.findByLabelText("docs/a.md");
    await user.type(box, "more");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/your text is still in the editor/)).toBeInTheDocument();
    expect(box).toHaveValue("# a\nmore");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("asks before leaving unsaved edits, and not after a save", async () => {
    base(editorOn);
    server.use(http.post(api + "/save", () => ok(withEditor({ etag: "e2" }), "saved docs/a.md")));
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at + "?dfile=docs%2Fa.md");
    await user.type(await screen.findByLabelText("docs/a.md"), "x");
    await user.click(within(screen.getByRole("table", { name: "Draft files" })).getByRole("link", { name: "kit.yml" }));
    expect(await screen.findByText("You have unsaved changes in the editor. Discard them?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(router.state.location.search).toBe("?dfile=docs%2Fa.md");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("saved docs/a.md");
    await user.click(within(screen.getByRole("table", { name: "Draft files" })).getByRole("link", { name: "kit.yml" }));
    await waitFor(() => expect(router.state.location.search).toBe("?dfile=kit.yml"));
  });

  it("shows why a file can't be edited, and sets its mode", async () => {
    base((q) => (q.get("file") ? withEditor({ editable: false, text: "", note: "binary file (4 B): not editable here — Download it, or Replace it with an upload" }) : draftPage()));
    let sent: unknown;
    server.use(http.post(api + "/mode", async ({ request }) => { sent = await request.json(); return ok(withEditor({ editable: false, exec: true }), "docs/a.md is now 0755"); }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "?dfile=docs%2Fa.md");
    expect(await screen.findByText(/binary file \(4 B\): not editable here/)).toBeInTheDocument();
    await user.click(screen.getAllByLabelText("Executable (0755)")[0]!);
    await user.click(screen.getByRole("button", { name: "Set mode" }));
    await waitFor(() => expect(sent).toEqual({ etag: "e1", path: "docs/a.md", executable: true }));
  });

  it("renames and deletes", async () => {
    base(editorOn);
    let renamed: unknown;
    server.use(
      http.post(api + "/rename", async ({ request }) => { renamed = await request.json(); return ok(withEditor({ path: "docs/b.md", label: "docs/b.md" }), "renamed docs/a.md to docs/b.md"); }),
      http.post(api + "/delete", () => ok(draftPage(), "removed docs/a.md")),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "?dfile=docs%2Fa.md");
    const to = await screen.findByLabelText(/^Rename to/);
    await user.clear(to);
    await user.type(to, "docs/b.md");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(renamed).toEqual({ etag: "e1", from: "docs/a.md", to: "docs/b.md" }));
    await screen.findByText("renamed docs/a.md to docs/b.md");
    await screen.findByLabelText("docs/b.md");
    const dels = await screen.findAllByRole("button", { name: "Delete" });
    await user.click(dels[dels.length - 1]!); // the editor's, after the kit's own
    expect(await screen.findByText("Remove docs/b.md from the draft?")).toBeInTheDocument();
  });
});
