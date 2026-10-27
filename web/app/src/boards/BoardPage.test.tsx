import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";

// Excalidraw needs a real canvas: the editor is stubbed here (the browser
// test drives the real one).
vi.mock("./BoardEditor", () => ({ default: ({ id, readOnly }: { id: string; readOnly: boolean }) => <div data-testid="editor">{id}:{String(readOnly)}</div> }));

const serve = (status: number, readOnly = false) =>
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/boards/:id", ({ params }) => status === 200
      ? HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly })
      : HttpResponse.json({ error: "no such board", code: "not_found" }, { status })),
  );

describe("the board route", () => {
  it("opens the editor under a bar back to its conversation", async () => {
    serve(200);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_1");
    expect(await screen.findByTestId("editor")).toHaveTextContent("brd_1:false");
    expect(screen.getByRole("link", { name: "← Back to chat" })).toHaveAttribute("href", "/app/next/c/chn_a");
    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.queryByText("view only")).toBeNull();
  });

  it("says view only to a reader", async () => {
    serve(200, true);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_1");
    expect(await screen.findByTestId("editor")).toHaveTextContent("brd_1:true");
    expect(screen.getByText("view only")).toBeInTheDocument();
  });

  it("says so for a board that isn't there or isn't theirs, without loading the editor", async () => {
    serve(404);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_x");
    expect(await screen.findByText("This whiteboard isn't available to you.")).toBeInTheDocument();
    expect(screen.queryByTestId("editor")).toBeNull();
  });
});
