import { describe, expect, it, vi } from "vitest";
import { ApiError, JamApi, SignedOutError } from "../src/api";

const mk = (res: Response) => {
  const f = vi.fn(async (..._a: unknown[]) => res);
  return { f, api: new JamApi("https://j", "tok", f as unknown as typeof fetch) };
};

describe("JamApi", () => {
  it("sends bearer, Accept json and redirect error", async () => {
    const { f, api } = mk(Response.json({ channels: [] }));
    await api.rail();
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://j/app/user/api/rail");
    expect(init.headers).toMatchObject({ Authorization: "Bearer tok", Accept: "application/json" });
    expect(init.redirect).toBe("error");
  });
  it("pushFocus posts the channel and accepts 204", async () => {
    const { f, api } = mk(new Response(null, { status: 204 }));
    await api.pushFocus("chn_x");
    await api.pushFocus(null).catch(() => undefined);
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://j/app/user/push/focus");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ channel: "chn_x" });
  });
  it("401 throws SignedOutError", async () => {
    await expect(mk(new Response("no", { status: 401 })).api.rail()).rejects.toBeInstanceOf(SignedOutError);
  });
  it("404 ApiError carries a JSON message", async () => {
    const e = await mk(Response.json({ error: "no such session", code: "not_found" }, { status: 404 })).api.sessionHead("ses_a").catch((x) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(e).toMatchObject({ message: "no such session", status: 404, code: "not_found" });
  });
  it("404 ApiError carries a text message", async () => {
    const e = await mk(new Response("studio not found\n", { status: 404 })).api.issueCert("ses_a", "k").catch((x) => x);
    expect(e).toMatchObject({ message: "studio not found", status: 404, code: "" });
  });
  it("issueCert posts {public_key}", async () => {
    const { f, api } = mk(Response.json({ certificate: "C", expires_at: "E" }));
    expect(await api.issueCert("ses_abc", "ssh-ed25519 AAA")).toEqual({ certificate: "C", expiresAt: "E" });
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://j/me/studios/ses_abc/ssh/cert");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ public_key: "ssh-ed25519 AAA" });
  });
  it("encodes path segments", async () => {
    const { f, api } = mk(Response.json({}));
    await api.conversation("a/b c");
    expect((f.mock.calls[0] as [string])[0]).toBe("https://j/app/user/api/conversations/a%2Fb%20c");
  });
});
