import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { upload } from "./upload";

describe("upload", () => {
  it("posts the raw file with its name and resolves the ref", async () => {
    let seen: { name: string | null; type: string | null; body: string } | undefined;
    server.use(http.post("/app/user/uploads", async ({ request }) => {
      seen = { name: request.headers.get("X-Filename"), type: request.headers.get("Content-Type"), body: await request.text() };
      return HttpResponse.json({ ref: "blb_1" }, { status: 201 });
    }));
    const { done } = upload(new File(["hello"], "a b.txt", { type: "text/plain" }), () => {});
    await expect(done).resolves.toBe("blb_1");
    expect(seen).toEqual({ name: "a%20b.txt", type: "text/plain", body: "hello" });
  });

  it("rejects with the server's reason", async () => {
    server.use(http.post("/app/user/uploads", () => new HttpResponse("too large (max 25 MB)", { status: 413 })));
    await expect(upload(new File(["x"], "big.bin"), () => {}).done).rejects.toThrow("too large (max 25 MB)");
  });
});
