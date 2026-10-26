import { describe, expect, it } from "vitest";
import { act } from "react";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { start } from "./start";

describe("start", () => {
  it("shows why the bootstrap failed instead of redirecting", async () => {
    server.use(http.get("/app/user/api/bootstrap", () => new HttpResponse("bad gateway", { status: 502, headers: { "Content-Type": "text/plain" } })));
    const el = document.createElement("div");
    document.body.append(el);
    await act(() => start(el));
    expect(await screen.findByText("Jam answered 502. Try again shortly.")).toBeInTheDocument();
    el.remove();
  });
});
