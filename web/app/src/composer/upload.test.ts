import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { afterEach } from "vitest";
import { setUploader, upload, uploadOutcome } from "./upload";

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

describe("upload seam", () => {
  afterEach(() => setUploader());
  it("uploadOutcome maps a status and body to a ref or an Error", () => {
    expect(uploadOutcome(201, '{"ref":"r1"}')).toBe("r1");
    expect(uploadOutcome(413, "too large\n")).toEqual(new Error("too large"));
    expect(uploadOutcome(201, "")).toEqual(new Error("failed 201"));
  });
  it("setUploader makes upload call the fake", async () => {
    const fake = vi.fn(() => ({ done: Promise.resolve("fake_ref"), abort: () => {} }));
    setUploader(fake);
    const f = new File(["x"], "x.txt");
    const cb = () => {};
    await expect(upload(f, cb).done).resolves.toBe("fake_ref");
    expect(fake).toHaveBeenCalledWith(f, cb);
  });
});
