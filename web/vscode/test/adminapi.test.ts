import { describe, expect, it, vi } from "vitest";
import { AdminApi, NoAdminError } from "../src/admin/api";
import { ApiError } from "../src/api";

const mk = (res: () => Response, branch = "jam/alice") => {
  const f = vi.fn(async (..._a: unknown[]) => res());
  return { f, api: new AdminApi("https://j", "tok", f as unknown as typeof fetch, () => branch) };
};
const call = (f: ReturnType<typeof vi.fn>, i = 0) => f.mock.calls[i] as [string, RequestInit & { headers: Record<string, string> }];
const files = { etag: '"t"', base: 1, current: 1, problems: [], maxFile: 1, files: [] };

describe("AdminApi", () => {
  it("sends the bearer and the branch on every call, the head only on publish", async () => {
    const { f, api } = mk(() => Response.json(files));
    await api.files("web");
    await api.publish("web", '"t"', "abc123");
    const [url, init] = call(f);
    expect(url).toBe("https://j/app/admin/api/kits/web/draft/files");
    expect(init.headers).toMatchObject({ Authorization: "Bearer tok", "X-Jam-Config-Branch": "jam/alice" });
    expect(init.headers["X-Jam-Config-Head"]).toBeUndefined();
    expect(call(f, 1)[1].headers).toMatchObject({ "X-Jam-Config-Head": "abc123", "X-Jam-Config-Branch": "jam/alice" });
  });
  it("writes a file conditionally, raw, with its path encoded per segment", async () => {
    const { f, api } = mk(() => Response.json(files));
    await api.writeFile("web", "a b/c.sh", new TextEncoder().encode("x"), { sha256: "s1" });
    await api.writeFile("web", "new.sh", new Uint8Array(), "create", true);
    const [url, init] = call(f);
    expect(url).toBe("https://j/app/admin/api/kits/web/draft/files/a%20b/c.sh");
    expect(init.method).toBe("PUT");
    expect(init.headers["If-Match"]).toBe('"s1"');
    expect(init.headers["Content-Type"]).toBe("application/octet-stream");
    const [url2, init2] = call(f, 1);
    expect(url2).toBe("https://j/app/admin/api/kits/web/draft/files/new.sh?exec=1");
    expect(init2.headers["If-None-Match"]).toBe("*");
  });
  it("reads a file's bytes and sha256", async () => {
    const { api } = mk(() => new Response("hi", { headers: { ETag: '"abc"' } }));
    const r = await api.readFile("web", "kit.yml");
    expect(new TextDecoder().decode(r.bytes)).toBe("hi");
    expect(r.sha256).toBe("abc");
  });
  it("turns 403 no_admin into NoAdminError and 412 into ApiError changed", async () => {
    await expect(mk(() => Response.json({ error: "x", code: "no_admin" }, { status: 403 })).api.kits()).rejects.toBeInstanceOf(NoAdminError);
    const e = await mk(() => Response.json({ error: "changed", code: "changed" }, { status: 412 })).api.deleteFile("web", "a", "s").catch((x) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(e).toMatchObject({ status: 412, code: "changed" });
  });
  it("polls a build from an offset", async () => {
    const { f, api } = mk(() => Response.json({ state: "running", log: "", log_end: 5 }));
    await api.buildStatus("web", 5);
    expect(call(f)[0]).toBe("https://j/app/admin/api/kits/web/draft/build?since=5");
  });
});
